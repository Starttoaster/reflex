package router

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/starttoaster/reflex/internal/alertmanager"
	"github.com/starttoaster/reflex/internal/scrub"
)

var testNow = time.Date(2030, 1, 15, 10, 0, 0, 0, time.UTC)

func loadWebhook(t *testing.T, name string) *alertmanager.Webhook {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "..", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	w, err := alertmanager.Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func ipScrubber(t *testing.T, dropLabels ...string) *scrub.Scrubber {
	t.Helper()
	s, err := scrub.New([]string{scrub.PrivateIPv4}, dropLabels)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestBuildState(t *testing.T) {
	st := BuildState(loadWebhook(t, "firing-critical.json"), ipScrubber(t), testNow)

	if st.Status != "firing" || st.AlertName != "HostDown" || st.Severity != "critical" {
		t.Errorf("header = %+v", st)
	}
	if st.FiringCount != 2 || st.ResolvedCount != 0 {
		t.Errorf("counts = %d firing, %d resolved", st.FiringCount, st.ResolvedCount)
	}
	if got := strings.Join(st.AffectedHosts, ","); got != "web-01,web-02" {
		t.Errorf("AffectedHosts = %q", got)
	}
	if st.LongestFiring != "30m" {
		t.Errorf("LongestFiring = %q, want 30m", st.LongestFiring)
	}
	if got := st.Alerts[1].FiringFor; got != "15m" {
		t.Errorf("Alerts[1].FiringFor = %q, want 15m", got)
	}
	if st.Alerts[0].Title != "web-01 is down" {
		t.Errorf("Alerts[0].Title = %q", st.Alerts[0].Title)
	}
	if st.GroupLabels["job"] != "node" {
		t.Errorf("GroupLabels = %v", st.GroupLabels)
	}

	b, _ := json.Marshal(st)
	if strings.Contains(string(b), "10.20.30") {
		t.Errorf("state leaks a private IP: %s", b)
	}
	if !strings.Contains(string(b), scrub.Redacted) {
		t.Errorf("state has no redactions: %s", b)
	}
}

func TestBuildStateDropsHostLabel(t *testing.T) {
	st := BuildState(loadWebhook(t, "firing-critical.json"), ipScrubber(t, "instance_name"), testNow)
	if len(st.AffectedHosts) != 0 || st.Alerts[0].Host != "" {
		t.Errorf("dropped host label still present: hosts=%v host=%q", st.AffectedHosts, st.Alerts[0].Host)
	}
}

func TestBuildStateMixed(t *testing.T) {
	st := BuildState(loadWebhook(t, "mixed.json"), nil, testNow)
	if st.FiringCount != 1 || st.ResolvedCount != 1 {
		t.Errorf("counts = %d firing, %d resolved", st.FiringCount, st.ResolvedCount)
	}
	if st.Alerts[1].FiringFor != "" {
		t.Errorf("resolved alert has FiringFor = %q", st.Alerts[1].FiringFor)
	}
	// summary is used when there's no description.
	if st.Alerts[1].Description != "Backup of db-02 took over an hour" {
		t.Errorf("Alerts[1].Description = %q", st.Alerts[1].Description)
	}
	if st.LongestFiring != "7h0m" {
		t.Errorf("LongestFiring = %q", st.LongestFiring)
	}
}

func TestBuildStateCapsAlerts(t *testing.T) {
	w := &alertmanager.Webhook{Status: "firing"}
	for i := range maxStateAlerts + 5 {
		w.Alerts = append(w.Alerts, alertmanager.Alert{
			Status: "firing",
			Labels: map[string]string{"alertname": "X", "instance_name": fmt.Sprintf("h%02d", i)},
		})
	}
	st := BuildState(w, nil, testNow)
	if len(st.Alerts) != maxStateAlerts || st.OmittedAlerts != 5 {
		t.Errorf("got %d alerts, %d omitted", len(st.Alerts), st.OmittedAlerts)
	}
	if len(st.AffectedHosts) != maxStateAlerts+5 {
		t.Errorf("AffectedHosts counts %d hosts, want all %d", len(st.AffectedHosts), maxStateAlerts+5)
	}
}

func TestHumanDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{42 * time.Second, "42s"},
		{12*time.Minute + 20*time.Second, "12m"},
		{2*time.Hour + 15*time.Minute, "2h15m"},
		{50 * time.Hour, "50h0m"},
	}
	for _, tt := range tests {
		if got := humanDuration(tt.d); got != tt.want {
			t.Errorf("humanDuration(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}
