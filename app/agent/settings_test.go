package agent_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/devark28/xhaos/app/agent"
	"github.com/devark28/xhaos/app/apperr"
	"github.com/devark28/xhaos/app/policy"
)

// The workspace's default policy is policy.Default until set; new top-level
// agents without a policy get it, an explicit policy wins, and subagents
// still inherit their parent's. Commits allowed cannot be the default.
func TestDefaultPolicyForNewAgents(t *testing.T) {
	s, _, _ := open(t)
	st, err := s.Agents.Settings(ws)
	if err != nil || st.DefaultPolicy != policy.Default() {
		t.Fatalf("fresh settings = %+v, %v; want policy.Default", st, err)
	}
	auto := policy.Policy{Access: "rw", Review: "auto", Commit: "review"}
	st, err = s.Agents.SaveSettings(agent.SaveSettingsRequest{WorkspaceID: ws, AgentSettings: agent.AgentSettings{SelfRename: true, DefaultPolicy: auto}})
	if err != nil || st.DefaultPolicy != auto || !st.SelfRename {
		t.Fatalf("saved = %+v, %v", st, err)
	}
	// An empty policy keeps the stored one (a page that only sends the switch).
	if st, err = s.Agents.SaveSettings(agent.SaveSettingsRequest{WorkspaceID: ws}); err != nil || st.DefaultPolicy != auto || st.SelfRename {
		t.Fatalf("saved without a policy = %+v, %v", st, err)
	}
	for _, bad := range []policy.Policy{{Access: "rw", Review: "auto", Commit: "allowed"}, {Access: "rw", Review: "sometimes", Commit: "review"}} {
		if _, err := s.Agents.SaveSettings(agent.SaveSettingsRequest{WorkspaceID: ws, AgentSettings: agent.AgentSettings{DefaultPolicy: bad}}); !errors.Is(err, apperr.Invalid) {
			t.Errorf("default %+v: err = %v, want Invalid", bad, err)
		}
	}

	a := mustCreate(t, s, agent.CreateRequest{IdempotencyKey: "a", Title: "Uses the default"})
	if a.Policy != auto {
		t.Fatalf("new agent policy = %+v, want the default %+v", a.Policy, auto)
	}
	ro := policy.Policy{Access: "ro", Review: "manual", Commit: "blocked"}
	b := mustCreate(t, s, agent.CreateRequest{IdempotencyKey: "b", Title: "Explicit", Policy: &ro})
	if b.Policy != ro {
		t.Fatalf("explicit policy = %+v, want %+v", b.Policy, ro)
	}
	// A subagent inherits its parent's, not the default.
	child := mustCreate(t, s, agent.CreateRequest{IdempotencyKey: "c", Title: "Child", ParentID: b.ID})
	if child.Policy != ro {
		t.Fatalf("subagent policy = %+v, want its parent's %+v", child.Policy, ro)
	}
	if _, err := s.Agents.Create(agent.CreateRequest{IdempotencyKey: "d", WorkspaceID: ws, Title: "Above", ParentID: b.ID, Policy: &auto}); !errors.Is(err, apperr.Invalid) {
		t.Fatalf("subagent above its parent: err = %v, want Invalid", err)
	}
}

// SetPolicy validates, changes the policy, and notes the change in the
// agent's history; the same policy again changes nothing. The owner may raise
// a subagent above its parent.
func TestSetPolicy(t *testing.T) {
	s, published, _ := open(t)
	a := mustCreate(t, s, agent.CreateRequest{IdempotencyKey: "a", Title: "Worker"})
	ro := policy.Policy{Access: "ro", Review: "manual", Commit: "blocked"}
	child := mustCreate(t, s, agent.CreateRequest{IdempotencyKey: "c", Title: "Child", ParentID: a.ID, Policy: &ro})
	*published = nil

	if _, err := s.Agents.SetPolicy(agent.PolicyRequest{IdempotencyKey: "bad", WorkspaceID: ws, AgentID: a.ID, Policy: policy.Policy{Access: "rw", Review: "auto"}}); !errors.Is(err, apperr.Invalid) {
		t.Fatalf("invalid policy: err = %v, want Invalid", err)
	}
	auto := policy.Policy{Access: "rw", Review: "auto", Commit: "review"}
	got, err := s.Agents.SetPolicy(agent.PolicyRequest{IdempotencyKey: "p1", WorkspaceID: ws, AgentID: a.ID, Policy: auto})
	if err != nil || got.Policy != auto {
		t.Fatalf("SetPolicy = %+v, %v", got, err)
	}
	if now := agentNow(t, s.Agents, a.ID); now.Policy != auto {
		t.Fatalf("stored policy = %+v", now.Policy)
	}
	if k := kinds(*published); len(k) != 2 || k[0] != agent.EventPolicy || k[1] != agent.EventMessage {
		t.Fatalf("events = %v, want policy then a note", k)
	}
	var d agent.PolicyData
	if err := json.Unmarshal((*published)[0].Data, &d); err != nil || d.Policy != auto || d.From != policy.Default() {
		t.Fatalf("policy event = %+v, %v", d, err)
	}
	var note agent.MessageData
	json.Unmarshal((*published)[1].Data, &note)
	if note.From != agent.FromSystem || !strings.Contains(note.Body, "Policy changed from rw · manual · commits review to rw · auto · commits review by you") ||
		!strings.Contains(note.Body, "waiting for you stay as they are") {
		t.Fatalf("note = %+v", note)
	}

	// A retry and the same policy again add nothing.
	*published = nil
	if _, err := s.Agents.SetPolicy(agent.PolicyRequest{IdempotencyKey: "p1", WorkspaceID: ws, AgentID: a.ID, Policy: auto}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Agents.SetPolicy(agent.PolicyRequest{IdempotencyKey: "p2", WorkspaceID: ws, AgentID: a.ID, Policy: auto}); err != nil {
		t.Fatal(err)
	}
	if len(*published) != 0 {
		t.Fatalf("unchanged policy published %v", kinds(*published))
	}

	// The owner raises the read-only subagent: allowed (Within binds agents only).
	allowed := policy.Policy{Access: "rw", Review: "auto", Commit: "allowed"}
	if got, err := s.Agents.SetPolicy(agent.PolicyRequest{IdempotencyKey: "p3", WorkspaceID: ws, AgentID: child.ID, Policy: allowed}); err != nil || got.Policy != allowed {
		t.Fatalf("raise subagent = %+v, %v", got, err)
	}

	if _, err := s.Agents.SetPolicy(agent.PolicyRequest{IdempotencyKey: "p4", WorkspaceID: ws, AgentID: 99, Policy: auto}); !errors.Is(err, apperr.NotFound) {
		t.Fatalf("missing agent: err = %v, want NotFound", err)
	}
}
