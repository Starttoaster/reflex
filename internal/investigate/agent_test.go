package investigate

import (
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

func TestAgentInvestigate(t *testing.T) {
	f := &fakeMessages{responses: []*anthropic.BetaMessage{
		response(t, "tool_use", toolUse("t1", "query_prometheus", `{"expr":"node_filesystem_avail_bytes"}`)),
		response(t, "end_turn", text("/tank is filling from nightly backups.")),
	}}
	limits := testLimits
	limits.ToolResultMaxBytes = 1 << 10
	a := &Agent{
		Loop:    &Loop{Messages: f},
		Grafana: fakeGrafana(t),
		Limits:  limits,
		Context: "Single-node homelab.",
		Now:     func() time.Time { return time.Date(2030, 1, 15, 9, 0, 0, 0, time.UTC) },
	}
	run, err := a.Investigate(t.Context(), hook("g1"))
	if err != nil {
		t.Fatal(err)
	}
	if run.Summary != "/tank is filling from nightly backups." || run.ToolCalls != 1 {
		t.Errorf("run = %+v", run)
	}
	p := f.calls[0]
	if len(p.Tools) != 2 {
		t.Errorf("offered %d tools, want the 2 allowed and not denied", len(p.Tools))
	}
	if !strings.Contains(p.System[0].Text, "Single-node homelab.") {
		t.Error("operator context missing from the system prompt")
	}
	results := lastToolResults(t, f.calls[1])
	if len(results) != 1 || results[0].IsError.Value || !strings.Contains(results[0].Content[0].OfText.Text, "node_filesystem_avail_bytes") {
		t.Errorf("tool result = %+v", results)
	}
}
