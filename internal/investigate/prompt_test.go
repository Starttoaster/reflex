package investigate

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/starttoaster/reflex/internal/alertmanager"
)

func TestUserPromptIsUnscrubbed(t *testing.T) {
	f, err := os.Open("../../testdata/firing-warning.json")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	w, err := alertmanager.Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	got := UserPrompt(w, time.Date(2030, 1, 15, 8, 42, 0, 0, time.UTC))
	for _, want := range []string{
		"alertname: DiskSpaceLow",
		`mountpoint="/tank"`,
		"192.168.77.10", // raw: Claude needs real values to build queries
		"started: 2030-01-15T08:00:00Z (42m ago)",
		"source: http://prometheus.example.com/graph?g0.expr=disk",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt lacks %q:\n%s", want, got)
		}
	}
}

func TestSystemPromptContext(t *testing.T) {
	if SystemPrompt("") != systemPrompt {
		t.Error("empty context changed the prompt")
	}
	if !strings.HasSuffix(SystemPrompt("NAS matters.\n"), "environment like this:\nNAS matters.") {
		t.Error("context not appended")
	}
}
