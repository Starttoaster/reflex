package scrub

import "testing"

func TestPrivateIPv4(t *testing.T) {
	s, err := New([]string{PrivateIPv4}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		in, want string
	}{
		{"host 10.1.2.3 is down", "host [redacted] is down"},
		{"scrape 10.20.30.41:9100 failed", "scrape [redacted] failed"},
		{"subnet 192.168.1.0/24", "subnet [redacted]"},
		{"172.16.0.1 and 172.31.255.254", "[redacted] and [redacted]"},
		{"loopback 127.0.0.1", "loopback [redacted]"},
		{"172.32.0.1 is public", "172.32.0.1 is public"},
		{"8.8.8.8 is public", "8.8.8.8 is public"},
		{"110.0.0.1 is public", "110.0.0.1 is public"},
		{"version 1.10.2", "version 1.10.2"},
		{"no addresses", "no addresses"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := s.String(tt.in); got != tt.want {
				t.Errorf("String(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestCustomPatternsAndLabels(t *testing.T) {
	s, err := New([]string{PrivateIPv4, `\b[a-z0-9-]+\.internal\.example\b`}, []string{"instance", "secret_label"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := s.String("db.internal.example at 10.0.0.5"), "[redacted] at [redacted]"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
	got := s.Labels(map[string]string{
		"instance":     "10.0.0.5:9100",
		"secret_label": "x",
		"job":          "node",
		"note":         "peer 192.168.0.9",
	})
	want := map[string]string{"job": "node", "note": "peer [redacted]"}
	if len(got) != len(want) {
		t.Fatalf("Labels() = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("Labels()[%q] = %q, want %q", k, got[k], v)
		}
	}
	if !s.Drops("instance") || s.Drops("job") || s.Drops("") {
		t.Error("Drops() reports the wrong labels")
	}
}

func TestNilScrubberIsNoop(t *testing.T) {
	var s *Scrubber
	if got := s.String("10.0.0.1"); got != "10.0.0.1" {
		t.Errorf("String() = %q", got)
	}
	if got := s.Labels(map[string]string{"a": "b"}); got["a"] != "b" {
		t.Errorf("Labels() = %v", got)
	}
}

func TestInvalidPattern(t *testing.T) {
	if _, err := New([]string{"("}, nil); err == nil {
		t.Fatal("New() with an invalid regex returned no error")
	}
}
