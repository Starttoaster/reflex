package investigate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
)

// Messages is the part of the Anthropic SDK the loop uses. Satisfied by
// &anthropic.Client{}.Beta.Messages.
type Messages interface {
	New(ctx context.Context, params anthropic.BetaMessageNewParams, opts ...option.RequestOption) (*anthropic.BetaMessage, error)
}

// Limits bound one investigation.
type Limits struct {
	Model string
	// Effort is low, medium, high, xhigh or max; empty uses the model default.
	Effort          string
	MaxIterations   int
	MaxTotalTokens  int64
	MaxOutputTokens int64
	// ToolResultMaxBytes caps each tool result shown to the model. Results
	// are re-sent on every later turn, so an unbounded Loki query would be
	// paid for many times over.
	ToolResultMaxBytes int
}

// Run is the outcome of one conversation.
type Run struct {
	Summary    string
	Model      string
	Iterations int
	ToolCalls  int
	Tokens     int64
	// Notes records anything that limited the run, such as a budget cap.
	Notes []string
}

// Loop runs a bounded tool-use conversation. The model only names tools;
// the loop looks them up in a Set and executes them.
type Loop struct {
	Messages Messages
	Log      *slog.Logger
}

// Run holds the conversation until the model answers or the budget runs out.
func (l *Loop) Run(ctx context.Context, limits Limits, set *Set, system, user string) (run Run, err error) {
	run.Model = limits.Model
	tools, err := toolParams(set.Defs())
	if err != nil {
		return run, err
	}
	params := anthropic.BetaMessageNewParams{
		Model:     limits.Model,
		MaxTokens: limits.MaxOutputTokens,
		System: []anthropic.BetaTextBlockParam{{
			Text:         system,
			CacheControl: anthropic.NewBetaCacheControlEphemeralParam(),
		}},
		Tools: tools,
		Messages: []anthropic.BetaMessageParam{
			anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(user)),
		},
		// Let the API re-serve a refused request on a suitable model rather
		// than ending the investigation.
		Betas:     []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01},
		Fallbacks: anthropic.BetaFallbacksParamUnion{OfDefault: "default"},
	}
	if limits.Effort != "" {
		params.OutputConfig = anthropic.BetaOutputConfigParam{Effort: anthropic.BetaOutputConfigEffort(limits.Effort)}
	}

	budget := &budget{maxIterations: limits.MaxIterations, maxTokens: limits.MaxTotalTokens}
	// Named results let this report the cost on every return path.
	defer func() {
		run.Iterations = budget.iterations
		run.Tokens = budget.tokens
	}()
	var last *anthropic.BetaMessage
	for {
		if err := ctx.Err(); err != nil {
			return run, err
		}
		if reason := budget.begin(); reason != "" {
			run.Notes = append(run.Notes, reason)
			break
		}
		msg, err := l.Messages.New(ctx, params)
		if err != nil {
			return run, fmt.Errorf("call the model: %w", err)
		}
		budget.record(msg.Usage)
		if msg.Model != "" {
			run.Model = msg.Model
		}
		last = msg
		// ToParam keeps thinking blocks, which must be sent back unchanged.
		params.Messages = append(params.Messages, msg.ToParam())

		switch msg.StopReason {
		case anthropic.BetaStopReasonEndTurn, anthropic.BetaStopReasonStopSequence:
			run.Summary = textOf(msg)
			return run, nil
		case anthropic.BetaStopReasonMaxTokens:
			run.Summary = textOf(msg)
			run.Notes = append(run.Notes, "answer cut short by the output token limit")
			return run, nil
		case anthropic.BetaStopReasonModelContextWindowExceeded:
			run.Summary = textOf(msg)
			run.Notes = append(run.Notes, "conversation outgrew the context window")
			return run, nil
		case anthropic.BetaStopReasonRefusal:
			return run, errors.New("the model declined to investigate")
		case anthropic.BetaStopReasonPauseTurn:
			// Re-sending the conversation resumes an interrupted turn.
			continue
		}

		results := l.runTools(ctx, set, msg, limits.ToolResultMaxBytes, &run)
		if len(results) == 0 {
			run.Summary = textOf(msg)
			return run, nil
		}
		params.Messages = append(params.Messages, anthropic.NewBetaUserMessage(results...))
	}

	// The budget ran out mid-conversation; whatever prose there is beats
	// nothing.
	if last != nil {
		run.Summary = textOf(last)
	}
	if strings.TrimSpace(run.Summary) == "" {
		return run, fmt.Errorf("stopped before reaching a conclusion: %s", strings.Join(run.Notes, "; "))
	}
	return run, nil
}

// runTools executes every tool call in one assistant turn. Failures are
// returned to the model as error results so it can correct itself.
func (l *Loop) runTools(ctx context.Context, set *Set, msg *anthropic.BetaMessage, maxBytes int, run *Run) []anthropic.BetaContentBlockParamUnion {
	var out []anthropic.BetaContentBlockParamUnion
	for _, block := range msg.Content {
		use, ok := block.AsAny().(anthropic.BetaToolUseBlock)
		if !ok {
			continue
		}
		run.ToolCalls++
		text, err := l.execute(ctx, set, use.Name, json.RawMessage(use.JSON.Input.Raw()))
		if err != nil {
			if l.Log != nil {
				l.Log.Debug("investigation tool call failed", "tool", use.Name, "err", err)
			}
			out = append(out, anthropic.NewBetaToolResultBlock(use.ID, err.Error(), true))
			continue
		}
		out = append(out, anthropic.NewBetaToolResultBlock(use.ID, truncate(text, maxBytes), false))
	}
	return out
}

func (l *Loop) execute(ctx context.Context, set *Set, name string, args json.RawMessage) (string, error) {
	tool, ok := set.Lookup(name)
	if !ok {
		return "", fmt.Errorf("there is no tool called %q; available tools: %s", name, strings.Join(set.Names(), ", "))
	}
	if l.Log != nil {
		l.Log.Debug("investigation tool call", "tool", name, "args", string(args))
	}
	return tool.Execute(ctx, args)
}

// budget caps iterations and billable tokens. Both are checked before each
// request, because a request's cost is committed once it is sent.
type budget struct {
	maxIterations int
	maxTokens     int64
	iterations    int
	tokens        int64
}

func (b *budget) begin() string {
	if b.maxIterations > 0 && b.iterations >= b.maxIterations {
		return fmt.Sprintf("reached the %d-request limit", b.maxIterations)
	}
	if b.maxTokens > 0 && b.tokens >= b.maxTokens {
		return fmt.Sprintf("reached the %d-token limit", b.maxTokens)
	}
	b.iterations++
	return ""
}

// record adds a response's usage. Cache reads are discounted, not free, so
// they count.
func (b *budget) record(u anthropic.BetaUsage) {
	b.tokens += u.InputTokens + u.OutputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
}

func truncate(text string, maxBytes int) string {
	if maxBytes <= 0 || len(text) <= maxBytes {
		return text
	}
	return text[:maxBytes] + fmt.Sprintf("\n\n[truncated: %d of %d bytes shown]", maxBytes, len(text))
}

// textOf concatenates a response's text blocks.
func textOf(msg *anthropic.BetaMessage) string {
	var b strings.Builder
	for _, block := range msg.Content {
		if text, ok := block.AsAny().(anthropic.BetaTextBlock); ok {
			b.WriteString(text.Text)
		}
	}
	return strings.TrimSpace(b.String())
}

// toolParams converts tool definitions into API tool parameters. The last
// tool carries a cache breakpoint, caching the tool block and system prompt
// that every turn re-sends.
func toolParams(defs []Def) ([]anthropic.BetaToolUnionParam, error) {
	params := make([]anthropic.BetaToolUnionParam, 0, len(defs))
	for i, def := range defs {
		schema, err := inputSchema(def.Schema)
		if err != nil {
			return nil, fmt.Errorf("tool %s: %w", def.Name, err)
		}
		tool := anthropic.BetaToolParam{
			Name:        def.Name,
			Description: param.NewOpt(def.Description),
			InputSchema: schema,
		}
		if i == len(defs)-1 {
			tool.CacheControl = anthropic.NewBetaCacheControlEphemeralParam()
		}
		params = append(params, anthropic.BetaToolUnionParam{OfTool: &tool})
	}
	return params, nil
}

// inputSchema passes a JSON Schema through without losing keywords the SDK
// struct has no field for, such as $defs and additionalProperties, which
// mcp-grafana emits.
func inputSchema(raw json.RawMessage) (anthropic.BetaToolInputSchemaParam, error) {
	if len(raw) == 0 {
		return anthropic.BetaToolInputSchemaParam{Properties: map[string]any{}}, nil
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return anthropic.BetaToolInputSchemaParam{}, fmt.Errorf("input schema is not a JSON object: %w", err)
	}
	schema := anthropic.BetaToolInputSchemaParam{Properties: generic["properties"]}
	if schema.Properties == nil {
		schema.Properties = map[string]any{}
	}
	if required, ok := generic["required"].([]any); ok {
		for _, item := range required {
			if name, ok := item.(string); ok {
				schema.Required = append(schema.Required, name)
			}
		}
	}
	extra := map[string]any{}
	for key, value := range generic {
		switch key {
		case "properties", "required", "type":
		default:
			extra[key] = value
		}
	}
	if len(extra) > 0 {
		schema.ExtraFields = extra
	}
	return schema, nil
}
