package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// gitRepos makes a repository with a submodule and a worktree, as git does.
func gitRepos(t *testing.T) (dir, parent string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir, _ = filepath.EvalSymlinks(t.TempDir())
	git := func(in string, args ...string) {
		t.Helper()
		c := exec.Command("git", append([]string{"-c", "user.email=t@t", "-c", "user.name=t", "-c", "protocol.file.allow=always"}, args...)...)
		c.Dir = in
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	lib, parent := filepath.Join(dir, "lib"), filepath.Join(dir, "parent")
	for _, d := range []string{lib, parent} {
		os.MkdirAll(d, 0o755)
		git(d, "init", "-q", "-b", "main")
		os.WriteFile(filepath.Join(d, "README"), []byte(d), 0o644)
		git(d, "add", ".")
		git(d, "commit", "-qm", "init")
	}
	git(parent, "submodule", "add", "-q", lib, "sub")
	git(parent, "worktree", "add", "-q", filepath.Join(dir, "wt"), "-b", "feature")
	return dir, parent
}

// Real worktrees and submodules still get their git folders.
func TestGitDirsWorktreeAndSubmodule(t *testing.T) {
	dir, parent := gitRepos(t)
	if got, want := GitDirs(filepath.Join(dir, "wt")), []string{filepath.Join(parent, ".git", "worktrees", "wt"), filepath.Join(parent, ".git")}; !slices.Equal(got, want) {
		t.Errorf("worktree = %v, want %v", got, want)
	}
	if got, want := GitDirs(filepath.Join(parent, "sub")), []string{filepath.Join(parent, ".git", "modules", "sub")}; !slices.Equal(got, want) {
		t.Errorf("submodule = %v, want %v", got, want)
	}
	if got, want := GitDirs(parent), []string{filepath.Join(parent, ".git")}; !slices.Equal(got, want) {
		t.Errorf("repository = %v, want %v", got, want)
	}
}

// A .git a command wrote must not open any other folder: the node's state
// (its key), ~/.ssh, another repository's git folders.
func TestGitDirsRefusesForeignTargets(t *testing.T) {
	dir, parent := gitRepos(t)
	secret := filepath.Join(dir, "state") // stands for /var/lib/xhaos-node or ~/.ssh
	os.MkdirAll(secret, 0o700)
	wtDir := filepath.Join(parent, ".git", "worktrees", "wt")
	for name, c := range map[string]struct {
		dotGit string // the .git file's content; "link" makes .git a symlink to secret
		extra  func(root string)
	}{
		"absolute gitdir":      {dotGit: "gitdir: " + secret},
		"relative gitdir":      {dotGit: "gitdir: ../state"},
		"symlinked .git":       {dotGit: "link"},
		"another's worktree":   {dotGit: "gitdir: " + wtDir},                                           // its gitdir names wt, not this folder
		"another's submodule":  {dotGit: "gitdir: " + filepath.Join(parent, ".git", "modules", "sub")}, // its worktree is parent/sub
		"main repository":      {dotGit: "gitdir: " + filepath.Join(parent, ".git")},
		"gitdir via a symlink": {dotGit: "gitdir: inner", extra: func(root string) { os.Symlink(secret, filepath.Join(root, "inner")) }},
		"commondir outside": {dotGit: "gitdir: inner", extra: func(root string) {
			os.MkdirAll(filepath.Join(root, "inner"), 0o755)
			os.WriteFile(filepath.Join(root, "inner", "commondir"), []byte(secret+"\n"), 0o644)
		}},
		"relative commondir outside": {dotGit: "gitdir: inner", extra: func(root string) {
			os.MkdirAll(filepath.Join(root, "inner"), 0o755)
			os.WriteFile(filepath.Join(root, "inner", "commondir"), []byte("../../state\n"), 0o644)
		}},
	} {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(dir, "p-"+filepath.Base(t.Name()))
			os.MkdirAll(root, 0o755)
			if c.dotGit == "link" {
				os.Symlink(secret, filepath.Join(root, ".git"))
			} else {
				os.WriteFile(filepath.Join(root, ".git"), []byte(c.dotGit+"\n"), 0o644)
			}
			if c.extra != nil {
				c.extra(root)
			}
			for _, d := range GitDirs(root) {
				if !within(d, root) {
					t.Errorf("opened %s outside the project", d)
				}
			}
		})
	}
	// A worktree's own folder whose commondir was changed (commands can
	// write it) still gives only its repository.
	os.WriteFile(filepath.Join(wtDir, "commondir"), []byte(secret+"\n"), 0o644)
	if got := GitDirs(filepath.Join(dir, "wt")); !slices.Equal(got, []string{wtDir}) {
		t.Errorf("tampered commondir = %v", got)
	}
}

// The attack itself: a command points the project's .git at a folder it
// must not reach, and the next command tries to read and write there.
func TestPlantedGitFileOpensNothing(t *testing.T) {
	enforcers := map[string]Config{}
	if ABI() > 0 {
		enforcers[Landlock] = Config{Mode: On}
	}
	if ProbeBwrap() == nil {
		enforcers[Bwrap] = Config{Mode: On, LimitDevices: true, Enforcer: Bwrap}
	}
	if len(enforcers) == 0 {
		t.Skip("neither Landlock nor bubblewrap works here")
	}
	for name, c := range enforcers {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			root, state := filepath.Join(dir, "work"), filepath.Join(dir, "state")
			os.MkdirAll(root, 0o755)
			os.MkdirAll(state, 0o700)
			os.WriteFile(filepath.Join(state, "node.key"), []byte("TOP SECRET"), 0o600)
			c.Cache = filepath.Join(dir, "cache")
			for _, plant := range []string{
				"printf 'gitdir: " + state + "\\n' > .git",
				"rm -f .git && ln -s " + state + " .git",
				"rm -f .git && mkdir -p inner && printf '" + state + "\\n' > inner/commondir && printf 'gitdir: inner\\n' > .git",
			} {
				if out, err := run(t, c, root, plant); err != nil {
					t.Fatalf("%s: %v %s", plant, err, out)
				}
				if out, err := run(t, c, root, "cat "+state+"/node.key"); err == nil || strings.Contains(out, "TOP SECRET") {
					t.Errorf("after %q the key was read: %q", plant, out)
				}
				if _, err := run(t, c, root, "echo x > "+state+"/planted"); err == nil {
					t.Errorf("after %q a file was written in the state folder", plant)
				}
			}
		})
	}
}
