package investigate

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// DefaultAllowedTools are the read-only mcp-grafana tools offered to the
// model. Anything the server advertises outside this list is ignored, and a
// call to it is refused even if the model invents one.
var DefaultAllowedTools = []string{
	"query_prometheus",
	"query_prometheus_histogram",
	"list_prometheus_metric_names",
	"list_prometheus_metric_metadata",
	"list_prometheus_label_names",
	"list_prometheus_label_values",
	"query_loki_logs",
	"query_loki_stats",
	"query_loki_patterns",
	"list_loki_label_names",
	"list_loki_label_values",
	"list_datasources",
	"search_dashboards",
	"get_dashboard_summary",
	"get_dashboard_panel_queries",
	"list_alert_rules",
	"get_alert_rule_by_uid",
}

// deniedTools advertise themselves as read-only but create state in Grafana
// (Sift investigations), so they are never offered.
var deniedTools = []string{"find_error_pattern_logs", "find_slow_requests"}

// Grafana opens sessions to one mcp-grafana server over streamable HTTP.
// mcp-grafana authenticates to Grafana itself, and should run with
// --disable-write.
type Grafana struct {
	URL     string
	Version string
	// Token is sent as a bearer token when set, for an mcp-grafana server
	// that requires one (MCP_GRAFANA_SERVER_TOKEN).
	Token          string
	ConnectTimeout time.Duration
	RequestTimeout time.Duration
	// Allowed lists the tools that may be published and called.
	Allowed []string
	Log     *slog.Logger
}

// Open connects and completes the MCP handshake. Callers must Close the
// returned session.
func (g *Grafana) Open(ctx context.Context) (*Session, error) {
	httpClient := &http.Client{Timeout: g.RequestTimeout}
	if g.Token != "" {
		httpClient.Transport = &bearerTransport{token: g.Token, next: http.DefaultTransport}
	}
	transport := &mcp.StreamableClientTransport{
		Endpoint:             g.URL,
		HTTPClient:           httpClient,
		MaxRetries:           2,
		DisableStandaloneSSE: true,
	}
	client := mcp.NewClient(
		&mcp.Implementation{Name: "reflex", Version: g.Version},
		&mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}},
	)
	connectCtx, cancel := context.WithTimeout(ctx, g.ConnectTimeout)
	defer cancel()
	session, err := client.Connect(connectCtx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("connect to mcp-grafana: %w", err)
	}
	return &Session{g: g, session: session}, nil
}

// bearerTransport adds an Authorization header to every request.
type bearerTransport struct {
	token string
	next  http.RoundTripper
}

func (t *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+t.token)
	return t.next.RoundTrip(req)
}

func (g *Grafana) allowed(name string) bool {
	return slices.Contains(g.Allowed, name) && !slices.Contains(deniedTools, name)
}

// Session is a live connection to the mcp-grafana server.
type Session struct {
	g       *Grafana
	session *mcp.ClientSession
}

// Close ends the session so the server can drop its state immediately.
func (s *Session) Close() error {
	return s.session.Close()
}

// Tools lists the server's tools, keeping only the allowed ones.
func (s *Session) Tools(ctx context.Context) ([]Tool, error) {
	var out []Tool
	for tool, err := range s.session.Tools(ctx, nil) {
		if err != nil {
			return nil, fmt.Errorf("list mcp-grafana tools: %w", err)
		}
		if slices.Contains(deniedTools, tool.Name) && s.g.Log != nil {
			s.g.Log.Warn("mcp-grafana advertises a tool that is not read-only; ignoring it",
				"tool", tool.Name, "hint", "run mcp-grafana with --disable-write")
		}
		if !s.g.allowed(tool.Name) {
			continue
		}
		schema, err := json.Marshal(tool.InputSchema)
		if err != nil {
			continue
		}
		desc := strings.TrimSpace(tool.Description)
		if desc == "" {
			desc = tool.Name
		}
		out = append(out, &mcpTool{session: s, def: Def{Name: tool.Name, Description: desc + "\n\n(Read-only.)", Schema: schema}})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("mcp-grafana exposes none of the allowed tools")
	}
	return out, nil
}

// Call invokes a tool and flattens its result to text.
func (s *Session) Call(ctx context.Context, name string, args json.RawMessage) (string, error) {
	if !s.g.allowed(name) {
		return "", fmt.Errorf("tool %q is not permitted", name)
	}
	var arguments any
	if len(args) > 0 {
		if err := json.Unmarshal(args, &arguments); err != nil {
			return "", fmt.Errorf("arguments for %s are not valid JSON: %w", name, err)
		}
	}
	callCtx, cancel := context.WithTimeout(ctx, s.g.RequestTimeout)
	defer cancel()
	res, err := s.session.CallTool(callCtx, &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		return "", fmt.Errorf("call %s: %w", name, err)
	}
	text := flatten(res)
	if res.IsError {
		// The tool ran and failed; the model reads this and can fix its query.
		return "", fmt.Errorf("%s failed: %s", name, text)
	}
	return text, nil
}

// flatten renders an MCP tool result as text.
func flatten(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, content := range res.Content {
		switch c := content.(type) {
		case *mcp.TextContent:
			b.WriteString(c.Text)
		case *mcp.ResourceLink:
			fmt.Fprintf(&b, "[resource %s]", c.URI)
		default:
			fmt.Fprintf(&b, "[unsupported %T content]", content)
		}
		b.WriteString("\n")
	}
	if b.Len() == 0 && res.StructuredContent != nil {
		if encoded, err := json.Marshal(res.StructuredContent); err == nil {
			b.Write(encoded)
		}
	}
	return strings.TrimSpace(b.String())
}

type mcpTool struct {
	session *Session
	def     Def
}

func (t *mcpTool) Def() Def { return t.def }

func (t *mcpTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	return t.session.Call(ctx, t.def.Name, args)
}
