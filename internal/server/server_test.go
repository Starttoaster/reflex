package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/starttoaster/reflex/internal/discord"
	"github.com/starttoaster/reflex/internal/metrics"
	"github.com/starttoaster/reflex/internal/router"
)

type sent struct {
	url string
	msg discord.Message
}

type fakeSender struct {
	mu   sync.Mutex
	sent []sent
	err  error
}

func (f *fakeSender) Send(_ context.Context, url string, msg discord.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, sent{url, msg})
	return f.err
}

func newTestServer(t *testing.T, sender *fakeSender, logs io.Writer) *httptest.Server {
	t.Helper()
	s := &Server{
		// No Jev: label routing only, which keeps these tests about HTTP.
		Router:      &router.Router{Config: router.Config{Timeout: time.Second}, Sticky: router.NewSticky(time.Hour)},
		Sender:      sender,
		Webhooks:    map[router.Channel]string{router.Urgent: "urgent-hook", router.Info: "info-hook"},
		Metrics:     metrics.New(),
		Log:         slog.New(slog.NewJSONHandler(logs, nil)),
		SendTimeout: time.Second,
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

// do sends a request and returns the status code and body.
func do(t *testing.T, method, url string, body io.Reader) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func post(t *testing.T, srv *httptest.Server, file string) int {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "testdata", file))
	if err != nil {
		t.Fatal(err)
	}
	code, _ := do(t, http.MethodPost, srv.URL+"/alerts", bytes.NewReader(body))
	return code
}

func get(t *testing.T, url string) string {
	t.Helper()
	_, body := do(t, http.MethodGet, url, nil)
	return body
}
func TestAlertsRouting(t *testing.T) {
	sender := &fakeSender{}
	var logs bytes.Buffer
	srv := newTestServer(t, sender, &logs)

	for _, tt := range []struct{ file, wantURL string }{
		{"firing-critical.json", "urgent-hook"},
		{"firing-warning.json", "info-hook"},
		{"resolved-critical.json", "urgent-hook"},
	} {
		if code := post(t, srv, tt.file); code != http.StatusOK {
			t.Fatalf("%s: status %d", tt.file, code)
		}
		last := sender.sent[len(sender.sent)-1]
		if last.url != tt.wantURL {
			t.Errorf("%s: sent to %q, want %q", tt.file, last.url, tt.wantURL)
		}
	}
	if f := sender.sent[0].msg.Embeds[0].Footer; f == nil || f.Text != "fallback (Jev disabled): severity=critical" {
		t.Errorf("footer = %+v", f)
	}
	if f := sender.sent[2].msg.Embeds[0].Footer; f == nil || f.Text != "resolved: same channel as firing" {
		t.Errorf("resolved footer = %+v", f)
	}
	if !strings.Contains(logs.String(), `"reason":"fallback_no_key"`) || !strings.Contains(logs.String(), `"label_channel":"urgent"`) {
		t.Errorf("decision not logged:\n%s", logs.String())
	}

	metricsBody := get(t, srv.URL+"/metrics")
	for _, want := range []string{
		`reflex_routing_decisions_total{channel="urgent",reason="fallback_no_key"} 1`,
		`reflex_routing_decisions_total{channel="info",reason="fallback_no_key"} 1`,
		`reflex_routing_decisions_total{channel="urgent",reason="sticky"} 1`,
		`reflex_notifications_received_total{status="firing"} 2`,
		`reflex_alerts_received_total{status="resolved"} 2`,
	} {
		if !strings.Contains(metricsBody, want) {
			t.Errorf("metrics missing %q", want)
		}
	}
}

func TestBadPayload(t *testing.T) {
	sender := &fakeSender{}
	srv := newTestServer(t, sender, io.Discard)
	if code, _ := do(t, http.MethodPost, srv.URL+"/alerts", strings.NewReader(`{"version":"4"}`)); code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", code)
	}
	if len(sender.sent) != 0 {
		t.Error("sent a message for a bad payload")
	}
	if !strings.Contains(get(t, srv.URL+"/metrics"), "reflex_invalid_payloads_total 1") {
		t.Error("invalid payload not counted")
	}
}

func TestDiscordFailureAsksForRetry(t *testing.T) {
	sender := &fakeSender{err: errors.New("discord down")}
	srv := newTestServer(t, sender, io.Discard)
	if code := post(t, srv, "firing-warning.json"); code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 so Alertmanager retries", code)
	}
	if !strings.Contains(get(t, srv.URL+"/metrics"), `reflex_discord_send_failures_total{channel="info"} 1`) {
		t.Error("send failure not counted")
	}
}

func TestHealthzAndMethods(t *testing.T) {
	srv := newTestServer(t, &fakeSender{}, io.Discard)
	if body := get(t, srv.URL+"/healthz"); body != "ok\n" {
		t.Errorf("healthz = %q", body)
	}
	if code, _ := do(t, http.MethodGet, srv.URL+"/alerts", nil); code != http.StatusMethodNotAllowed {
		t.Errorf("GET /alerts = %d, want 405", code)
	}
}
