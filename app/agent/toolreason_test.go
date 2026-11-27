package agent_test

import (
	"testing"

	"github.com/devark28/xhaos/app/agent"
)

// A classifier's reason written after the call was recorded as allowed stays
// with the call through its later states; other calls and policy allows are
// not given one.
func TestToolReasonSurvivesLaterStates(t *testing.T) {
	s, _, _ := open(t)
	store := agent.NewStore(s.DB(), nil)
	a := mustCreate(t, s, agent.CreateRequest{IdempotencyKey: "a", Prompt: "go"})
	allowed := agent.ToolData{CallID: "c1", Name: "bash", Effect: "exec", State: agent.ToolAllowed, By: "classifier"}
	if err := store.RecordTool(ws, a.ID, allowed); err != nil {
		t.Fatal(err)
	}
	if err := store.SetToolReason(ws, a.ID, "c1", "allowed by the local classifier: runs the tests"); err != nil {
		t.Fatal(err)
	}
	// The runner writes its later states from a copy taken before the reason.
	done := allowed
	done.State, done.Output = agent.ToolDone, "ok"
	if err := store.RecordTool(ws, a.ID, done); err != nil {
		t.Fatal(err)
	}
	tools, err := store.Tools(ws, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := tools["c1"]
	if got.State != agent.ToolDone || got.By != "classifier" || got.Reason != "allowed by the local classifier: runs the tests" || got.Output != "ok" {
		t.Errorf("c1 = %+v; want done, by the classifier, with the reason kept", got)
	}

	// A policy allow gets no reason from another call's reason.
	policy := agent.ToolData{CallID: "c2", Name: "bash", Effect: "exec", State: agent.ToolAllowed, By: "policy"}
	if err := store.RecordTool(ws, a.ID, policy); err != nil {
		t.Fatal(err)
	}
	tools, _ = store.Tools(ws, a.ID)
	if tools["c2"].Reason != "" {
		t.Errorf("c2 reason = %q, want none", tools["c2"].Reason)
	}

	// A reason for a call with no state is skipped, with no error.
	if err := store.SetToolReason(ws, a.ID, "nope", "x"); err != nil {
		t.Errorf("SetToolReason for an unknown call = %v", err)
	}
	tools, _ = store.Tools(ws, a.ID)
	if _, ok := tools["nope"]; ok {
		t.Error("an unknown call got a state")
	}
}
