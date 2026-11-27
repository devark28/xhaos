package mcpserver_test

import (
	"testing"

	"github.com/devark28/xhaos/app/agent"
	"github.com/devark28/xhaos/app/mcpserver"
	"github.com/devark28/xhaos/app/policy"
)

// create_agent takes what its arguments leave out from the workspace's
// default policy; given arguments still win.
func TestCreateAgentUsesTheDefaultPolicy(t *testing.T) {
	e := setup(t, false, false)
	cs := e.connect(t, e.token(t, "cc", mcpserver.ScopeRead, mcpserver.ScopeDrive))
	def := policy.Policy{Access: "rw", Review: "auto", Commit: "blocked"}
	if _, err := e.s.Agents.SaveSettings(agent.SaveSettingsRequest{WorkspaceID: ws, AgentSettings: agent.AgentSettings{DefaultPolicy: def}}); err != nil {
		t.Fatal(err)
	}
	pol := func(m map[string]any) policy.Policy {
		p := m["policy"].(map[string]any)
		return policy.Policy{Access: p["access"].(string), Review: p["review"].(string), Commit: p["commit"].(string)}
	}
	if got := pol(ok(t, cs, "create_agent", map[string]any{"task": "x"})); got != def {
		t.Fatalf("no arguments: policy = %+v, want the default %+v", got, def)
	}
	want := policy.Policy{Access: "rw", Review: "manual", Commit: "blocked"}
	if got := pol(ok(t, cs, "create_agent", map[string]any{"task": "y", "review": "manual"})); got != want {
		t.Fatalf("review given: policy = %+v, want %+v", got, want)
	}
}
