package router

import (
	"sort"
	"strings"
	"time"

	"github.com/starttoaster/reflex/internal/alertmanager"
	"github.com/starttoaster/reflex/internal/scrub"
)

// maxStateAlerts caps how many alerts are described individually, to keep
// the request small for large groups.
const maxStateAlerts = 20

// State is the structured description of an alert group sent to Jev.
type State struct {
	Status        string            `json:"status"`
	AlertName     string            `json:"alertname"`
	Severity      string            `json:"severity,omitempty"`
	GroupLabels   map[string]string `json:"group_labels,omitempty"`
	FiringCount   int               `json:"firing_count"`
	ResolvedCount int               `json:"resolved_count"`
	AffectedHosts []string          `json:"affected_hosts,omitempty"`
	LongestFiring string            `json:"longest_firing_for,omitempty"`
	Alerts        []AlertState      `json:"alerts"`
	OmittedAlerts int               `json:"omitted_alerts,omitempty"`
}

// AlertState describes one alert in the group.
type AlertState struct {
	Status      string `json:"status"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Host        string `json:"host,omitempty"`
	Severity    string `json:"severity,omitempty"`
	FiringFor   string `json:"firing_for,omitempty"`
}

// BuildState summarises a webhook for Jev, scrubbing every free-text value.
func BuildState(w *alertmanager.Webhook, s *scrub.Scrubber, now time.Time) State {
	st := State{
		Status:        w.Status,
		AlertName:     s.String(w.AlertName()),
		Severity:      w.Severity(),
		FiringCount:   len(w.Firing()),
		ResolvedCount: len(w.Resolved()),
	}
	if gl := s.Labels(w.GroupLabels); len(gl) > 0 {
		st.GroupLabels = gl
	}

	hosts := map[string]bool{}
	var longest time.Duration
	for i, a := range w.Alerts {
		hostKey, host := a.Host()
		if s.Drops(hostKey) {
			host = ""
		}
		host = s.String(host)
		if host != "" {
			hosts[host] = true
		}
		var firingFor time.Duration
		if a.Status == alertmanager.StatusFiring && !a.StartsAt.IsZero() {
			firingFor = now.Sub(a.StartsAt)
			longest = max(longest, firingFor)
		}
		if i >= maxStateAlerts {
			st.OmittedAlerts++
			continue
		}
		as := AlertState{
			Status:      a.Status,
			Title:       s.String(a.Annotations["title"]),
			Description: s.String(firstNonEmpty(a.Annotations["description"], a.Annotations["summary"])),
			Host:        host,
			Severity:    a.Labels["severity"],
		}
		if firingFor > 0 {
			as.FiringFor = humanDuration(firingFor)
		}
		st.Alerts = append(st.Alerts, as)
	}
	for h := range hosts {
		st.AffectedHosts = append(st.AffectedHosts, h)
	}
	sort.Strings(st.AffectedHosts)
	if longest > 0 {
		st.LongestFiring = humanDuration(longest)
	}
	return st
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// humanDuration formats d to the second under a minute, otherwise to the
// minute, e.g. "45s", "12m", "2h15m".
func humanDuration(d time.Duration) string {
	if d < time.Minute {
		return d.Round(time.Second).String()
	}
	return strings.TrimSuffix(d.Round(time.Minute).String(), "0s")
}
