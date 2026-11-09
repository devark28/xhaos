package danger

import (
	"github.com/devark28/xhaos/app/sandbox"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The text check lets feature-branch work through and refuses what cannot
// be undone afterwards.
func TestCheckCommand(t *testing.T) {
	ok := []string{
		"go test ./...",
		"git status && git diff",
		"git add -A && git commit -m 'fix'",
		"git worktree add -b feat/x .worktrees/feat-x origin/main",
		"cd .worktrees/feat-x && git switch -c feat/y",
		"git switch main", // the checkout is put back afterwards if it moves
		"git checkout -- main.go",
		"git push -u origin feat/x",
		"git push origin feat/x:feat/x",
		"git push origin HEAD:refs/heads/feat/x",
		"git branch",
		"git branch --show-current",
	}
	for _, c := range ok {
		if why := CheckCommand(c); why != "" {
			t.Errorf("%q refused: %s", c, why)
		}
	}
	refused := []string{
		"git push",
		"git push origin",
		"git push origin HEAD",
		"git push origin main",
		"git push origin HEAD:main",
		"git push origin feat/x:release-2",
		"git push --force origin feat/x",
		"git push -f origin feat/x",
		"git push --force-with-lease origin feat/x",
		"git push origin +feat/x",
		"git push origin :feat/x",
		"git push --delete origin feat/x",
		"git push --all origin",
		"git push --mirror origin",
		"git push --tags origin",
		"git -C . push origin main",
		"echo hi; git push origin master",
		"git branch -D feat/x",
		"git branch -m renamed",
	}
	for _, c := range refused {
		if why := CheckCommand(c); why == "" {
			t.Errorf("%q allowed", c)
		}
	}
}

// Quoted, escaped and nested forms of git push and git branch are read as
// the same commands: the shell runs them, so the check must too.
func TestCheckCommandReadsQuotedForms(t *testing.T) {
	refused := []string{
		"gi''t push --force origin main",
		"g'i't push --force origin main",
		`"git" push --force origin main`,
		`git "push" --force origin main`,
		`g\it push -f origin feat/x`,
		"'git' push origin main",
		"/usr/bin/git push origin main",
		`"git" branch -D x`,
		"g''it branch -d x",
		`git "branch" -D x`,
		`git -C . "push" origin HEAD:main`,
		`sh -c 'git push --force origin main'`,
		`bash -c "gi''t push origin main"`,
		"echo ok && 'git' push origin release-1",
	}
	for _, c := range refused {
		if why := CheckCommand(c); why == "" {
			t.Errorf("%q allowed", c)
		}
	}
	// Feature-branch work in quoted forms still passes.
	ok := []string{
		`git push -u origin "feat/x"`,
		"g'i't status",
		`git commit -m "branch -D is refused, not run"`,
	}
	for _, c := range ok {
		if why := CheckCommand(c); why != "" {
			t.Errorf("%q refused: %s", c, why)
		}
	}
}

// Around a dangerous command, protected branches and the project's checkout
// are put back; the agent's own branches are left as they are.
func TestGuardRepairsProtectedState(t *testing.T) {
	root := t.TempDir()
	run := func(cmd string) string {
		t.Helper()
		c := exec.Command("bash", "-c", cmd)
		c.Dir = root
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v %s", cmd, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("git init -q -b main && git commit -q --allow-empty -m init && git branch release-1")
	mainAt, releaseAt := run("git rev-parse main"), run("git rev-parse release-1")
	s := &Service{Sandbox: sandbox.Config{Mode: sandbox.Off, Cache: t.TempDir()}}

	before, err := s.Capture(root)
	if err != nil || !before.Git || before.Head != "refs/heads/main" || before.Protected["main"] != mainAt {
		t.Fatalf("capture = %+v %v", before, err)
	}
	// What an agent might do: commit on main, delete a release branch, make
	// a new protected one, start its own branch, and leave the checkout on it.
	run("git commit -q --allow-empty -m sneaky && git branch -D release-1 && git branch release-x && git switch -q -c feat/mine && git commit -q --allow-empty -m mine")
	mine := run("git rev-parse feat/mine")
	note := s.Repair(root, before)
	for _, want := range []string{"put main back", "restored the deleted protected branch release-1", "removed the new protected branch release-x", "switched the project's checkout back to main"} {
		if !strings.Contains(note, want) {
			t.Errorf("note lacks %q: %s", want, note)
		}
	}
	if got := run("git rev-parse main"); got != mainAt {
		t.Errorf("main at %s, want %s", got, mainAt)
	}
	if got := run("git rev-parse release-1"); got != releaseAt {
		t.Errorf("release-1 at %s, want %s", got, releaseAt)
	}
	if out := run("git branch --list release-x"); out != "" {
		t.Errorf("release-x still there: %q", out)
	}
	if got := run("git symbolic-ref HEAD"); got != "refs/heads/main" {
		t.Errorf("checkout on %s", got)
	}
	if got := run("git rev-parse feat/mine"); got != mine {
		t.Errorf("the agent's own branch changed: %s, want %s", got, mine)
	}
	// Nothing to undo: no note.
	if note := s.Repair(root, before); note != "" {
		t.Errorf("second repair: %s", note)
	}
	// Not a git project: nothing guarded.
	if g, err := s.Capture(t.TempDir()); err != nil || g.Git {
		t.Errorf("non-git capture = %+v %v", g, err)
	}
}

func TestCurrentBranch(t *testing.T) {
	dir := t.TempDir()
	if b, err := CurrentBranch(dir); b != "" || err != nil {
		t.Fatalf("no repo = %q %v", b, err)
	}
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/feature/x\n"), 0o644)
	if b, err := CurrentBranch(dir); b != "feature/x" || err != nil {
		t.Fatalf("branch = %q %v", b, err)
	}
	os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("3f2a9c0d\n"), 0o644)
	if _, err := CurrentBranch(dir); err == nil {
		t.Fatal("detached HEAD accepted")
	}
}

// A submodule's .git is a file: the branch comes from where it points.
func TestCurrentBranchInASubmodule(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	gitdir := filepath.Join(dir, ".git", "modules", "sub")
	os.MkdirAll(sub, 0o755)
	os.MkdirAll(gitdir, 0o755)
	os.WriteFile(filepath.Join(sub, ".git"), []byte("gitdir: ../.git/modules/sub\n"), 0o644)
	os.WriteFile(filepath.Join(gitdir, "HEAD"), []byte("ref: refs/heads/feature/uvc\n"), 0o644)
	if b, err := CurrentBranch(sub); b != "feature/uvc" || err != nil {
		t.Fatalf("branch = %q %v", b, err)
	}
}

func TestParseDuration(t *testing.T) {
	for text, want := range map[string]time.Duration{
		"10s": 10 * time.Second, "15m": 15 * time.Minute, "1h": time.Hour, "10h": 10 * time.Hour,
		"1d": 24 * time.Hour, "1h30m": 90 * time.Minute, " 2H ": 2 * time.Hour, "7d": 7 * 24 * time.Hour,
	} {
		if got, err := ParseDuration(text); err != nil || got != want {
			t.Errorf("%q = %v %v, want %v", text, got, err, want)
		}
	}
	for _, bad := range []string{"", "9s", "8d", "1w", "15", "m", "1h and 2m", "-5m", "99999999999h"} {
		if got, err := ParseDuration(bad); err == nil {
			t.Errorf("%q accepted as %v", bad, got)
		}
	}
}
