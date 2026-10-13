// Package alertmanager defines the payload Alertmanager sends to webhook
// receivers (version 4) and helpers for reading it.
package alertmanager

import (
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// Status values used by both the group and individual alerts.
const (
	StatusFiring   = "firing"
	StatusResolved = "resolved"
)

// Webhook is the body of an Alertmanager webhook notification.
// See https://prometheus.io/docs/alerting/latest/configuration/#webhook_config.
type Webhook struct {
	Version           string            `json:"version"`
	GroupKey          string            `json:"groupKey"`
	TruncatedAlerts   int               `json:"truncatedAlerts"`
	Status            string            `json:"status"`
	Receiver          string            `json:"receiver"`
	GroupLabels       map[string]string `json:"groupLabels"`
	CommonLabels      map[string]string `json:"commonLabels"`
	CommonAnnotations map[string]string `json:"commonAnnotations"`
	ExternalURL       string            `json:"externalURL"`
	Alerts            []Alert           `json:"alerts"`
}

// Alert is a single alert inside a webhook notification.
type Alert struct {
	Status       string            `json:"status"`
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     time.Time         `json:"startsAt"`
	EndsAt       time.Time         `json:"endsAt"`
	GeneratorURL string            `json:"generatorURL"`
	Fingerprint  string            `json:"fingerprint"`
}

// Parse decodes and validates a webhook body.
func Parse(r io.Reader) (*Webhook, error) {
	var w Webhook
	if err := json.NewDecoder(r).Decode(&w); err != nil {
		return nil, fmt.Errorf("decode webhook: %w", err)
	}
	if w.Version != "4" {
		return nil, fmt.Errorf("unsupported webhook version %q (want \"4\")", w.Version)
	}
	if w.Status != StatusFiring && w.Status != StatusResolved {
		return nil, fmt.Errorf("unknown group status %q", w.Status)
	}
	if len(w.Alerts) == 0 {
		return nil, fmt.Errorf("webhook has no alerts")
	}
	return &w, nil
}

// AlertName returns the group's alertname, falling back to the first alert's.
func (w *Webhook) AlertName() string {
	if n := w.CommonLabels["alertname"]; n != "" {
		return n
	}
	if n := w.GroupLabels["alertname"]; n != "" {
		return n
	}
	if len(w.Alerts) > 0 {
		return w.Alerts[0].Labels["alertname"]
	}
	return ""
}

// Severity returns the group's common severity label, or the most severe one
// found across its alerts when they differ.
func (w *Webhook) Severity() string {
	if s := w.CommonLabels["severity"]; s != "" {
		return s
	}
	best := ""
	for _, a := range w.Alerts {
		if severityRank(a.Labels["severity"]) > severityRank(best) {
			best = a.Labels["severity"]
		}
	}
	return best
}

func severityRank(s string) int {
	switch s {
	case "critical":
		return 3
	case "warning":
		return 2
	case "info":
		return 1
	default:
		return 0
	}
}

// Firing returns the alerts that are currently firing.
func (w *Webhook) Firing() []Alert { return w.byStatus(StatusFiring) }

// Resolved returns the alerts that have resolved.
func (w *Webhook) Resolved() []Alert { return w.byStatus(StatusResolved) }

func (w *Webhook) byStatus(status string) []Alert {
	var out []Alert
	for _, a := range w.Alerts {
		if a.Status == status {
			out = append(out, a)
		}
	}
	return out
}

// HostLabels are the labels checked, in order, for an alert's host.
var HostLabels = []string{"instance_name", "host", "hostname", "node", "instance"}

// Host returns the best host identifier for an alert and the label it came
// from, or two empty strings.
func (a Alert) Host() (label, value string) {
	for _, k := range HostLabels {
		if v := a.Labels[k]; v != "" {
			return k, v
		}
	}
	return "", ""
}
