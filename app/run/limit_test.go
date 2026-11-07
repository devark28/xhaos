package run

import (
	"testing"
	"time"

	"github.com/devark28/xhaos/app/agent"
)

// A dangerously-allowed session may raise or lift the limit on model calls in
// a row; when it ends (or never set one) the normal limit is back.
func TestCallLimit(t *testing.T) {
	now := time.Now()
	on := func(calls int) agent.Agent {
		return agent.Agent{Danger: &agent.Danger{Until: now.Add(time.Hour), Calls: calls}}
	}
	expired := agent.Agent{Danger: &agent.Danger{Until: now.Add(-time.Minute), Calls: 500}}
	for name, c := range map[string]struct {
		a    agent.Agent
		want int
	}{
		"no session":            {agent.Agent{}, maxSteps},
		"session, normal limit": {on(0), maxSteps},
		"session, raised":       {on(200), 200},
		"session, no limit":     {on(agent.NoCallLimit), 0},
		"expired session":       {expired, maxSteps},
	} {
		if got := callLimit(c.a, now); got != c.want {
			t.Errorf("%s: %d, want %d", name, got, c.want)
		}
	}
}
