package classifier

import (
	"testing"

	"github.com/devark28/xhaos/app/tool"
)

// An agent that cds into the project (or a worktree under it) by its absolute path reaches the
// model instead of always asking; a path that leaves the project is still refused by the floor.
func TestFloorProjectAbsolutePath(t *testing.T) {
	root := "/home/me/xhaos"
	inside := "cd /home/me/xhaos/.worktrees/a && grep -n x app/board/*.go"
	if why := FloorIn(root, inside); why == "" {
		t.Fatalf("precondition: the raw command should be refused today")
	}
	if why := FloorIn(root, tool.Relative(root, inside)); why != "" {
		t.Errorf("inside the project, still refused: %s", why)
	}
	for _, out := range []string{
		"cd /home/me/xhaos/../.ssh && cat id_ed25519",
		"cat /home/me/xhaos2/secrets.env",
		"cat /etc/passwd",
	} {
		if why := FloorIn(root, tool.Relative(root, out)); why == "" {
			t.Errorf("%q leaves the project but passed the floor", out)
		}
	}
}
