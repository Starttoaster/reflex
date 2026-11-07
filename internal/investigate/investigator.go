package investigate

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/starttoaster/reflex/internal/alertmanager"
	"github.com/starttoaster/reflex/internal/discord"
	"github.com/starttoaster/reflex/internal/metrics"
)

// Outcomes recorded in reflex_investigations_total.
const (
	OutcomeStarted          = "started"
	OutcomeSkippedDuplicate = "skipped_duplicate"
	OutcomeSkippedBusy      = "skipped_busy"
	OutcomeSkippedRateLimit = "skipped_rate_limited"
	OutcomeSucceeded        = "succeeded"
	OutcomeFailed           = "failed"
)

// Runner performs one investigation. *Agent is the real one.
type Runner interface {
	Investigate(ctx context.Context, w *alertmanager.Webhook) (Run, error)
}

// Sender delivers a message to a Discord webhook.
type Sender interface {
	Send(ctx context.Context, webhookURL string, msg discord.Message) error
}

// Config configures an Investigator.
type Config struct {
	Runner  Runner
	Sender  Sender
	Metrics *metrics.Metrics
	Log     *slog.Logger
	// Concurrency is how many investigations may run at once; more are skipped.
	Concurrency int
	// Timeout bounds one investigation.
	Timeout time.Duration
	// SendTimeout bounds the Discord post of its result.
	SendTimeout time.Duration
	// DedupeTTL is how long a group is remembered as investigated.
	DedupeTTL time.Duration
	// MaxPerHour caps investigations started in any rolling hour; more are
	// skipped. Zero means no cap.
	MaxPerHour int
}

// Investigator runs investigations in the background, at most once per alert
// group, and posts each result to the webhook the alert itself went to.
type Investigator struct {
	cfg    Config
	sem    chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	now    func() time.Time

	mu   sync.Mutex
	seen map[string]time.Time
	// started holds the start times of investigations in the last hour.
	started []time.Time
}

// New returns an Investigator ready to Start investigations.
func New(cfg Config) *Investigator {
	ctx, cancel := context.WithCancel(context.Background())
	return &Investigator{
		cfg:    cfg,
		sem:    make(chan struct{}, max(cfg.Concurrency, 1)),
		ctx:    ctx,
		cancel: cancel,
		now:    time.Now,
		seen:   map[string]time.Time{},
	}
}

// Start begins investigating w in the background unless its group has
// already been investigated or every slot is busy. It never blocks, and
// reports whether an investigation started.
func (i *Investigator) Start(w *alertmanager.Webhook, webhookURL string) bool {
	i.mu.Lock()
	now := i.now()
	for k, at := range i.seen {
		if now.Sub(at) > i.cfg.DedupeTTL {
			delete(i.seen, k)
		}
	}
	if _, dup := i.seen[w.GroupKey]; dup {
		i.mu.Unlock()
		i.count(OutcomeSkippedDuplicate)
		return false
	}
	i.started = slices.DeleteFunc(i.started, func(at time.Time) bool { return now.Sub(at) >= time.Hour })
	if i.cfg.MaxPerHour > 0 && len(i.started) >= i.cfg.MaxPerHour {
		i.mu.Unlock()
		i.count(OutcomeSkippedRateLimit)
		i.cfg.Log.Warn("investigation skipped: hourly limit reached", "group_key", w.GroupKey,
			"alertname", w.AlertName(), "max_per_hour", i.cfg.MaxPerHour)
		return false
	}
	select {
	case i.sem <- struct{}{}:
	default:
		i.mu.Unlock()
		i.count(OutcomeSkippedBusy)
		i.cfg.Log.Warn("investigation skipped: all slots busy", "group_key", w.GroupKey, "alertname", w.AlertName())
		return false
	}
	i.seen[w.GroupKey] = now
	i.started = append(i.started, now)
	i.wg.Add(1)
	i.mu.Unlock()

	i.count(OutcomeStarted)
	go func() {
		defer i.wg.Done()
		defer func() { <-i.sem }()
		i.run(w, webhookURL)
	}()
	return true
}

// Forget drops a group from the investigated set, so it is investigated
// again if it fires again after resolving.
func (i *Investigator) Forget(groupKey string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	delete(i.seen, groupKey)
}

// Close cancels running investigations and waits for them to stop, or for
// ctx to end.
func (i *Investigator) Close(ctx context.Context) error {
	i.cancel()
	done := make(chan struct{})
	go func() {
		i.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (i *Investigator) run(w *alertmanager.Webhook, webhookURL string) {
	log := i.cfg.Log.With("group_key", w.GroupKey, "alertname", w.AlertName())
	ctx, cancel := context.WithTimeout(i.ctx, i.cfg.Timeout)
	defer cancel()

	start := time.Now()
	run, err := i.cfg.Runner.Investigate(ctx, w)
	elapsed := time.Since(start)
	i.cfg.Metrics.InvestigationTime.Observe(elapsed.Seconds())
	i.cfg.Metrics.InvestigationToks.Add(float64(run.Tokens))
	if i.ctx.Err() != nil {
		log.Info("investigation abandoned: shutting down")
		return
	}

	attrs := []any{"duration_ms", elapsed.Milliseconds(), "model", run.Model, "iterations", run.Iterations,
		"tool_calls", run.ToolCalls, "tokens", run.Tokens}
	var msg discord.Message
	if err != nil {
		i.count(OutcomeFailed)
		log.Error("investigation failed", append(attrs, "err", err)...)
		msg = discord.FormatInvestigation(w, "Investigation failed: "+err.Error(), Footer(run), true)
	} else {
		i.count(OutcomeSucceeded)
		log.Info("investigation finished", append(attrs, "notes", strings.Join(run.Notes, "; "))...)
		msg = discord.FormatInvestigation(w, run.Summary, Footer(run), false)
	}

	sendCtx, cancelSend := context.WithTimeout(i.ctx, i.cfg.SendTimeout)
	defer cancelSend()
	if err := i.cfg.Sender.Send(sendCtx, webhookURL, msg); err != nil {
		i.cfg.Metrics.DiscordFailures.WithLabelValues("investigation").Inc()
		log.Error("discord send failed for investigation", "err", err)
	}
}

func (i *Investigator) count(outcome string) {
	i.cfg.Metrics.Investigations.WithLabelValues(outcome).Inc()
}

// Footer summarises what an investigation cost and anything that cut it short.
func Footer(run Run) string {
	if run.Iterations == 0 {
		return ""
	}
	parts := []string{"Claude " + run.Model, fmt.Sprintf("%d tool calls", run.ToolCalls), humanTokens(run.Tokens) + " tokens"}
	parts = append(parts, run.Notes...)
	return strings.Join(parts, " · ")
}

func humanTokens(n int64) string {
	if n < 1000 {
		return fmt.Sprint(n)
	}
	return fmt.Sprintf("%dk", (n+500)/1000)
}
