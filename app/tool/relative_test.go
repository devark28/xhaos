package tool

import "testing"

func TestRelative(t *testing.T) {
	root := "/home/me/xhaos"
	for _, c := range []struct{ in, want string }{
		{"cd /home/me/xhaos/.worktrees/a && grep -n x app/board/*.go", "cd ./.worktrees/a && grep -n x app/board/*.go"},
		{"cd /home/me/xhaos", "cd ."},
		{`cat "/home/me/xhaos/go.mod"`, `cat "./go.mod"`},
		{"ls --dir=/home/me/xhaos/app", "ls --dir=./app"},
		{"cat /home/me/xhaos2/secret", "cat /home/me/xhaos2/secret"},   // a sibling folder is not the project
		{"cat /x/home/me/xhaos/go.mod", "cat /x/home/me/xhaos/go.mod"}, // root must start the word
		{"cat /home/me/xhaos/../.ssh/id", "cat ./../.ssh/id"},          // still climbs out
		{"cat /etc/passwd", "cat /etc/passwd"},
	} {
		if got := Relative(root, c.in); got != c.want {
			t.Errorf("Relative(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := Relative("", "cd /home/me/xhaos"); got != "cd /home/me/xhaos" {
		t.Errorf("no root changed the command: %q", got)
	}
}
