package investigate

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// fakeMessages replays scripted responses and records what it was sent.
type fakeMessages struct {
	responses []*anthropic.BetaMessage
	calls     []anthropic.BetaMessageNewParams
}

func (f *fakeMessages) New(_ context.Context, params anthropic.BetaMessageNewParams, _ ...option.RequestOption) (*anthropic.BetaMessage, error) {
	f.calls = append(f.calls, params)
	if len(f.responses) == 0 {
		return nil, errors.New("more requests than scripted")
	}
	next := f.responses[0]
	f.responses = f.responses[1:]
	return next, nil
}

// response builds a message from wire JSON, as the SDK does: the union
// accessors read the raw JSON the decoder captured.
func response(t *testing.T, stopReason string, content ...any) *anthropic.BetaMessage {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"id": "msg_test", "type": "message", "role": "assistant", "model": "claude-test",
		"stop_reason": stopReason,
		"content":     content,
		"usage":       map[string]any{"input_tokens": 100, "output_tokens": 50},
	})
	if err != nil {
		t.Fatal(err)
	}
	var m anthropic.BetaMessage
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	return &m
}

func text(s string) any { return map[string]any{"type": "text", "text": s} }

func toolUse(id, name, args string) any {
	return map[string]any{"type": "tool_use", "id": id, "name": name, "input": json.RawMessage(args)}
}

var testLimits = Limits{Model: "claude-test", MaxIterations: 5, MaxTotalTokens: 100000, MaxOutputTokens: 1000, ToolResultMaxBytes: 20}

func testSet(t *testing.T, got *[]string) *Set {
	t.Helper()
	set := NewSet()
	err := set.Add(Func{
		Definition: Def{Name: "query_prometheus", Description: "PromQL", Schema: json.RawMessage(`{"type":"object","properties":{"expr":{"type":"string"}},"required":["expr"],"additionalProperties":false}`)},
		Run: func(_ context.Context, args json.RawMessage) (string, error) {
			*got = append(*got, string(args))
			return strings.Repeat("x", 50), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return set
}

// lastToolResult returns the tool_result blocks of the last request's final
// user message.
func lastToolResults(t *testing.T, p anthropic.BetaMessageNewParams) []anthropic.BetaToolResultBlockParam {
	t.Helper()
	msg := p.Messages[len(p.Messages)-1]
	var out []anthropic.BetaToolResultBlockParam
	for _, b := range msg.Content {
		if b.OfToolResult != nil {
			out = append(out, *b.OfToolResult)
		}
	}
	return out
}

func TestLoopToolRoundTrip(t *testing.T) {
	var calls []string
	f := &fakeMessages{responses: []*anthropic.BetaMessage{
		response(t, "tool_use", text("Checking."), toolUse("t1", "query_prometheus", `{"expr":"up"}`)),
		response(t, "end_turn", text("The NAS is fine.")),
	}}
	run, err := (&Loop{Messages: f}).Run(context.Background(), testLimits, testSet(t, &calls), "sys", "user")
	if err != nil {
		t.Fatal(err)
	}
	if run.Summary != "The NAS is fine." || run.ToolCalls != 1 || run.Iterations != 2 || run.Tokens != 300 {
		t.Errorf("run = %+v", run)
	}
	if len(calls) != 1 || calls[0] != `{"expr":"up"}` {
		t.Errorf("tool args = %q", calls)
	}
	results := lastToolResults(t, f.calls[1])
	if len(results) != 1 || results[0].ToolUseID != "t1" || results[0].IsError.Value {
		t.Fatalf("tool results = %+v", results)
	}
	got := results[0].Content[0].OfText.Text
	if !strings.HasPrefix(got, strings.Repeat("x", 20)+"\n\n[truncated: 20 of 50") {
		t.Errorf("tool result not truncated: %q", got)
	}

	p := f.calls[0]
	if p.Model != "claude-test" || p.Fallbacks.OfDefault != "default" || len(p.Betas) != 1 {
		t.Errorf("request params: model=%q fallbacks=%+v betas=%v", p.Model, p.Fallbacks, p.Betas)
	}
	schema := p.Tools[0].OfTool.InputSchema
	if schema.ExtraFields["additionalProperties"] != false || len(schema.Required) != 1 {
		t.Errorf("schema lost keywords: %+v", schema)
	}
}

func TestLoopUnknownTool(t *testing.T) {
	var calls []string
	f := &fakeMessages{responses: []*anthropic.BetaMessage{
		response(t, "tool_use", toolUse("t1", "delete_everything", `{}`)),
		response(t, "end_turn", text("done")),
	}}
	if _, err := (&Loop{Messages: f}).Run(context.Background(), testLimits, testSet(t, &calls), "sys", "user"); err != nil {
		t.Fatal(err)
	}
	results := lastToolResults(t, f.calls[1])
	if len(results) != 1 || !results[0].IsError.Value || !strings.Contains(results[0].Content[0].OfText.Text, "no tool called") {
		t.Errorf("tool results = %+v", results)
	}
	if len(calls) != 0 {
		t.Errorf("unknown tool executed something: %q", calls)
	}
}

func TestLoopBudget(t *testing.T) {
	var calls []string
	limits := testLimits
	limits.MaxIterations = 2
	f := &fakeMessages{responses: []*anthropic.BetaMessage{
		response(t, "tool_use", text("first"), toolUse("t1", "query_prometheus", `{"expr":"up"}`)),
		response(t, "tool_use", text("Partial finding."), toolUse("t2", "query_prometheus", `{"expr":"up"}`)),
	}}
	run, err := (&Loop{Messages: f}).Run(context.Background(), limits, testSet(t, &calls), "sys", "user")
	if err != nil {
		t.Fatal(err)
	}
	if run.Summary != "Partial finding." || len(run.Notes) != 1 || !strings.Contains(run.Notes[0], "2-request limit") {
		t.Errorf("run = %+v", run)
	}
}

func TestLoopPauseTurnResumes(t *testing.T) {
	var calls []string
	f := &fakeMessages{responses: []*anthropic.BetaMessage{
		response(t, "pause_turn", text("thinking")),
		response(t, "end_turn", text("answer")),
	}}
	run, err := (&Loop{Messages: f}).Run(context.Background(), testLimits, testSet(t, &calls), "sys", "user")
	if err != nil || run.Summary != "answer" || len(f.calls) != 2 {
		t.Errorf("run = %+v, err = %v, calls = %d", run, err, len(f.calls))
	}
}

func TestLoopRefusal(t *testing.T) {
	var calls []string
	f := &fakeMessages{responses: []*anthropic.BetaMessage{response(t, "refusal")}}
	if _, err := (&Loop{Messages: f}).Run(context.Background(), testLimits, testSet(t, &calls), "sys", "user"); err == nil {
		t.Error("refusal returned no error")
	}
}
