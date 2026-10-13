package discord

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/starttoaster/reflex/internal/alertmanager"
)

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

func TestFormat(t *testing.T) {
	tests := []struct {
		file      string
		title     string
		desc      string
		color     int
		footer    string
		hasFooter bool
	}{
		{
			file:  "firing-critical.json",
			title: "[FIRING:2] HostDown",
			desc: "🔴 **web-01 is down**\nnode-exporter on web-01 (10.20.30.41) has not responded to scrapes for 5 minutes.\n\n" +
				"🔴 **web-02 is down**\nnode-exporter on web-02 (10.20.30.42) has not responded to scrapes for 5 minutes.",
			color:     colorFiring,
			footer:    "routed by Jev (p=0.93)",
			hasFooter: true,
		},
		{
			file:  "resolved-critical.json",
			title: "[RESOLVED] HostDown",
			desc:  "✅ ~~web-01 is down~~ (resolved)\n✅ ~~web-02 is down~~ (resolved)",
			color: colorResolved,
		},
		{
			// Firing alerts first, then resolved ones by title only.
			file:  "mixed.json",
			title: "[FIRING:1] BackupFailed",
			desc: "🔴 **Nightly backup of db-01 failed**\nThe backup job exited with status 1.\nLast success was 2 days ago.\n\n" +
				"✅ ~~BackupSlow on db-02~~ (resolved)",
			color:     colorFiring,
			footer:    "fallback (Jev timeout): severity=warning",
			hasFooter: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			msg := Format(loadWebhook(t, tt.file), tt.footer)
			if len(msg.Embeds) != 1 {
				t.Fatalf("got %d embeds", len(msg.Embeds))
			}
			e := msg.Embeds[0]
			if e.Title != tt.title {
				t.Errorf("Title = %q, want %q", e.Title, tt.title)
			}
			if e.Description != tt.desc {
				t.Errorf("Description =\n%s\nwant\n%s", e.Description, tt.desc)
			}
			if e.Color != tt.color {
				t.Errorf("Color = %#x, want %#x", e.Color, tt.color)
			}
			if (e.Footer != nil) != tt.hasFooter || (e.Footer != nil && e.Footer.Text != tt.footer) {
				t.Errorf("Footer = %+v, want %q", e.Footer, tt.footer)
			}
		})
	}
}

func TestFormatDisablesMentions(t *testing.T) {
	w := loadWebhook(t, "firing-warning.json")
	w.Alerts[0].Annotations["description"] = "@everyone disk full"
	b, err := json.Marshal(Format(w, ""))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"allowed_mentions":{"parse":[]}`) {
		t.Errorf("message does not disable mentions: %s", b)
	}
}

func TestTruncation(t *testing.T) {
	long := strings.Repeat("é", 5000)

	t.Run("title", func(t *testing.T) {
		w := loadWebhook(t, "firing-warning.json")
		w.CommonLabels["alertname"] = long
		title := Format(w, "").Embeds[0].Title
		if n := utf8.RuneCountInString(title); n != MaxTitle {
			t.Errorf("title has %d characters, want %d", n, MaxTitle)
		}
		if !strings.HasSuffix(title, "…") {
			t.Errorf("title not marked as truncated: %q", title[len(title)-10:])
		}
	})

	t.Run("one huge alert", func(t *testing.T) {
		w := loadWebhook(t, "firing-warning.json")
		w.Alerts[0].Annotations["description"] = long
		desc := Format(w, "").Embeds[0].Description
		if n := utf8.RuneCountInString(desc); n > MaxDescription {
			t.Errorf("description has %d characters, limit %d", n, MaxDescription)
		}
		if !strings.HasPrefix(desc, "🔴 **nas-01 disk /tank is 85% full**") || !strings.HasSuffix(desc, "…") {
			t.Errorf("unexpected truncation: starts %q, ends %q", desc[:40], desc[len(desc)-10:])
		}
	})

	t.Run("many alerts", func(t *testing.T) {
		w := loadWebhook(t, "firing-warning.json")
		base := w.Alerts[0]
		w.Alerts = nil
		for range 100 {
			a := base
			a.Annotations = map[string]string{"title": "disk full", "description": strings.Repeat("x", 100)}
			w.Alerts = append(w.Alerts, a)
		}
		desc := Format(w, "").Embeds[0].Description
		if n := utf8.RuneCountInString(desc); n > MaxDescription {
			t.Errorf("description has %d characters, limit %d", n, MaxDescription)
		}
		shown := strings.Count(desc, "🔴 **disk full**")
		if !strings.HasSuffix(desc, "…and "+itoa(100-shown)+" more") {
			t.Errorf("showed %d alerts but ending is %q", shown, desc[len(desc)-20:])
		}
		// No alert block is cut part-way.
		if strings.Count(desc, strings.Repeat("x", 100)) != shown {
			t.Error("an alert block was cut")
		}
	})
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestTruncate(t *testing.T) {
	tests := []struct {
		s     string
		limit int
		want  string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"hello world", 8, "hello w…"},
		{"hello world", 7, "hello…"},
		{"héllo", 3, "hé…"},
		{"abc", 0, ""},
	}
	for _, tt := range tests {
		if got := truncate(tt.s, tt.limit); got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.s, tt.limit, got, tt.want)
		}
	}
}

func TestSend(t *testing.T) {
	msg := Format(loadWebhook(t, "firing-warning.json"), "x")

	t.Run("ok", func(t *testing.T) {
		var got Message
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Content-Type") != "application/json" {
				t.Errorf("Content-Type = %q", r.Header.Get("Content-Type"))
			}
			_ = json.NewDecoder(r.Body).Decode(&got)
			w.WriteHeader(http.StatusNoContent)
		}))
		defer srv.Close()
		if err := (&Client{}).Send(context.Background(), srv.URL, msg); err != nil {
			t.Fatal(err)
		}
		if got.Embeds[0].Title != msg.Embeds[0].Title {
			t.Errorf("server got %+v", got)
		}
	})

	t.Run("error hides webhook url", func(t *testing.T) {
		err := (&Client{}).Send(context.Background(), "http://127.0.0.1:1/api/webhooks/123/secret-token", msg)
		if err == nil || strings.Contains(err.Error(), "secret-token") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("http error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, `{"message":"Invalid Webhook Token"}`, http.StatusUnauthorized)
		}))
		defer srv.Close()
		err := (&Client{}).Send(context.Background(), srv.URL, msg)
		if err == nil || !strings.Contains(err.Error(), "401") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("rate limited then ok", func(t *testing.T) {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if calls.Add(1) == 1 {
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"message":"rate limited","retry_after":0.05,"global":false}`))
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}))
		defer srv.Close()
		if err := (&Client{}).Send(context.Background(), srv.URL, msg); err != nil {
			t.Fatal(err)
		}
		if calls.Load() != 2 {
			t.Errorf("calls = %d, want 2", calls.Load())
		}
	})

	t.Run("rate limited twice", func(t *testing.T) {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"retry_after":30}`))
		}))
		defer srv.Close()
		c := &Client{MaxRetryAfter: 10 * time.Millisecond}
		if err := c.Send(context.Background(), srv.URL, msg); err == nil {
			t.Fatal("expected an error")
		}
		if calls.Load() != 2 {
			t.Errorf("calls = %d, want 2 (one retry)", calls.Load())
		}
	})
}
