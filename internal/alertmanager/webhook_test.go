package alertmanager

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func parseFile(t *testing.T, name string) *Webhook {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "..", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	w, err := Parse(f)
	if err != nil {
		t.Fatalf("Parse(%s): %v", name, err)
	}
	return w
}

func TestParseSamples(t *testing.T) {
	tests := []struct {
		file         string
		status       string
		alertName    string
		severity     string
		firing       int
		resolved     int
		firstHostKey string
		firstHost    string
	}{
		{"firing-critical.json", StatusFiring, "HostDown", "critical", 2, 0, "instance_name", "web-01"},
		{"firing-warning.json", StatusFiring, "DiskSpaceLow", "warning", 1, 0, "instance_name", "nas-01"},
		{"resolved-critical.json", StatusResolved, "HostDown", "critical", 0, 2, "instance_name", "web-01"},
		// No common alertname or severity: fall back to the first alert and the worst severity.
		{"mixed.json", StatusFiring, "BackupFailed", "warning", 1, 1, "host", "db-01"},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			w := parseFile(t, tt.file)
			if w.Status != tt.status {
				t.Errorf("Status = %q, want %q", w.Status, tt.status)
			}
			if got := w.AlertName(); got != tt.alertName {
				t.Errorf("AlertName() = %q, want %q", got, tt.alertName)
			}
			if got := w.Severity(); got != tt.severity {
				t.Errorf("Severity() = %q, want %q", got, tt.severity)
			}
			if got := len(w.Firing()); got != tt.firing {
				t.Errorf("len(Firing()) = %d, want %d", got, tt.firing)
			}
			if got := len(w.Resolved()); got != tt.resolved {
				t.Errorf("len(Resolved()) = %d, want %d", got, tt.resolved)
			}
			if k, v := w.Alerts[0].Host(); k != tt.firstHostKey || v != tt.firstHost {
				t.Errorf("Host() = %q, %q, want %q, %q", k, v, tt.firstHostKey, tt.firstHost)
			}
			if w.GroupKey == "" {
				t.Error("GroupKey is empty")
			}
		})
	}
}

func TestParseTimes(t *testing.T) {
	w := parseFile(t, "resolved-critical.json")
	want := time.Date(2030, 1, 15, 10, 5, 0, 0, time.UTC)
	if !w.Alerts[0].EndsAt.Equal(want) {
		t.Errorf("EndsAt = %v, want %v", w.Alerts[0].EndsAt, want)
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{"not json", `{`, "decode webhook"},
		{"wrong version", `{"version":"3","status":"firing","alerts":[{}]}`, "unsupported webhook version"},
		{"unknown status", `{"version":"4","status":"pending","alerts":[{}]}`, "unknown group status"},
		{"no alerts", `{"version":"4","status":"firing","alerts":[]}`, "no alerts"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(tt.body))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Parse() error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestSeverity(t *testing.T) {
	alerts := func(sevs ...string) []Alert {
		var out []Alert
		for _, s := range sevs {
			out = append(out, Alert{Labels: map[string]string{"severity": s}})
		}
		return out
	}
	tests := []struct {
		name   string
		common string
		alerts []Alert
		want   string
	}{
		{"common label wins", "info", alerts("critical"), "info"},
		{"worst of mixed", "", alerts("info", "critical", "warning"), "critical"},
		{"unknown values ignored", "", alerts("page", "warning"), "warning"},
		{"none", "", alerts(""), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := &Webhook{CommonLabels: map[string]string{"severity": tt.common}, Alerts: tt.alerts}
			if got := w.Severity(); got != tt.want {
				t.Errorf("Severity() = %q, want %q", got, tt.want)
			}
		})
	}
}
