package tool

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/devark28/xhaos/app/llm"
)

// repo makes a project folder with a .git folder whose config is config
// ("" for no config file).
func repo(t *testing.T, config string) string {
	t.Helper()
	root := t.TempDir()
	git := filepath.Join(root, ".git")
	if err := os.MkdirAll(git, 0o755); err != nil {
		t.Fatal(err)
	}
	if config != "" {
		if err := os.WriteFile(filepath.Join(git, "config"), []byte(config), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestConfigRunsDrivers(t *testing.T) {
	cases := []struct {
		name   string
		config string
		want   bool
	}{
		{"clean config", "[core]\n\trepositoryformatversion = 0\n\tbare = false\n", false},
		{"empty config", "", false},
		{"diff.external", "[diff]\n\texternal = /tmp/x.sh\n", true},
		{"diff.external in any case", "[diff]\n\tExternal = x\n", true},
		{"textconv driver", "[diff \"lfs\"]\n\ttextconv = git-lfs cat-file --filters\n", true},
		{"command driver", "[diff \"x\"]\n\tcommand = ./diff.sh\n", true},
		{"driver without textconv or command", "[diff \"x\"]\n\tbinary = true\n", false},
		{"alias section", "[alias]\n\tst = status\n", true},
		{"named alias", "[alias \"lg\"]\n\tcommand = log\n", true},
		{"include", "[include]\n\tpath = ../other\n", true},
		{"includeIf", "[includeIf \"gitdir:/x/\"]\n\tpath = ../other\n", true},
		{"comment with the word", "# [alias] and external = x are comments\n[core]\n\tbare = false\n", false},
		{"core pager runs for output", "[core]\n\tpager = less\n", true},
		{"fsmonitor hook", "[core]\n\tfsmonitor = ./hook\n", true},
		{"fsmonitor boolean", "[core]\n\tfsmonitor\n", true},
		{"hooksPath", "[core]\n\thooksPath = .githooks\n", true},
		{"lfs-style clean filter", "[filter \"lfs\"]\n\tclean = git-lfs clean -- %f\n", true},
		{"smudge filter", "[filter \"x\"]\n\tSmudge = ./s\n", true},
		{"merge driver", "[merge \"m\"]\n\tdriver = ./m %O\n", true},
		{"credential helper", "[credential]\n\thelper = store\n", true},
		{"same-line key after the header", "[diff] external = /tmp/x.sh\n", true},
		{"same-line key after a subsection header", "[diff \"x\"] command = ./d\n", true},
		{"a broken header", "[diff\n\texternal = x\n", true},
		{"pager section", "[pager]\n\tdiff = cat\n", true},
		{"a key that is not a driver", "[filter \"x\"]\n\trequired = true\n", false},
		{"legacy dotted subsection", "[diff.x]\n\ttextconv = ./t\n", true},
		// git skips a UTF-8 BOM at the start of a config and reads a
		// subsection name up to its closing quote, so ] can be inside it.
		{"a BOM before the first header", "\xef\xbb\xbf[diff]\n\texternal = x\n", true},
		{"a clean config with a BOM", "\xef\xbb\xbf[core]\n\tbare = false\n", false},
		{"a ] in a subsection, same-line key", "[diff \"a]b\"] external = x\n", true},
		{"an escaped quote and ] in a subsection", "[diff \"a\\\"]\"] external = x\n", true},
		{"a clean subsection with a ]", "[diff \"a]b\"]\n\tbinary = true\n", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ConfigRunsDrivers(repo(t, c.config)); got != c.want {
				t.Errorf("ConfigRunsDrivers = %v, want %v", got, c.want)
			}
		})
	}
}

// A command can cd or use -C into a nested repo, so every repo under the
// project counts: a driver in a subfolder's repo is a driver.
func TestConfigRunsDriversNestedRepos(t *testing.T) {
	root := repo(t, "[core]\n\tbare = false\n")
	sub := filepath.Join(root, "pkg", "tool", ".git")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "config"), []byte("[alias]\n\tx = !sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !ConfigRunsDrivers(root) {
		t.Error("a nested repo whose config has an alias: want true")
	}
	// A nested worktree-style .git file pointing at a risky gitdir.
	gitdir := filepath.Join(root, "elsewhere")
	if err := os.MkdirAll(gitdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitdir, "config"), []byte("[diff]\n\texternal = x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	clean := t.TempDir()
	if err := os.MkdirAll(filepath.Join(clean, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clean, "sub", ".git"), []byte("gitdir: "+gitdir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !ConfigRunsDrivers(clean) {
		t.Error("a .git file whose gitdir sets diff.external: want true")
	}
}

// A per-worktree config (extensions.worktreeConfig) is read too.
func TestConfigRunsDriversWorktreeConfig(t *testing.T) {
	root := repo(t, "[core]\n\tbare = false\n")
	if err := os.WriteFile(filepath.Join(root, ".git", "config.worktree"), []byte("[core]\n\tfsmonitor = x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !ConfigRunsDrivers(root) {
		t.Error("config.worktree sets core.fsmonitor: want true")
	}
}

func TestConfigRunsDriversWithoutRepo(t *testing.T) {
	if ConfigRunsDrivers(t.TempDir()) {
		t.Error("a folder with no .git has no project config: want false")
	}
	if !ConfigRunsDrivers("") {
		t.Error("an empty root is unknown: want true")
	}
}

func TestConfigRunsDriversWorktree(t *testing.T) {
	// A worktree's .git is a file naming the gitdir; the config is the common
	// one that the gitdir's commondir names.
	base := t.TempDir()
	common := filepath.Join(base, "common")
	gitdir := filepath.Join(common, "worktrees", "w")
	for _, d := range []string{common, gitdir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(gitdir, "commondir"), []byte("../..\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "w")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: "+gitdir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(common, "config"), []byte("[diff]\n\texternal = x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !ConfigRunsDrivers(root) {
		t.Error("worktree whose common config sets diff.external: want true")
	}
	if err := os.WriteFile(filepath.Join(common, "config"), []byte("[core]\n\tbare = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ConfigRunsDrivers(root) {
		t.Error("worktree with a clean common config: want false")
	}
}

func TestConfigRunsDriversUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads any file")
	}
	root := repo(t, "[core]\n")
	if err := os.Chmod(filepath.Join(root, ".git", "config"), 0); err != nil {
		t.Fatal(err)
	}
	if !ConfigRunsDrivers(root) {
		t.Error("an unreadable config: want true")
	}
}

func TestGitWord(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		{"git diff", true},
		{"git status", true},
		{"git -C sub blame x", true},
		{"cd src && git diff --stat", true},
		{"/usr/bin/git show", true},
		{"time git log", true},
		{"X=1 git rev-parse HEAD", true},
		{"echo $(git status)", true},
		{"cat .git/config", false},
		{"grep -r diff .", false},
		{"go test ./...", false},
	}
	for _, c := range cases {
		if got := gitWord.MatchString(c.line); got != c.want {
			t.Errorf("gitWord(%q) = %v, want %v", c.line, got, c.want)
		}
	}
}

func TestGitDriverRiskGate(t *testing.T) {
	risky := repo(t, "[diff]\n\texternal = x\n")
	clean := repo(t, "[core]\n\tbare = false\n")
	cases := []struct {
		name, root, line string
		want             bool
	}{
		{"diff with a driver", risky, "git diff", true},
		{"log with a driver", risky, "git log --oneline", true},
		{"status runs filters and fsmonitor too", risky, "git status", true},
		{"blame with a driver", risky, "git -C sub blame x", true},
		{"wrapped git with a driver", risky, "time git diff", true},
		{"diff with a clean config", clean, "git diff", false},
		{"status with a clean config", clean, "git status", false},
		{"no git word", risky, "grep -r external .", false},
		{"git environment can name a driver", clean, "GIT_EXTERNAL_DIFF=./x git status", true},
		{"git exec path can name a driver", clean, "git --exec-path=. status", true},
		// Quote marks do not hide the word from the shell, so they must not hide it here.
		{"quotes split git", risky, "g''it diff", true},
		{"quotes split git in a log", risky, "gi\"\"t log -p", true},
		{"quotes split git in -C", risky, "g''it -C sub status", true},
		{"quotes split the exec path", clean, "git --exec-pa''th=. status", true},
		{"quotes split GIT_", clean, "GIT_EXTERN''AL_DIFF=./x git status", true},
		{"no repo", t.TempDir(), "git diff", false},
		{"unknown root", "", "git diff", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := GitDriverRisk(c.root, c.line); got != c.want {
				t.Errorf("GitDriverRisk = %v, want %v", got, c.want)
			}
		})
	}
}

// The gate (Prepare) asks for a git read whose project config names a driver,
// and keeps it a read otherwise.
func TestPrepareGitReadEffect(t *testing.T) {
	risky := repo(t, "[alias]\n\tx = status\n")
	clean := repo(t, "[core]\n\tbare = false\n")
	prep := func(root, command string) string {
		in, _ := json.Marshal(map[string]string{"command": command})
		call, err := Prepare(root, llm.ToolCall{ID: "1", Name: "bash", Input: in})
		if err != nil {
			t.Fatal(err)
		}
		return call.Effect
	}
	if got := prep(risky, "git diff"); got != Exec {
		t.Errorf("git diff with an alias in the project config: effect %s, want exec", got)
	}
	if got := prep(clean, "git diff"); got != Read {
		t.Errorf("git diff with a clean project config: effect %s, want read", got)
	}
	if got := prep(risky, "git status"); got != Exec {
		t.Errorf("git status with a driver in the config: effect %s, want exec", got)
	}
	if got := prep(clean, "git status"); got != Read {
		t.Errorf("git status with a clean config: effect %s, want read", got)
	}
}

// git -c and --config-env set config for one run, which can name a driver,
// a pager or an alias: they are exec, wherever they are.
func TestGitConfigOptionIsExec(t *testing.T) {
	for _, line := range []string{
		"git -c core.pager=x log",
		"git -c diff.external=x diff",
		"git diff --config-env=diff.external=VAR",
		"git --config-env=core.pager=VAR status",
	} {
		if got := ClassifyCommand(line); got != Exec {
			t.Errorf("ClassifyCommand(%q) = %s, want exec", line, got)
		}
	}
	if got := ClassifyCommand("git status"); got != Read {
		t.Errorf("ClassifyCommand(git status) = %s, want read", got)
	}
}
