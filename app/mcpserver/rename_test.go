package mcpserver_test

import (
	"strings"
	"testing"

	"github.com/devark28/xhaos/app/agent"
	"github.com/devark28/xhaos/app/mcpserver"
)

// A title an agent proposed for itself is listed as kind rename and
// answered with decision rename or keep, attributed to the token.
func TestRenameProposalsThroughMCP(t *testing.T) {
	e := setup(t, false, false)
	a, err := e.s.Agents.Create(agent.CreateRequest{IdempotencyKey: "a", WorkspaceID: ws, Title: "Old task"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agentStore(e).ProposeTitle(ws, a.ID, "New task", "the focus moved"); err != nil {
		t.Fatal(err)
	}
	cs := e.connect(t, e.token(t, "helper", mcpserver.ScopeRead, mcpserver.ScopeAnswer))

	list := ok(t, cs, "list_questions", nil)
	qs := list["questions"].([]any)
	if len(qs) != 1 {
		t.Fatalf("questions = %v", qs)
	}
	q := qs[0].(map[string]any)
	if q["kind"] != "rename" || q["id"] != mustQID(a.ID, "rename") || q["agent"] != float64(a.ID) || q["agent_title"] != "Old task" ||
		q["proposed_title"] != "New task" || q["reason"] != "the focus moved" || q["you_can_answer"] != true {
		t.Fatalf("rename item = %v", q)
	}
	if text, isErr := call(t, cs, "answer_question", map[string]any{"question_id": mustQID(a.ID, "rename"), "decision": "allow"}); !isErr || !strings.Contains(text, "rename or keep") {
		t.Fatalf("decision allow: %s", text)
	}
	res := ok(t, cs, "answer_question", map[string]any{"question_id": mustQID(a.ID, "rename"), "decision": "rename"})
	if res["state"] != "renamed" || res["agent_title"] != "New task" || res["renamed_from"] != "Old task" {
		t.Fatalf("answer = %v", res)
	}
	snap, _ := e.s.Agents.Snapshot(ws)
	if got := snap.Agents[0]; got.Title != "New task" || got.TitleProposal != nil {
		t.Fatalf("agent = %+v", got)
	}
	found := false
	for _, ev := range snap.Transcript {
		found = found || strings.Contains(string(ev.Data), "approved via MCP (helper)")
	}
	if !found {
		t.Fatal("the history does not name the MCP client")
	}
	if text, isErr := call(t, cs, "answer_question", map[string]any{"question_id": mustQID(a.ID, "rename"), "decision": "rename"}); !isErr || !strings.Contains(text, "no title proposal") {
		t.Fatalf("answering again: %s", text)
	}

	// keep declines it: the title stays.
	agentStore(e).ProposeTitle(ws, a.ID, "Another idea", "")
	if res := ok(t, cs, "answer_question", map[string]any{"question_id": mustQID(a.ID, "rename"), "decision": "keep"}); res["state"] != "kept" {
		t.Fatalf("keep = %v", res)
	}
	if snap, _ := e.s.Agents.Snapshot(ws); snap.Agents[0].Title != "New task" || snap.Agents[0].TitleProposal != nil {
		t.Fatalf("after keep = %+v", snap.Agents[0])
	}
	if qs := ok(t, cs, "list_questions", nil)["questions"].([]any); len(qs) != 0 {
		t.Fatalf("still listed: %v", qs)
	}
}
