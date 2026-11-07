package investigate

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/starttoaster/reflex/internal/alertmanager"
	"github.com/starttoaster/reflex/internal/discord"
	"github.com/starttoaster/reflex/internal/metrics"
)

type fakeRunner struct {
	release chan struct{}
	run     Run
	err     error
}

func (f *fakeRunner) Investigate(ctx context.Context, _ *alertmanager.Webhook) (Run, error) {
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return Run{}, ctx.Err()
		}
	}
	return f.run, f.err
}

type sentMsg struct {
	url string
	msg discord.Message
}

type fakeSender struct {
	mu   sync.Mutex
	sent []sentMsg
}

func (f *fakeSender) Send(_ context.Context, url string, msg discord.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, sentMsg{url, msg})
	return nil
}

func newTestInvestigator(r Runner, s Sender, concurrency int) (*Investigator, *metrics.Metrics) {
	m := metrics.New()
	return New(Config{
		Runner: r, Sender: s, Metrics: m, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Concurrency: concurrency, Timeout: 5 * time.Second, SendTimeout: time.Second, DedupeTTL: time.Hour,
	}), m
}

func hook(groupKey string) *alertmanager.Webhook {
	return &alertmanager.Webhook{
		GroupKey: groupKey,
		Status:   alertmanager.StatusFiring,
		Alerts:   []alertmanager.Alert{{Status: alertmanager.StatusFiring, Labels: map[string]string{"alertname": "DiskSpaceLow"}}},
	}
}

func closeInvestigator(t *testing.T, inv *Investigator) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := inv.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestInvestigatorPostsResult(t *testing.T) {
	sender := &fakeSender{}
	runner := &fakeRunner{run: Run{Summary: "Disk fills from backups.", Model: "claude-test", Iterations: 3, ToolCalls: 2, Tokens: 84321}}
	inv, m := newTestInvestigator(runner, sender, 1)
	if !inv.Start(hook("g1"), "info-hook") {
		t.Fatal("did not start")
	}
	// Wait for the post before Close, which would cancel it.
	waitFor(t, func() bool { sender.mu.Lock(); defer sender.mu.Unlock(); return len(sender.sent) == 1 })
	closeInvestigator(t, inv)

	got := sender.sent[0]
	e := got.msg.Embeds[0]
	if got.url != "info-hook" || e.Description != "Disk fills from backups." || !strings.Contains(e.Title, "DiskSpaceLow") {
		t.Errorf("sent %+v", got)
	}
	if e.Footer == nil || e.Footer.Text != "Claude claude-test · 2 tool calls · 84k tokens" {
		t.Errorf("footer = %+v", e.Footer)
	}
	if v := testutil.ToFloat64(m.Investigations.WithLabelValues(OutcomeSucceeded)); v != 1 {
		t.Errorf("succeeded = %v", v)
	}
}

func TestInvestigatorPostsFailure(t *testing.T) {
	sender := &fakeSender{}
	inv, m := newTestInvestigator(&fakeRunner{err: errors.New("connect to mcp-grafana: refused")}, sender, 1)
	inv.Start(hook("g1"), "urgent-hook")
	waitFor(t, func() bool { sender.mu.Lock(); defer sender.mu.Unlock(); return len(sender.sent) == 1 })
	closeInvestigator(t, inv)
	if d := sender.sent[0].msg.Embeds[0].Description; !strings.Contains(d, "Investigation failed: connect to mcp-grafana") {
		t.Errorf("description = %q", d)
	}
	if v := testutil.ToFloat64(m.Investigations.WithLabelValues(OutcomeFailed)); v != 1 {
		t.Errorf("failed = %v", v)
	}
}

func TestInvestigatorDedupeAndBusy(t *testing.T) {
	runner := &fakeRunner{release: make(chan struct{})}
	inv, m := newTestInvestigator(runner, &fakeSender{}, 1)

	if !inv.Start(hook("g1"), "x") {
		t.Fatal("first start refused")
	}
	if inv.Start(hook("g1"), "x") {
		t.Error("same group started twice")
	}
	if inv.Start(hook("g2"), "x") {
		t.Error("started past the concurrency limit")
	}
	close(runner.release)
	closeInvestigator(t, inv)

	if v := testutil.ToFloat64(m.Investigations.WithLabelValues(OutcomeSkippedDuplicate)); v != 1 {
		t.Errorf("skipped_duplicate = %v", v)
	}
	if v := testutil.ToFloat64(m.Investigations.WithLabelValues(OutcomeSkippedBusy)); v != 1 {
		t.Errorf("skipped_busy = %v", v)
	}
}

func TestInvestigatorForgetAndExpiry(t *testing.T) {
	inv, _ := newTestInvestigator(&fakeRunner{}, &fakeSender{}, 4)
	now := time.Now()
	inv.now = func() time.Time { return now }

	inv.Start(hook("g1"), "x")
	inv.Forget("g1")
	if !inv.Start(hook("g1"), "x") {
		t.Error("forgotten group not investigated again")
	}
	now = now.Add(2 * time.Hour)
	if !inv.Start(hook("g1"), "x") {
		t.Error("expired group not investigated again")
	}
	closeInvestigator(t, inv)
}

func TestInvestigatorCloseCancels(t *testing.T) {
	sender := &fakeSender{}
	inv, _ := newTestInvestigator(&fakeRunner{release: make(chan struct{})}, sender, 1)
	inv.Start(hook("g1"), "x")
	closeInvestigator(t, inv)
	if len(sender.sent) != 0 {
		t.Errorf("posted during shutdown: %+v", sender.sent)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
