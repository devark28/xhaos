package tool

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/devark28/xhaos/app/llm"
	"github.com/devark28/xhaos/app/sandbox"
)

// gitRun runs git in dir for the test's setup (a commit, a config value).
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// readBash runs command as a read-class bash call in root, the way a command
// that the floor and the scan let through runs (the scan is Prepare's job).
func readBash(t *testing.T, root, command string) string {
	t.Helper()
	in, _ := json.Marshal(map[string]string{"command": command})
	call, err := Prepare(root, llm.ToolCall{ID: "1", Name: "bash", Input: in})
	if err != nil {
		t.Fatal(err)
	}
	call.Effect = Read
	out, _ := ExecuteWith(context.Background(), root, sandbox.Config{}, sandbox.Extra{}, call)
	return out
}

// planted is a script that leaves a marker file named name in the folder it
// runs in: git runs it as a hook or a driver, so the marker shows it ran.
func planted(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, "plant-"+name+".sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\ntouch "+filepath.Join(dir, name)+"\ncat\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// committedRepo is a repo with one commit of a.txt, in a folder of its own.
func committedRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	gitRun(t, root, "init", "-q", ".")
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("one\n"), 0o644)
	gitRun(t, root, "add", "a.txt")
	gitRun(t, root, "commit", "-q", "-m", "one")
	return root
}

// A read-class git command must not run the program the project's config
// names for the index refresh (fsmonitor): the environment sets it to false.
func TestReadGitSkipsFsmonitor(t *testing.T) {
	root := committedRepo(t)
	gitRun(t, root, "config", "core.fsmonitor", planted(t, root, "PWNED-FSM"))
	readBash(t, root, "git status")
	if _, err := os.Stat(filepath.Join(root, "PWNED-FSM")); err == nil {
		t.Error("git status ran the fsmonitor program of the project's config")
	}
}

// A hook in the project's hooks folder runs on the index refresh (post-index-change);
// core.hooksPath is /dev/null for a read-class command, so it does not run.
func TestReadGitSkipsHooks(t *testing.T) {
	root := committedRepo(t)
	hook := filepath.Join(root, ".git", "hooks", "post-index-change")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\ntouch "+filepath.Join(root, "PWNED-HOOK")+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A new mtime makes git refresh the index, which runs the hook.
	later := time.Now().Add(time.Hour)
	os.Chtimes(filepath.Join(root, "a.txt"), later, later)
	readBash(t, root, "git status")
	if _, err := os.Stat(filepath.Join(root, "PWNED-HOOK")); err == nil {
		t.Error("git status ran a hook from .git/hooks")
	}
}

// A textconv driver named in the config, selected by info/attributes (no
// .gitattributes needed), runs for git diff. The environment blanks it.
func TestReadGitSkipsTextconvViaInfoAttributes(t *testing.T) {
	root := committedRepo(t)
	marker := filepath.Join(root, "PWNED-TC")
	gitRun(t, root, "config", "diff.x.textconv", "touch "+marker+"; cat")
	os.MkdirAll(filepath.Join(root, ".git", "info"), 0o755)
	os.WriteFile(filepath.Join(root, ".git", "info", "attributes"), []byte("a.txt diff=x\n"), 0o644)
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("two\n"), 0o644)
	readBash(t, root, "git diff")
	if _, err := os.Stat(marker); err == nil {
		t.Error("git diff ran the textconv driver named by info/attributes")
	}
}

// A clean filter selected by info/attributes runs when git status reads the
// file. The environment blanks the filter git lists.
func TestReadGitSkipsFilterViaInfoAttributes(t *testing.T) {
	root := committedRepo(t)
	marker := filepath.Join(root, "PWNED-FL")
	gitRun(t, root, "config", "filter.y.clean", "touch "+marker+"; cat")
	os.MkdirAll(filepath.Join(root, ".git", "info"), 0o755)
	os.WriteFile(filepath.Join(root, ".git", "info", "attributes"), []byte("a.txt filter=y\n"), 0o644)
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("two\n"), 0o644)
	readBash(t, root, "git status; git diff")
	if _, err := os.Stat(marker); err == nil {
		t.Error("git ran the clean filter named by info/attributes")
	}
}

// A project folder with no repo of its own, inside a repo: git must not use
// the repo around it (GIT_CEILING_DIRECTORIES), so its fsmonitor does not run.
func TestReadGitIgnoresParentRepo(t *testing.T) {
	parent := committedRepo(t)
	gitRun(t, parent, "config", "core.fsmonitor", planted(t, parent, "PWNED-PARENT"))
	project := filepath.Join(parent, "project")
	os.MkdirAll(project, 0o755)
	readBash(t, project, "git status")
	if _, err := os.Stat(filepath.Join(parent, "PWNED-PARENT")); err == nil {
		t.Error("git in a folder with no repo used the repo around it")
	}
}

// A .git link to a folder whose config names a program: the config is
// still read, and the environment's fsmonitor=false wins over it.
func TestReadGitSymlinkedGitFolder(t *testing.T) {
	root := committedRepo(t)
	elsewhere := t.TempDir()
	gitRun(t, root, "config", "core.fsmonitor", planted(t, root, "PWNED-LINK"))
	os.Rename(filepath.Join(root, ".git"), filepath.Join(elsewhere, "gitdir"))
	os.Symlink(filepath.Join(elsewhere, "gitdir"), filepath.Join(root, ".git"))
	readBash(t, root, "git status")
	if _, err := os.Stat(filepath.Join(root, "PWNED-LINK")); err == nil {
		t.Error("git status through a symlinked .git ran the project's fsmonitor program")
	}
}

// git named through a quoted word, a backslash or a variable is still run
// with the safe settings: the gate does not read the word from the raw text.
func TestReadGitNamedIndirectlyStillSafe(t *testing.T) {
	for _, line := range []string{`g''it status`, `g\it status`, `G=git; $G status`} {
		root := committedRepo(t)
		gitRun(t, root, "config", "core.fsmonitor", planted(t, root, "PWNED-IND"))
		readBash(t, root, line)
		if _, err := os.Stat(filepath.Join(root, "PWNED-IND")); err == nil {
			t.Errorf("%q ran the fsmonitor program of the project's config", line)
		}
	}
}

// symlinkedGit is a project whose .git is a link to a git dir outside the
// project: the scan does not read the config behind such a link, git does.
func symlinkedGit(t *testing.T) (root, gitdir string) {
	t.Helper()
	root = committedRepo(t)
	gitdir = filepath.Join(t.TempDir(), "gitdir")
	if err := os.Rename(filepath.Join(root, ".git"), gitdir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(gitdir, filepath.Join(root, ".git")); err != nil {
		t.Fatal(err)
	}
	return root, gitdir
}

// appendConfig adds text to the config file of gitdir.
func appendConfig(t *testing.T, gitdir, text string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(gitdir, "config"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

// A config behind a symlinked .git that names a program git runs, or reaches
// an ext:: remote, makes a git command risky. The scan misses these; git's
// own parse (gitConfigRuns) does not.
func TestGitConfigBehindSymlinkedGitAsks(t *testing.T) {
	cases := map[string]string{
		"diff.external":       "[diff]\n\texternal = cat\n",
		"core.pager":          "[core]\n\tpager = cat\n",
		"uploadpack hook":     "[uploadpack]\n\tpackObjectsHook = cat\n",
		"remote ext url":      "[remote \"o\"]\n\turl = ext::cat\n",
		"insteadOf ext base":  "[url \"ext::cat\"]\n\tinsteadOf = x\n",
		"insteadOf ext value": "[url \"x\"]\n\tinsteadOf = ext::cat\n",
		"include a driver":    "[include]\n\tpath = extra.cfg\n",
		"unreadable config":   "[bogus\n",
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			root, gitdir := symlinkedGit(t)
			appendConfig(t, gitdir, text)
			if name == "include a driver" {
				os.WriteFile(filepath.Join(gitdir, "extra.cfg"), []byte("[diff]\n\texternal = cat\n"), 0o644)
			}
			if !GitDriverRisk(root, "git diff") {
				t.Errorf("git diff with %s behind the .git link is not risky", name)
			}
		})
	}
}

// A clean config behind a symlinked .git is not risky: the check does not ask
// for every linked repo.
func TestGitConfigCleanSymlinkedGitNotRisky(t *testing.T) {
	root, gitdir := symlinkedGit(t)
	appendConfig(t, gitdir, "[user]\n\tname = t\n")
	if GitDriverRisk(root, "git diff") {
		t.Error("a clean config behind a .git link is risky")
	}
}

// Read-class git still works with the settings: status and log answer.
func TestReadGitStillWorks(t *testing.T) {
	root := committedRepo(t)
	if out := readBash(t, root, "git status --short && git log --oneline"); !strings.Contains(out, "one") {
		t.Errorf("git log under the safe settings printed %q, want the commit", out)
	}
}

// gitSafeEnv blanks the filter and textconv drivers git lists, and sets the
// config count that holds them.
func TestGitSafeEnvBlanksDrivers(t *testing.T) {
	root := committedRepo(t)
	gitRun(t, root, "config", "filter.y.smudge", "cat")
	gitRun(t, root, "config", "diff.z.textconv", "cat")
	env := gitSafeEnv(root)
	set := map[string]string{}
	for i := 0; ; i++ {
		k, ok := envValue(env, "GIT_CONFIG_KEY_"+strconv.Itoa(i))
		if !ok {
			break
		}
		set[k], _ = envValue(env, "GIT_CONFIG_VALUE_"+strconv.Itoa(i))
	}
	if v, ok := set["filter.y.smudge"]; !ok || v != "" {
		t.Errorf("filter.y.smudge = %q (set %v), want blanked", v, ok)
	}
	if v, ok := set["diff.z.textconv"]; !ok || v != "" {
		t.Errorf("diff.z.textconv = %q (set %v), want blanked", v, ok)
	}
	if v := set["core.fsmonitor"]; v != "false" {
		t.Errorf("core.fsmonitor = %q, want false", v)
	}
}

// A folder with HEAD, objects, refs, config and info symlinked to a bare repo
// is a bare repo to git. Its info/attributes names a textconv driver, which
// runs for git log -p unless the environment blanks it.
func TestReadGitBareRepoSymlinkedHead(t *testing.T) {
	root := committedRepo(t)
	bare := filepath.Join(t.TempDir(), "bare")
	gitRun(t, t.TempDir(), "clone", "-q", "--bare", root, bare)
	marker := filepath.Join(t.TempDir(), "PWNED-BARE")
	gitRun(t, bare, "config", "diff.x.textconv", "touch "+marker+"; cat")
	os.MkdirAll(filepath.Join(bare, "info"), 0o755)
	os.WriteFile(filepath.Join(bare, "info", "attributes"), []byte("a.txt diff=x\n"), 0o644)
	project := t.TempDir()
	// git rejects a symlinked HEAD (it reads it as an old symref), so HEAD is
	// a copy; the rest are links, and packed-refs holds the branch.
	head, err := os.ReadFile(filepath.Join(bare, "HEAD"))
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(project, "HEAD"), head, 0o644)
	for _, name := range []string{"objects", "refs", "config", "info", "packed-refs"} {
		if err := os.Symlink(filepath.Join(bare, name), filepath.Join(project, name)); err != nil {
			t.Fatal(err)
		}
	}
	readBash(t, project, "git log -p")
	if _, err := os.Stat(marker); err == nil {
		t.Error("git log -p in a folder linked to a bare repo ran the textconv driver")
	}
}

// A linked worktree whose gitdir (reached through commondir) holds a
// config.worktree that names fsmonitor: the environment's false wins.
func TestReadGitWorktreeConfigFsmonitor(t *testing.T) {
	root := committedRepo(t)
	gitRun(t, root, "config", "core.repositoryformatversion", "1")
	wt := filepath.Join(t.TempDir(), "wt")
	gitRun(t, root, "worktree", "add", "-q", "--detach", wt)
	gitRun(t, wt, "config", "extensions.worktreeConfig", "true")
	gitRun(t, wt, "config", "--worktree", "core.fsmonitor", planted(t, wt, "PWNED-WT"))
	readBash(t, wt, "git status")
	if _, err := os.Stat(filepath.Join(wt, "PWNED-WT")); err == nil {
		t.Error("git status in a linked worktree ran the fsmonitor of config.worktree")
	}
}

// A partial clone whose promisor remote is an ext:: helper, with the project's
// own config allowing ext (git refuses it by default): a read that needs a
// missing blob makes git fetch it, which would run the helper.
func TestReadGitLazyFetchExtNotRun(t *testing.T) {
	root := committedRepo(t)
	marker := filepath.Join(root, "PWNED-EXT")
	script := filepath.Join(root, "plant-ext.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "config", "protocol.ext.allow", "always")
	gitRun(t, root, "config", "core.repositoryformatversion", "1")
	gitRun(t, root, "config", "extensions.partialclone", "origin")
	gitRun(t, root, "config", "remote.origin.url", "ext::"+script)
	gitRun(t, root, "config", "remote.origin.promisor", "true")
	// Drop a.txt's blob so that git log -p has to fetch it.
	out, err := exec.Command("git", "-C", root, "rev-parse", "HEAD:a.txt").Output()
	if err != nil {
		t.Fatal(err)
	}
	blob := strings.TrimSpace(string(out))
	os.Remove(filepath.Join(root, ".git", "objects", blob[:2], blob[2:]))
	readBash(t, root, "git log -p -1")
	if _, err := os.Stat(marker); err == nil {
		t.Error("a lazy fetch ran the ext:: helper of the promisor remote")
	}
}

// envValue returns the value of name in env.
func envValue(env []string, name string) (string, bool) {
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == name {
			return v, true
		}
	}
	return "", false
}
