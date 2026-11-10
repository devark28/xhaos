package node

import (
	"strings"
	"testing"

	"github.com/devark28/xhaos/app/node/wire"
)

// nodes_list says what confines a node's commands, also for older nodes
// that send no Sandbox.
func TestNodeSandboxText(t *testing.T) {
	for _, tc := range []struct {
		f    wire.Facts
		want string
	}{
		{wire.Facts{Sandbox: wire.SandboxLandlock, Landlock: 6}, "Landlock"},
		{wire.Facts{Sandbox: wire.SandboxBwrap}, "bubblewrap, confined"},
		{wire.Facts{Sandbox: wire.SandboxOff, Unsandbox: true}, "unconfined"},
		{wire.Facts{Sandbox: wire.SandboxNone}, "refuses commands"},
		{wire.Facts{Landlock: 4}, "Landlock"},
		{wire.Facts{Unsandbox: true}, "unconfined"},
		{wire.Facts{}, "refuses commands"},
	} {
		if got := nodeSandbox(&tc.f); !strings.Contains(got, tc.want) {
			t.Errorf("%+v → %q, want %q", tc.f, got, tc.want)
		}
	}
}
