package investigate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var testSchema = map[string]any{
	"type":                 "object",
	"properties":           map[string]any{"expr": map[string]any{"type": "string"}},
	"required":             []any{"expr"},
	"additionalProperties": false,
}

// fakeGrafana stands in for an mcp-grafana server.
func fakeGrafana(t *testing.T) *Grafana {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "fake-mcp-grafana", Version: "1.0.0"}, nil)
	srv.AddTool(&mcp.Tool{Name: "query_prometheus", Description: "Run PromQL", InputSchema: testSchema},
		func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			raw, _ := json.Marshal(req.Params.Arguments)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "got " + string(raw)}}}, nil
		})
	srv.AddTool(&mcp.Tool{Name: "query_loki_logs", Description: "Run LogQL", InputSchema: testSchema},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "parse error"}}}, nil
		})
	for _, name := range []string{"update_dashboard", "find_slow_requests"} {
		srv.AddTool(&mcp.Tool{Name: name, InputSchema: testSchema},
			func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				t.Errorf("%s must never be called", name)
				return &mcp.CallToolResult{}, nil
			})
	}
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil))
	t.Cleanup(ts.Close)
	return &Grafana{
		URL:            ts.URL,
		ConnectTimeout: 5 * time.Second,
		RequestTimeout: 5 * time.Second,
		// find_slow_requests is allowed here to prove the denylist still wins.
		Allowed: []string{"query_prometheus", "query_loki_logs", "find_slow_requests"},
	}
}

func TestGrafanaTools(t *testing.T) {
	session, err := fakeGrafana(t).Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()

	tools, err := session.Tools(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools {
		names = append(names, tool.Def().Name)
	}
	slices.Sort(names)
	if strings.Join(names, ",") != "query_loki_logs,query_prometheus" {
		t.Fatalf("published tools = %v", names)
	}

	out, err := session.Call(t.Context(), "query_prometheus", json.RawMessage(`{"expr":"up"}`))
	if err != nil || out != `got {"expr":"up"}` {
		t.Errorf("query_prometheus = %q, %v", out, err)
	}
	if _, err := session.Call(t.Context(), "query_loki_logs", json.RawMessage(`{"expr":"{"}`)); err == nil || !strings.Contains(err.Error(), "parse error") {
		t.Errorf("tool error not surfaced: %v", err)
	}
	for _, name := range []string{"update_dashboard", "find_slow_requests"} {
		if _, err := session.Call(t.Context(), name, nil); err == nil || !strings.Contains(err.Error(), "not permitted") {
			t.Errorf("%s: err = %v, want not permitted", name, err)
		}
	}
}

func TestGrafanaUnreachable(t *testing.T) {
	g := &Grafana{URL: "http://127.0.0.1:1/mcp", ConnectTimeout: time.Second, RequestTimeout: time.Second}
	if _, err := g.Open(t.Context()); err == nil {
		t.Error("expected a connection error")
	}
}

func TestGrafanaBearerToken(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "fake-mcp-grafana", Version: "1.0.0"}, nil)
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer s3cret" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		mcpHandler.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	g := &Grafana{URL: ts.URL, ConnectTimeout: 5 * time.Second, RequestTimeout: 5 * time.Second}

	if session, err := g.Open(t.Context()); err == nil {
		_ = session.Close()
		t.Fatal("connected without a token")
	}
	g.Token = "s3cret"
	session, err := g.Open(t.Context())
	if err != nil {
		t.Fatalf("connect with token: %v", err)
	}
	_ = session.Close()
}
