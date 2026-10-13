// Package server exposes reflex's HTTP endpoints.
package server

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/starttoaster/reflex/internal/alertmanager"
	"github.com/starttoaster/reflex/internal/discord"
	"github.com/starttoaster/reflex/internal/metrics"
	"github.com/starttoaster/reflex/internal/router"
)

// maxBody caps webhook bodies; Alertmanager groups are far smaller.
const maxBody = 4 << 20

// Sender delivers a formatted message to a Discord webhook.
type Sender interface {
	Send(ctx context.Context, webhookURL string, msg discord.Message) error
}

// Server handles Alertmanager webhooks.
type Server struct {
	Router   *router.Router
	Sender   Sender
	Webhooks map[router.Channel]string
	Metrics  *metrics.Metrics
	Log      *slog.Logger
	// SendTimeout bounds each Discord delivery.
	SendTimeout time.Duration
}

// Handler returns the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /alerts", s.handleAlerts)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.Handle("GET /metrics", promhttp.HandlerFor(s.Metrics.Registry, promhttp.HandlerOpts{}))
	return mux
}

func (s *Server) handleAlerts(w http.ResponseWriter, r *http.Request) {
	hook, err := alertmanager.Parse(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		s.Metrics.InvalidPayloads.Inc()
		s.Log.Warn("rejected webhook", "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.Metrics.NotificationsRecv.WithLabelValues(hook.Status).Inc()
	s.Metrics.AlertsReceived.WithLabelValues(alertmanager.StatusFiring).Add(float64(len(hook.Firing())))
	s.Metrics.AlertsReceived.WithLabelValues(alertmanager.StatusResolved).Add(float64(len(hook.Resolved())))

	d := s.Router.Route(r.Context(), hook)
	s.Metrics.Decisions.WithLabelValues(string(d.Channel), string(d.Reason)).Inc()
	if d.JevCalled {
		s.Metrics.JevLatency.Observe(d.JevLatency.Seconds())
	}
	s.logDecision(hook, d)

	ctx, cancel := context.WithTimeout(r.Context(), s.SendTimeout)
	defer cancel()
	if err := s.Sender.Send(ctx, s.Webhooks[d.Channel], discord.Format(hook, d.Footer())); err != nil {
		s.Metrics.DiscordFailures.WithLabelValues(string(d.Channel)).Inc()
		s.Log.Error("discord send failed", "group_key", hook.GroupKey, "channel", d.Channel, "err", err)
		// A 5xx makes Alertmanager retry the notification.
		http.Error(w, "discord send failed", http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// logDecision logs every routing decision with what label routing alone
// would have chosen, so Jev's choices can be compared during a trial.
func (s *Server) logDecision(hook *alertmanager.Webhook, d router.Decision) {
	attrs := []any{
		"group_key", hook.GroupKey,
		"alertname", hook.AlertName(),
		"status", hook.Status,
		"alerts", len(hook.Alerts),
		"severity", d.Severity,
		"channel", d.Channel,
		"reason", d.Reason,
		"label_channel", router.LabelChannel(d.Severity),
	}
	if d.JevCalled {
		attrs = append(attrs, "jev_latency_ms", d.JevLatency.Milliseconds())
	}
	if d.Model != "" {
		attrs = append(attrs, "p_page", d.PageProbability, "confidence", d.Confidence, "model", d.Model)
	}
	if d.Urgency != nil {
		attrs = append(attrs, "urgency", *d.Urgency)
	}
	if d.Err != nil {
		attrs = append(attrs, "err", d.Err)
	}
	s.Log.Info("routed notification", attrs...)
}
