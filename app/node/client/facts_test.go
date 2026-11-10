package client

import (
	"testing"

	"github.com/devark28/xhaos/app/node/wire"
	"github.com/devark28/xhaos/app/sandbox"
)

// Facts say which sandbox confines commands, and Sandboxed only for a real one.
func TestFactsSandbox(t *testing.T) {
	for _, tc := range []struct {
		name        string
		c           Config
		sb          sandbox.Choice
		kind        string
		sandboxed   bool
		landlockABI int
	}{
		{"landlock", Config{}, sandbox.Choice{Kind: sandbox.Landlock, ABI: 6, Note: "Landlock ABI 6"}, wire.SandboxLandlock, true, 6},
		{"bwrap", Config{}, sandbox.Choice{Kind: sandbox.Bwrap, Note: "bubblewrap (this kernel has no Landlock)"}, wire.SandboxBwrap, true, 0},
		{"none", Config{}, sandbox.Choice{Note: "this kernel has no Landlock; bubblewrap: bwrap is not installed"}, wire.SandboxNone, false, 0},
		{"unsandboxed", Config{Unsandboxed: true}, sandbox.Choice{ABI: 4}, wire.SandboxOff, false, 4},
	} {
		f := Facts(tc.c, tc.sb, "test")
		if f.Sandbox != tc.kind || f.Sandboxed != tc.sandboxed || f.Landlock != tc.landlockABI || f.SandboxNote == "" {
			t.Errorf("%s: sandbox %q sandboxed %v landlock %d note %q", tc.name, f.Sandbox, f.Sandboxed, f.Landlock, f.SandboxNote)
		}
	}
}
