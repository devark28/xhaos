package run_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devark28/xhaos/app/agent"
	"github.com/devark28/xhaos/app/llm"
	"github.com/devark28/xhaos/app/provider"
)

// A commit an MCP client allowed runs Xhaos's own command (ToolData.Run),
// never the agent's text.
func TestAllowedRunReplacesTheAgentsCommand(t *testing.T) {
	root := t.TempDir()
	h := setupIn(t, root, toolModel([]llm.ToolCall{tc("c", "bash", map[string]any{"command": "git commit -m x; touch agent-ran"})}))
	h.s.Providers.Save(provider.SaveRequest{WorkspaceID: ws, ID: "f", Kind: "fake", APIKey: "k", Models: []string{"m1"}})
	a, _ := h.s.Agents.Create(agent.CreateRequest{IdempotencyKey: "a", WorkspaceID: ws, Prompt: "go", Provider: "f", Model: "m1"})
	h.waitStatus(t, a.ID, agent.StatusAttention)
	if _, err := h.s.Agents.Decide(agent.DecideRequest{IdempotencyKey: "d", WorkspaceID: ws, AgentID: a.ID, CallID: "c", Allow: true, Via: "cc", Run: "touch xhaos-ran"}); err != nil {
		t.Fatal(err)
	}
	h.waitStatus(t, a.ID, agent.StatusComplete)
	if _, err := os.Stat(filepath.Join(root, "xhaos-ran")); err != nil {
		t.Fatal("Xhaos's command did not run")
	}
	if _, err := os.Stat(filepath.Join(root, "agent-ran")); err == nil {
		t.Fatal("the agent's command ran")
	}
	d := h.tools(t, a.ID)["c"]
	if d.State != agent.ToolDone || d.Run != "touch xhaos-ran" || d.Via != "cc" || !strings.Contains(d.Output, "Xhaos ran its own command") {
		t.Fatalf("call = %+v", d)
	}
}

// The owner's own Allow never carries a Run, even when one is given.
func TestDenyCarriesNoRun(t *testing.T) {
	h := setup(t, toolModel([]llm.ToolCall{tc("c", "bash", map[string]any{"command": "git commit -m x"})}))
	h.s.Providers.Save(provider.SaveRequest{WorkspaceID: ws, ID: "f", Kind: "fake", APIKey: "k", Models: []string{"m1"}})
	a, _ := h.s.Agents.Create(agent.CreateRequest{IdempotencyKey: "a", WorkspaceID: ws, Prompt: "go", Provider: "f", Model: "m1"})
	h.waitStatus(t, a.ID, agent.StatusAttention)
	if _, err := h.s.Agents.Decide(agent.DecideRequest{IdempotencyKey: "d", WorkspaceID: ws, AgentID: a.ID, CallID: "c", Allow: false, Run: "touch x"}); err != nil {
		t.Fatal(err)
	}
	if d := h.tools(t, a.ID)["c"]; d.State != agent.ToolDenied || d.Run != "" {
		t.Fatalf("call = %+v", d)
	}
}
