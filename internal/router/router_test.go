package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/starttoaster/reflex/internal/alertmanager"
	"github.com/starttoaster/reflex/internal/jev"
)

var testConfig = Config{Threshold: 0.8, MinConfidence: 0.6, Timeout: 200 * time.Millisecond}

// fakeJev returns a canned response or error.
type fakeJev struct {
	resp  *jev.Response
	err   error
	calls int
	last  jev.Request
}

func (f *fakeJev) Evaluate(_ context.Context, req jev.Request) (*jev.Response, error) {
	f.calls++
	f.last = req
	return f.resp, f.err
}

func routeAnswer(pPage, confidence float64) *jev.Response {
	choice := optionPageNow
	if pPage < 0.5 {
		choice = optionInfo
	}
	return &jev.Response{
		Model: "jev-test",
		Answers: map[string]jev.Answer{
			routeQuestion: {
				Type:          "choice",
				Choice:        choice,
				Probabilities: map[string]float64{optionPageNow: pPage, optionInfo: 1 - pPage},
				Confidence:    confidence,
			},
			urgencyQuestion: {Type: "score", Score: 3.2, Confidence: 0.7},
		},
	}
}

func newTestRouter(ev Evaluator) *Router {
	return &Router{Config: testConfig, Jev: ev, Sticky: NewSticky(time.Hour), Now: func() time.Time { return testNow }}
}

func TestDecisionThresholds(t *testing.T) {
	tests := []struct {
		name        string
		file        string
		pPage, conf float64
		wantChannel Channel
		wantReason  Reason
	}{
		{"confident page", "firing-warning.json", 0.93, 0.86, Urgent, ReasonJev},
		{"exactly at threshold", "firing-warning.json", 0.8, 0.6, Urgent, ReasonJev},
		{"confident informational overrides critical", "firing-critical.json", 0.05, 0.9, Info, ReasonJev},
		{"confident but below threshold", "firing-critical.json", 0.75, 0.7, Info, ReasonJev},
		{"low confidence falls back to severity critical", "firing-critical.json", 0.7, 0.4, Urgent, ReasonFallbackLowConfidence},
		{"low confidence falls back to severity warning", "firing-warning.json", 0.6, 0.2, Info, ReasonFallbackLowConfidence},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newTestRouter(&fakeJev{resp: routeAnswer(tt.pPage, tt.conf)})
			d := r.Route(context.Background(), loadWebhook(t, tt.file))
			if d.Channel != tt.wantChannel || d.Reason != tt.wantReason {
				t.Errorf("got %s/%s, want %s/%s", d.Channel, d.Reason, tt.wantChannel, tt.wantReason)
			}
			if d.PageProbability != tt.pPage || d.Confidence != tt.conf {
				t.Errorf("p=%v conf=%v not recorded", d.PageProbability, d.Confidence)
			}
			if d.Urgency == nil || *d.Urgency != 3.2 {
				t.Errorf("Urgency = %v, want 3.2", d.Urgency)
			}
			if !d.JevCalled || d.Model != "jev-test" {
				t.Errorf("JevCalled=%v Model=%q", d.JevCalled, d.Model)
			}
		})
	}
}

func TestRequestShape(t *testing.T) {
	f := &fakeJev{resp: routeAnswer(0.9, 0.9)}
	r := newTestRouter(f)
	r.Config.Model = "jev-pinned"
	r.Route(context.Background(), loadWebhook(t, "firing-warning.json"))

	if f.last.Model != "jev-pinned" {
		t.Errorf("Model = %q", f.last.Model)
	}
	q, ok := f.last.Questions[routeQuestion]
	if !ok || q.Type != "choice" {
		t.Fatalf("route question = %+v", q)
	}
	opts := q.Criteria.(map[string]string)
	if _, ok := opts[optionPageNow]; !ok {
		t.Errorf("options %v lack %q", opts, optionPageNow)
	}
	if _, ok := opts[optionInfo]; !ok {
		t.Errorf("options %v lack %q", opts, optionInfo)
	}
	if _, ok := f.last.Questions[urgencyQuestion]; !ok {
		t.Error("no urgency question")
	}
	if st, ok := f.last.State.(State); !ok || st.AlertName != "DiskSpaceLow" {
		t.Errorf("State = %#v", f.last.State)
	}
}

// fakeJevServer stands in for the TypeSafe API over real HTTP.
func fakeJevServer(t *testing.T, h http.HandlerFunc) *jev.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &jev.Client{Endpoint: srv.URL, APIKey: "test-key", HTTPClient: srv.Client()}
}

func TestFallbackPaths(t *testing.T) {
	writeJSON := func(v any) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(v) }
	}
	tests := []struct {
		name    string
		handler http.HandlerFunc
		client  func(*jev.Client) // optional tweak
		reason  Reason
	}{
		{
			name: "slow response times out",
			handler: func(w http.ResponseWriter, r *http.Request) {
				select {
				case <-time.After(500 * time.Millisecond): // well past testConfig.Timeout
				case <-r.Context().Done():
				}
				_ = json.NewEncoder(w).Encode(routeAnswer(0.01, 1))
			},
			reason: ReasonFallbackTimeout,
		},
		{
			name: "server error",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, `{"detail":"boom"}`, http.StatusInternalServerError)
			},
			reason: ReasonFallbackError,
		},
		{
			name: "bad api key",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, `{"detail":"invalid key"}`, http.StatusUnauthorized)
			},
			reason: ReasonFallbackError,
		},
		{
			name: "overloaded",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, `{"detail":"overloaded"}`, 529)
			},
			reason: ReasonFallbackError,
		},
		{
			name:    "malformed body",
			handler: func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"answers":`)) },
			reason:  ReasonFallbackError,
		},
		{
			name:    "missing route answer",
			handler: writeJSON(jev.Response{Model: "jev-test", Answers: map[string]jev.Answer{}}),
			reason:  ReasonFallbackError,
		},
		{
			name: "missing page-now probability",
			handler: writeJSON(jev.Response{Model: "jev-test", Answers: map[string]jev.Answer{
				routeQuestion: {Type: "choice", Choice: "x", Probabilities: map[string]float64{"x": 1}, Confidence: 1},
			}}),
			reason: ReasonFallbackError,
		},
		{
			name:    "low confidence",
			handler: writeJSON(routeAnswer(0.55, 0.1)),
			reason:  ReasonFallbackLowConfidence,
		},
		{
			name:    "connection refused",
			handler: func(http.ResponseWriter, *http.Request) {},
			client:  func(c *jev.Client) { c.Endpoint = "http://127.0.0.1:1" },
			reason:  ReasonFallbackError,
		},
		{
			name:    "empty api key",
			handler: func(http.ResponseWriter, *http.Request) { t.Error("request sent without a key") },
			client:  func(c *jev.Client) { c.APIKey = "" },
			reason:  ReasonFallbackNoKey,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fakeJevServer(t, tt.handler)
			if tt.client != nil {
				tt.client(c)
			}
			for file, want := range map[string]Channel{"firing-critical.json": Urgent, "firing-warning.json": Info} {
				start := time.Now()
				d := newTestRouter(c).Route(context.Background(), loadWebhook(t, file))
				if d.Reason != tt.reason {
					t.Errorf("%s: reason = %s, want %s (err %v)", file, d.Reason, tt.reason, d.Err)
				}
				if d.Channel != want {
					t.Errorf("%s: channel = %s, want label-routed %s", file, d.Channel, want)
				}
				if elapsed := time.Since(start); elapsed > time.Second {
					t.Errorf("%s: routing took %v, want under the timeout plus slack", file, elapsed)
				}
				if !strings.HasPrefix(d.Footer(), "fallback (") {
					t.Errorf("%s: footer = %q", file, d.Footer())
				}
			}
		})
	}
}

func TestNoJevConfigured(t *testing.T) {
	r := newTestRouter(nil)
	d := r.Route(context.Background(), loadWebhook(t, "firing-critical.json"))
	if d.Channel != Urgent || d.Reason != ReasonFallbackNoKey || d.JevCalled {
		t.Errorf("got %+v", d)
	}
}

func TestResolvedFollowsFiring(t *testing.T) {
	f := &fakeJev{resp: routeAnswer(0.02, 0.95)}
	r := newTestRouter(f)

	// Jev confidently says informational despite severity=critical.
	firing := r.Route(context.Background(), loadWebhook(t, "firing-critical.json"))
	if firing.Channel != Info {
		t.Fatalf("firing channel = %s, want info", firing.Channel)
	}

	// The resolved notification goes to the same channel, without asking Jev.
	f.err = errors.New("should not be called")
	resolved := r.Route(context.Background(), loadWebhook(t, "resolved-critical.json"))
	if resolved.Channel != Info || resolved.Reason != ReasonSticky {
		t.Errorf("resolved = %s/%s, want info/sticky", resolved.Channel, resolved.Reason)
	}
	if f.calls != 1 {
		t.Errorf("Jev called %d times, want 1", f.calls)
	}
	if resolved.Footer() != "resolved: same channel as firing" {
		t.Errorf("footer = %q", resolved.Footer())
	}

	// The entry is forgotten once used, so a second resolved uses the label rule.
	again := r.Route(context.Background(), loadWebhook(t, "resolved-critical.json"))
	if again.Channel != Urgent || again.Reason != ReasonFallbackUnknownGroup {
		t.Errorf("second resolved = %s/%s, want urgent/fallback_unknown_group", again.Channel, again.Reason)
	}
}

func TestUrgentGroupStaysUrgent(t *testing.T) {
	f := &fakeJev{resp: routeAnswer(0.95, 0.9)}
	r := newTestRouter(f)

	first := r.Route(context.Background(), loadWebhook(t, "firing-critical.json"))
	if first.Channel != Urgent || first.Reason != ReasonJev {
		t.Fatalf("first = %s/%s, want urgent/jev", first.Channel, first.Reason)
	}

	// Jev would now say informational, but the group has already paged.
	f.resp = routeAnswer(0.05, 0.9)
	update := r.Route(context.Background(), loadWebhook(t, "firing-critical.json"))
	if update.Channel != Urgent || update.Reason != ReasonEscalated || update.JevCalled {
		t.Errorf("update = %s/%s called=%v, want urgent/escalated without Jev", update.Channel, update.Reason, update.JevCalled)
	}
	if f.calls != 1 {
		t.Errorf("Jev called %d times, want 1", f.calls)
	}
	if update.Footer() != "still urgent: escalated earlier" {
		t.Errorf("footer = %q", update.Footer())
	}

	resolved := r.Route(context.Background(), loadWebhook(t, "resolved-critical.json"))
	if resolved.Channel != Urgent || resolved.Reason != ReasonSticky {
		t.Errorf("resolved = %s/%s, want urgent/sticky", resolved.Channel, resolved.Reason)
	}
}

func TestInfoGroupCanEscalate(t *testing.T) {
	f := &fakeJev{resp: routeAnswer(0.05, 0.9)}
	r := newTestRouter(f)

	first := r.Route(context.Background(), loadWebhook(t, "firing-warning.json"))
	if first.Channel != Info {
		t.Fatalf("first channel = %s, want info", first.Channel)
	}

	// An info group is re-asked on every firing notification.
	f.resp = routeAnswer(0.95, 0.9)
	update := r.Route(context.Background(), loadWebhook(t, "firing-warning.json"))
	if update.Channel != Urgent || update.Reason != ReasonJev {
		t.Errorf("update = %s/%s, want urgent/jev", update.Channel, update.Reason)
	}
	if f.calls != 2 {
		t.Errorf("Jev called %d times, want 2", f.calls)
	}
}

func TestResolvedUnknownGroup(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*alertmanager.Webhook)
		want   Channel
	}{
		{"critical", func(*alertmanager.Webhook) {}, Urgent},
		{"warning", func(w *alertmanager.Webhook) { w.CommonLabels["severity"] = "warning" }, Info},
		{"no group key", func(w *alertmanager.Webhook) { w.GroupKey = "" }, Urgent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := loadWebhook(t, "resolved-critical.json")
			tt.mutate(w)
			d := newTestRouter(&fakeJev{err: errors.New("unused")}).Route(context.Background(), w)
			if d.Channel != tt.want || d.Reason != ReasonFallbackUnknownGroup || d.JevCalled {
				t.Errorf("got %s/%s called=%v, want %s/fallback_unknown_group", d.Channel, d.Reason, d.JevCalled, tt.want)
			}
		})
	}
}

func TestClassifyDoesNotTouchSticky(t *testing.T) {
	r := newTestRouter(&fakeJev{resp: routeAnswer(0.9, 0.9)})
	r.Classify(context.Background(), loadWebhook(t, "firing-critical.json"))
	if n := r.Sticky.Len(); n != 0 {
		t.Errorf("sticky has %d entries after Classify", n)
	}
}

func TestFooter(t *testing.T) {
	tests := []struct {
		d    Decision
		want string
	}{
		{Decision{Reason: ReasonJev, PageProbability: 0.934}, "routed by Jev (p=0.93)"},
		{Decision{Reason: ReasonFallbackTimeout, Severity: "critical"}, "fallback (Jev timeout): severity=critical"},
		{Decision{Reason: ReasonFallbackNoKey}, "fallback (Jev disabled): severity=none"},
		{Decision{Reason: ReasonFallbackLowConfidence, Severity: "warning"}, "fallback (low confidence): severity=warning"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprint(tt.d.Reason), func(t *testing.T) {
			if got := tt.d.Footer(); got != tt.want {
				t.Errorf("Footer() = %q, want %q", got, tt.want)
			}
		})
	}
}
