package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/devark28/xhaos/app/agent"
	"github.com/devark28/xhaos/app/backend"
	"github.com/devark28/xhaos/app/llm"
	"github.com/devark28/xhaos/app/mcp"
	"github.com/devark28/xhaos/app/tool"
	"github.com/devark28/xhaos/app/workspace"
)

const ws = workspace.DefaultID

// testServer is a small MCP server: echo (read-only hint) and fail.
func testServer() *sdk.Server {
	s := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "1"}, nil)
	sdk.AddTool(s, &sdk.Tool{Name: "echo", Description: "Echo the text.", Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true}},
		func(_ context.Context, _ *sdk.CallToolRequest, in struct {
			Text string `json:"text"`
		}) (*sdk.CallToolResult, any, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "echo: " + in.Text + " " + os.Getenv("XHAOS_TEST_SECRET")}}}, nil, nil
		})
	sdk.AddTool(s, &sdk.Tool{Name: "fail.now", Description: "Always fails."},
		func(context.Context, *sdk.CallToolRequest, struct{}) (*sdk.CallToolResult, any, error) {
			return nil, nil, errors.New("it broke")
		})
	return s
}

// The test binary doubles as a local (stdio) MCP server.
func TestMain(m *testing.M) {
	if os.Getenv("XHAOS_MCP_STDIO") == "1" {
		if err := testServer().Run(context.Background(), &sdk.StdioTransport{}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func open(t *testing.T) *backend.Services {
	t.Helper()
	s, err := backend.Open(filepath.Join(t.TempDir(), "x.db"), backend.Options{Root: t.TempDir(), DisableRuns: true, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestRemoteServerToolsAndHeaders(t *testing.T) {
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return testServer() }, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close) // registered first, so it runs after the backend closes its sessions
	s := open(t)

	// Without the header the server refuses: the state says so, the config stays.
	got, err := s.MCP.Save(mcp.SaveRequest{WorkspaceID: ws, ID: "remote", Transport: "http", URL: srv.URL, Enabled: true})
	if err != nil || got.Status != "error" {
		t.Fatalf("no header: %+v, %v", got, err)
	}
	got, err = s.MCP.Save(mcp.SaveRequest{WorkspaceID: ws, ID: "remote", Name: "Remote", Transport: "http", URL: srv.URL, Headers: map[string]string{"Authorization": "Bearer tok"}, Trust: mcp.TrustRead, Enabled: true})
	if err != nil || got.Status != "connected" || len(got.Tools) != 2 || got.HeaderKeys[0] != "Authorization" {
		t.Fatalf("saved = %+v, %v", got, err)
	}
	if got.Tools[0].Name != "echo" || !got.Tools[0].ReadOnly || got.Tools[1].AgentName != "mcp__remote__fail_now" {
		t.Fatalf("tools = %+v", got.Tools)
	}
	// Header values are never returned.
	list, _ := s.MCP.Servers(ws)
	raw, _ := json.Marshal(list)
	if strings.Contains(string(raw), "Bearer tok") {
		t.Fatal("a header value leaked to clients")
	}
	// Re-saving with nil headers keeps them.
	if got, _ = s.MCP.Save(mcp.SaveRequest{WorkspaceID: ws, ID: "remote", Transport: "http", URL: srv.URL, Trust: mcp.TrustRead, Enabled: true}); got.Status != "connected" {
		t.Fatalf("re-save lost the header: %+v", got)
	}
}

func TestToolsForAgents(t *testing.T) {
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return testServer() }, nil)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	s := open(t)
	m := mcpManager(t, s)
	if _, err := s.MCP.Save(mcp.SaveRequest{WorkspaceID: ws, ID: "tools", Transport: "http", URL: srv.URL, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	specs := m.Specs(ws)
	if len(specs) != 2 || specs[0].Name != "mcp__tools__echo" || specs[0].Schema.Properties["text"] == nil {
		t.Fatalf("specs = %+v", specs)
	}
	call, err := m.Prepare(ws, agent.Agent{}, llm.ToolCall{ID: "c", Name: "mcp__tools__echo", Input: json.RawMessage(`{"text":"hi"}`)})
	if err != nil || call.Effect != tool.Exec || !strings.Contains(call.Summary, `echo {"text":"hi"}`) {
		t.Fatalf("prepare (trust ask) = %+v, %v", call, err)
	}
	out, err := m.Execute(context.Background(), ws, agent.Agent{}, call)
	if err != nil || strings.TrimSpace(out) != "echo: hi" {
		t.Fatalf("execute = %q, %v", out, err)
	}
	fail, _ := m.Prepare(ws, agent.Agent{}, llm.ToolCall{ID: "f", Name: "mcp__tools__fail_now", Input: json.RawMessage(`{}`)})
	if out, err := m.Execute(context.Background(), ws, agent.Agent{}, fail); err == nil || !strings.Contains(out+err.Error(), "it broke") {
		t.Fatalf("failing tool = %q, %v", out, err)
	}
	if _, err := m.Prepare(ws, agent.Agent{}, llm.ToolCall{ID: "x", Name: "mcp__tools__echo", Input: json.RawMessage(`[1]`)}); err == nil {
		t.Fatal("a non-object input was accepted")
	}
	if _, err := m.Prepare(ws, agent.Agent{}, llm.ToolCall{ID: "x", Name: "mcp__tools__nope", Input: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("an unknown tool was accepted")
	}
	// Disabled servers offer no tools; deleted ones are gone.
	s.MCP.Save(mcp.SaveRequest{WorkspaceID: ws, ID: "tools", Transport: "http", URL: srv.URL, Enabled: false})
	if len(m.Specs(ws)) != 0 {
		t.Fatal("a disabled server still offers tools")
	}
	if err := s.MCP.Delete(mcp.ServerRef{WorkspaceID: ws, ID: "tools"}); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.MCP.Servers(ws); len(list) != 0 {
		t.Fatalf("after delete = %+v", list)
	}
}

func TestTrustSetsTheEffect(t *testing.T) {
	for trust, want := range map[string]string{mcp.TrustAsk: tool.Exec, mcp.TrustAuto: tool.Write, mcp.TrustRead: tool.Read, "": tool.Exec} {
		if got := mcp.Effect(trust); got != want {
			t.Errorf("Effect(%q) = %s, want %s", trust, got, want)
		}
	}
}

func TestLocalServerOverStdioGetsItsEnv(t *testing.T) {
	s := open(t)
	m := mcpManager(t, s)
	self, _ := os.Executable()
	got, err := s.MCP.Save(mcp.SaveRequest{WorkspaceID: ws, ID: "local", Transport: "stdio", Command: self, Args: []string{"-test.run=^$"},
		Env: map[string]string{"XHAOS_MCP_STDIO": "1", "XHAOS_TEST_SECRET": "s3cret"}, Trust: mcp.TrustAuto, Enabled: true})
	if err != nil || got.Status != "connected" || strings.Join(got.EnvKeys, ",") != "XHAOS_MCP_STDIO,XHAOS_TEST_SECRET" {
		t.Fatalf("saved = %+v, %v", got, err)
	}
	call, err := m.Prepare(ws, agent.Agent{}, llm.ToolCall{ID: "c", Name: "mcp__local__echo", Input: json.RawMessage(`{"text":"yo"}`)})
	if err != nil || call.Effect != tool.Write {
		t.Fatalf("prepare = %+v, %v", call, err)
	}
	out, err := m.Execute(context.Background(), ws, agent.Agent{}, call)
	if err != nil || out != "echo: yo s3cret" {
		t.Fatalf("execute = %q, %v", out, err)
	}
	// A bad command reports an error state and keeps the config.
	got, err = s.MCP.Save(mcp.SaveRequest{WorkspaceID: ws, ID: "broken", Transport: "stdio", Command: "/nonexistent/mcp-server", Enabled: true})
	if err != nil || got.Status != "error" || got.Error == "" {
		t.Fatalf("broken = %+v, %v", got, err)
	}
	for _, bad := range []mcp.SaveRequest{
		{WorkspaceID: ws, ID: "Bad ID", Transport: "stdio", Command: "x"},
		{WorkspaceID: ws, ID: "a", Transport: "stdio"},
		{WorkspaceID: ws, ID: "a", Transport: "http", URL: "ftp://x"},
		{WorkspaceID: ws, ID: "a", Transport: "pipe", Command: "x"},
		{WorkspaceID: ws, ID: "a", Transport: "stdio", Command: "x", Trust: "always"},
	} {
		if _, err := s.MCP.Save(bad); err == nil {
			t.Errorf("Save(%+v) accepted", bad)
		}
	}
}

func mcpManager(t *testing.T, s *backend.Services) *mcp.Manager {
	t.Helper()
	m := s.MCPManager()
	if m == nil {
		t.Fatal("no MCP manager")
	}
	return m
}
