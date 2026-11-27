package run_test

import (
	"strings"
	"testing"
	"time"

	"github.com/devark28/xhaos/app/agent"
	"github.com/devark28/xhaos/app/llm"
	"github.com/devark28/xhaos/app/provider"
)

func snapAgent(t *testing.T, h *harness, id int64) agent.Agent {
	t.Helper()
	snap, err := h.s.Agents.Snapshot(ws)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range snap.Agents {
		if a.ID == id {
			return a
		}
	}
	t.Fatalf("no agent #%d", id)
	return agent.Agent{}
}

// propose_title never waits for the owner: the call is done at once, the
// turn goes on to the next call and the final answer, and the proposal
// waits on the agent for the owner.
func TestProposeTitleNeverWaits(t *testing.T) {
	h := setup(t, toolModel(
		[]llm.ToolCall{tc("t1", "propose_title", map[string]any{"title": "First idea", "reason": "new task"})},
		[]llm.ToolCall{tc("t2", "propose_title", map[string]any{"title": "Second idea"}), tc("bad", "propose_title", map[string]any{"title": "two\nlines"})},
		[]llm.ToolCall{tc("ls", "list_dir", map[string]any{})},
	))
	h.s.Providers.Save(provider.SaveRequest{WorkspaceID: ws, ID: "f", Kind: "fake", APIKey: "k", Models: []string{"m1"}})
	a, _ := h.s.Agents.Create(agent.CreateRequest{IdempotencyKey: "a", WorkspaceID: ws, Title: "Old task", Prompt: "go", Provider: "f", Model: "m1"})
	h.waitStatus(t, a.ID, agent.StatusComplete)

	calls := h.tools(t, a.ID)
	if calls["t1"].State != agent.ToolDone || calls["t2"].State != agent.ToolDone || calls["bad"].State != agent.ToolFailed || calls["ls"].State != agent.ToolDone {
		t.Fatalf("states = %+v", calls)
	}
	h.mu.Lock()
	for _, e := range h.events {
		if e.AgentID == a.ID && e.Kind == agent.EventStatus && strings.Contains(string(e.Data), agent.StatusAttention) {
			h.mu.Unlock()
			t.Fatal("the agent waited for the owner")
		}
	}
	h.mu.Unlock()
	got := h.lastReply(t)
	for _, want := range []string{"false:Proposed “First idea” (your title is still “Old task”)", "keep working", "replaces your earlier proposal “First idea”", "true:the title must be one line"} {
		if !strings.Contains(got, want) {
			t.Fatalf("model saw %q, want %q", got, want)
		}
	}
	if now := snapAgent(t, h, a.ID); now.Title != "Old task" || now.TitleProposal == nil || now.TitleProposal.Title != "Second idea" {
		t.Fatalf("agent = %+v", now)
	}
	// The owner's rename applies it; the agent is not woken (it never waited).
	if _, err := h.s.Agents.DecideTitle(agent.TitleDecisionRequest{IdempotencyKey: "r", WorkspaceID: ws, AgentID: a.ID, Rename: true}); err != nil {
		t.Fatal(err)
	}
	if now := snapAgent(t, h, a.ID); now.Title != "Second idea" || now.TitleProposal != nil || now.Status != agent.StatusComplete {
		t.Fatalf("after rename = %+v", now)
	}
}

// While dangerously allowed, the rename applies at once and the model is told.
func TestProposeTitleAppliesInDangerMode(t *testing.T) {
	h := setup(t, toolModel([]llm.ToolCall{tc("t", "propose_title", map[string]any{"title": "Dangerous work"})}))
	h.s.Providers.Save(provider.SaveRequest{WorkspaceID: ws, ID: "f", Kind: "fake", APIKey: "k", Models: []string{"m1"}})
	a, _ := h.s.Agents.Create(agent.CreateRequest{IdempotencyKey: "a", WorkspaceID: ws, Title: "Old task", Provider: "f", Model: "m1"})
	if err := agent.NewStore(h.s.DB(), nil).SetDanger(ws, a.ID, &agent.Danger{Until: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	h.s.Agents.Message(agent.MessageRequest{IdempotencyKey: "m", WorkspaceID: ws, AgentIDs: []int64{a.ID}, Body: "go"})
	h.waitStatus(t, a.ID, agent.StatusComplete)
	if got := h.lastReply(t); !strings.Contains(got, "false:Renamed from “Old task” to “Dangerous work” (applied at once: dangerously allow is on)") {
		t.Fatalf("model saw %q", got)
	}
	if now := snapAgent(t, h, a.ID); now.Title != "Dangerous work" || now.TitleProposal != nil {
		t.Fatalf("agent = %+v", now)
	}
	found := false
	for _, m := range transcript(t, h) {
		found = found || strings.Contains(m, "applied automatically (dangerously allow is on)")
	}
	if !found {
		t.Fatal("the transcript does not note the rename")
	}
}
