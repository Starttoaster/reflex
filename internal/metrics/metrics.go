// Package metrics defines reflex's Prometheus metrics.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Metrics groups every collector reflex exports.
type Metrics struct {
	Registry          *prometheus.Registry
	NotificationsRecv *prometheus.CounterVec
	AlertsReceived    *prometheus.CounterVec
	Decisions         *prometheus.CounterVec
	JevLatency        prometheus.Histogram
	DiscordFailures   *prometheus.CounterVec
	InvalidPayloads   prometheus.Counter
	Investigations    *prometheus.CounterVec
	InvestigationTime prometheus.Histogram
	InvestigationToks prometheus.Counter
}

// New registers reflex's metrics, plus Go and process metrics, on a fresh registry.
func New() *Metrics {
	m := &Metrics{
		Registry: prometheus.NewRegistry(),
		NotificationsRecv: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "reflex_notifications_received_total",
			Help: "Alertmanager webhook notifications received, by group status.",
		}, []string{"status"}),
		AlertsReceived: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "reflex_alerts_received_total",
			Help: "Alerts received inside notifications, by alert status.",
		}, []string{"status"}),
		Decisions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "reflex_routing_decisions_total",
			Help: "Routing decisions, by channel and reason.",
		}, []string{"channel", "reason"}),
		JevLatency: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "reflex_jev_request_duration_seconds",
			Help:    "Latency of Jev requests, including failed and timed-out ones.",
			Buckets: []float64{0.05, 0.1, 0.2, 0.3, 0.5, 0.75, 1, 1.5, 2, 3},
		}),
		DiscordFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "reflex_discord_send_failures_total",
			Help: "Failed Discord webhook sends, by channel.",
		}, []string{"channel"}),
		InvalidPayloads: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "reflex_invalid_payloads_total",
			Help: "Webhook requests rejected because the body could not be parsed.",
		}),
		Investigations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "reflex_investigations_total",
			Help: "Claude investigations, by outcome: started, skipped_duplicate, skipped_busy, skipped_rate_limited, succeeded, failed.",
		}, []string{"outcome"}),
		InvestigationTime: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "reflex_investigation_duration_seconds",
			Help:    "Duration of finished Claude investigations, successful or not.",
			Buckets: []float64{5, 10, 20, 30, 60, 90, 120, 180, 300, 600},
		}),
		InvestigationToks: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "reflex_investigation_tokens_total",
			Help: "Billable Anthropic tokens spent on investigations, cache reads included.",
		}),
	}
	m.Registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.NotificationsRecv,
		m.AlertsReceived,
		m.Decisions,
		m.JevLatency,
		m.DiscordFailures,
		m.InvalidPayloads,
		m.Investigations,
		m.InvestigationTime,
		m.InvestigationToks,
	)
	return m
}
