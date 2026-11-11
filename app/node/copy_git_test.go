package node

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// A sandboxed command can make a git repo in a project folder and set its
// core.fsmonitor. Copying that folder to a node lists its files with git,
// run by the server outside the sandbox: the hook must not run.
func TestCopyListingRunsNoRepoProgram(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	os.MkdirAll(sub, 0o755)
	pwned := filepath.Join(t.TempDir(), "PWNED")
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", sub}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q")
	os.WriteFile(filepath.Join(sub, "a.txt"), []byte("a"), 0o644)
	os.WriteFile(filepath.Join(sub, ".gitignore"), []byte("ignored.txt\n"), 0o644)
	os.WriteFile(filepath.Join(sub, "ignored.txt"), []byte("x"), 0o644)
	git("add", "a.txt")
	git("config", "core.fsmonitor", "touch "+pwned+"; false")
	git("config", "core.hooksPath", filepath.Join(sub, "hooks"))

	files, err := projectFiles(context.Background(), root, sub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pwned); err == nil {
		t.Error("listing the folder ran the repo's core.fsmonitor as the server")
	}
	// It still lists git's view: tracked and untracked, not ignored.
	if !slices.Contains(files, "a.txt") || !slices.Contains(files, ".gitignore") || slices.Contains(files, "ignored.txt") {
		t.Errorf("files = %v", files)
	}
}
