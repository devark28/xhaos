package tool

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
		// git init copies the template dir named by init.templateDir into the repo.
		{"init.templateDir", "[init]\n\ttemplateDir = ./tpl\n", true},
		// git skips a UTF-8 BOM at the start of a config and reads a
		// subsection name up to its closing quote, so ] can be inside it.
		{"a BOM before the first header", "\xef\xbb\xbf[diff]\n\texternal = x\n", true},
		{"a clean config with a BOM", "\xef\xbb\xbf[core]\n\tbare = false\n", false},
		{"a ] in a subsection, same-line key", "[diff \"a]b\"] external = x\n", true},
		{"an escaped quote and ] in a subsection", "[diff \"a\\\"]\"] external = x\n", true},
		{"a clean subsection with a ]", "[diff \"a]b\"]\n\tbinary = true\n", false},
		// git reads every header on a line: a second one may hold the driver.
		{"two headers on one line", "[core][diff]external=/abs/x.sh\n", true},
		{"two headers with spaces", "[core]  [diff \"x\"] [diff]external=/x\n", true},
		{"two headers, a clean key", "[core][core]bare = false\n", false},
		// A line ending in a backslash continues into the next line, so the
		// [user] below is part of core.x and fsmonitor is core's key.
		{"a continued value hides a header", "[core]\n\tx = a\\\n[user]\n\tfsmonitor = touch PWNED\n", true},
		{"a continued quoted value hides a header", "[core]\n\tx = \"a\\\n[user]\"\n\tfsmonitor = touch PWNED\n", true},
		{"a continued header line", "[core \\\n]\n\tfsmonitor = touch PWNED\n", true},
		{"a continuation with trailing blanks", "[core]\n\tx = a\\  \n[user]\n\tfsmonitor = touch PWNED\n", true},
		{"a backslash inside a value", "[core]\n\tx = a\\tb\n\tbare = false\n", false},
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

// returnsWithin runs ConfigRunsDrivers(root) and reports its answer and whether
// it returned within d. A FIFO that is opened for reading blocks, so the walk
// must not open it.
func returnsWithin(t *testing.T, root string, d time.Duration) (got, ok bool) {
	t.Helper()
	done := make(chan bool, 1)
	go func() { done <- ConfigRunsDrivers(root) }()
	select {
	case got = <-done:
		return got, true
	case <-time.After(d):
		return false, false
	}
}

// A FIFO where git looks for a repo or a config blocks an open for reading
// forever, so the walk and the floor would hang. Anything that is not a regular
// file (or a folder, where one is expected) counts as risky and is never opened.
func TestConfigRunsDriversFIFO(t *testing.T) {
	t.Run("a FIFO named .git", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("mkfifo", filepath.Join(root, "sub", ".git")).CombinedOutput(); err != nil {
			t.Skipf("mkfifo: %v %s", err, out)
		}
		got, ok := returnsWithin(t, root, 5*time.Second)
		if !ok {
			t.Fatal("ConfigRunsDrivers blocked on a FIFO named .git")
		}
		if !got {
			t.Error("a FIFO named .git is not a repo git can read: want risky (true)")
		}
	})
	t.Run("a FIFO as the config of a gitdir", func(t *testing.T) {
		root := repo(t, "")
		gd := filepath.Join(root, "gd")
		if err := os.MkdirAll(gd, 0o755); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("mkfifo", filepath.Join(gd, "config")).CombinedOutput(); err != nil {
			t.Skipf("mkfifo: %v %s", err, out)
		}
		if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "sub", ".git"), []byte("gitdir: "+gd+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		got, ok := returnsWithin(t, root, 5*time.Second)
		if !ok {
			t.Fatal("ConfigRunsDrivers blocked on a FIFO config")
		}
		if !got {
			t.Error("a FIFO as a gitdir's config: want risky (true)")
		}
	})
}

// A config bigger than the read bound counts as risky: git would read all of it.
func TestConfigRunsDriversBigConfig(t *testing.T) {
	big := "[core]\n\tbare = false\n" + strings.Repeat("# filler line that does nothing\n", 1<<20/30+10)
	if ConfigRunsDrivers(repo(t, big)) == false {
		t.Error("a config over the read bound: want true")
	}
}

// git reads config from outside the project when HOME, XDG_CONFIG_HOME or
// --git-dir say so, and a command that moves it out of the project with cd or
// -C does too: such a command is risky whatever the project's config says.
func TestGitDriverRiskOutsideConfig(t *testing.T) {
	root := repo(t, "[core]\n\tbare = false\n")
	for _, line := range []string{
		"HOME=. git diff",
		"XDG_CONFIG_HOME=. git status",
		"env HOME=/tmp/h git log",
		"git --git-dir=/tmp/bare --work-tree=. status",
		"git --work-tree=. status",
		"cd .. && git status",
		"cd /tmp && git status",
		"git -C /tmp status",
		"git -C ../other status",
		"cd - && git status",
		"git -C ~/other status",
		"git -C $HOME/x status",
	} {
		if !GitDriverRisk(root, line) {
			t.Errorf("GitDriverRisk(%q) = false, want true", line)
		}
	}
	for _, line := range []string{"git status", "git -C sub status", "cd sub && git diff", "git log -1", "echo HOME is here && git status"} {
		if GitDriverRisk(root, line) {
			t.Errorf("GitDriverRisk(%q) = true, want false (clean project)", line)
		}
	}
}

// A folder with a bare repo's HEAD, objects and refs is a repo that git can
// enter with -C or cd, though its name is not .git: its config counts.
func TestConfigRunsDriversBareGitDir(t *testing.T) {
	bare := func(config string) string {
		root := repo(t, "[core]\n\tbare = false\n")
		evil := filepath.Join(root, "evil")
		for _, d := range []string{"objects", "refs"} {
			if err := os.MkdirAll(filepath.Join(evil, d), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(evil, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(evil, "config"), []byte(config), 0o644); err != nil {
			t.Fatal(err)
		}
		return root
	}
	if ConfigRunsDrivers(bare("[core]\n\tbare = true\n")) {
		t.Error("a bare folder with a clean config: want false")
	}
	root := bare("[core]\n\tfsmonitor = ./hook\n")
	if !ConfigRunsDrivers(root) {
		t.Error("a bare folder whose config names an fsmonitor hook: want true")
	}
	if !GitDriverRisk(root, "git -C evil status") {
		t.Error("git -C into the bare folder: want risky")
	}
}

// git can enter a folder that is a symlink to one outside the project, and
// the walk does not follow links: such a link counts as risky. A link inside
// the project, or a dangling one, does not.
func TestConfigRunsDriversSymlinks(t *testing.T) {
	if os.Symlink("x", filepath.Join(t.TempDir(), "probe")) != nil {
		t.Skip("symlinks need a platform that has them")
	}
	outside := t.TempDir()
	root := repo(t, "[core]\n\tbare = false\n")
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if !ConfigRunsDrivers(root) {
		t.Error("a link to a folder outside the project: want risky")
	}
	inside := repo(t, "[core]\n\tbare = false\n")
	if err := os.MkdirAll(filepath.Join(inside, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(inside, "real"), filepath.Join(inside, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(inside, "missing"), filepath.Join(inside, "dangling")); err != nil {
		t.Fatal(err)
	}
	if ConfigRunsDrivers(inside) {
		t.Error("links inside the project and a dangling link: want false")
	}
}

// A project folder that is itself a link is still a project: the walk starts
// at the folder it points to, so the repo inside is checked.
func TestConfigRunsDriversSymlinkedRoot(t *testing.T) {
	if os.Symlink("x", filepath.Join(t.TempDir(), "probe")) != nil {
		t.Skip("symlinks need a platform that has them")
	}
	real := repo(t, "[diff]\n\texternal = ./x\n")
	link := filepath.Join(t.TempDir(), "project")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if !ConfigRunsDrivers(link) {
		t.Error("a project folder that is a link to a repo with a driver: want risky")
	}
}

// git -C is read only before the subcommand, and a revision range is not a
// folder: these lines have no folder outside the project, so they are not
// risky with a clean project config.
func TestGitDriverRiskNoFalseAsks(t *testing.T) {
	root := repo(t, "[core]\n\tbare = false\n")
	for _, line := range []string{
		"git log -C",
		"git diff -C HEAD~1..HEAD",
		"git log -C main..feature -- x",
		"git diff -C --stat A..B",
		"git -C sub log -C A..B",
		"cd sub && git diff A..B",
		"git log A..B",
	} {
		if GitDriverRisk(root, line) {
			t.Errorf("GitDriverRisk(%q) = true, want false (clean project, no folder outside)", line)
		}
	}
	// A second -C, after the first, still names a folder: it must be read.
	if !GitDriverRisk(root, "git -C sub -C .. status") {
		t.Error("GitDriverRisk(git -C sub -C ..) = false, want true")
	}
}

// A link to a folder outside the project counts only when the command moves
// git into it (cd or -C). A plain git command runs in the project.
func TestGitDriverRiskLinksOutsideOnlyWhenMoved(t *testing.T) {
	if os.Symlink("x", filepath.Join(t.TempDir(), "probe")) != nil {
		t.Skip("symlinks need a platform that has them")
	}
	root := repo(t, "[core]\n\tbare = false\n")
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(root, "sub"), 0o755)
	for _, line := range []string{"git status", "git log A..B", "git -C sub status", "cd sub && git status"} {
		if GitDriverRisk(root, line) {
			t.Errorf("GitDriverRisk(%q) = true, want false: no move into the outside link", line)
		}
	}
	for _, line := range []string{"cd link && git status", "git -C link status"} {
		if !GitDriverRisk(root, line) {
			t.Errorf("GitDriverRisk(%q) = false, want true: git enters the outside link", line)
		}
	}
}

// A global option before the subcommand may take the next word as its value
// (git --attr-source HEAD, git 2.40+). The parser must not read that value as
// the subcommand and miss a -C after it. A global option it does not know
// makes the line risky when a link leads outside, since the parser cannot
// tell where the -C would be.
func TestGitDriverRiskGlobalOptions(t *testing.T) {
	if os.Symlink("x", filepath.Join(t.TempDir(), "probe")) != nil {
		t.Skip("symlinks need a platform that has them")
	}
	root := repo(t, "[core]\n\tbare = false\n")
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		"git --attr-source HEAD -C link status",
		"git --attr-source=HEAD -C link status",
		"git --unknown-option x -C link status",
		"git --unknown-flag -C link status",
		"git -c a=b --attr-source HEAD -C link status",
	} {
		if !GitDriverRisk(root, line) {
			t.Errorf("GitDriverRisk(%q) = false, want true: git may enter the outside link", line)
		}
	}
	// Known global options that take no folder keep a plain command clean.
	for _, line := range []string{"git --no-pager status", "git -p log A..B", "git --attr-source HEAD status"} {
		if GitDriverRisk(root, line) {
			t.Errorf("GitDriverRisk(%q) = true, want false: no move into the outside link", line)
		}
	}
	// Without any link, an unknown option still hides the -C folder from the
	// check, so the line is risky: a -C to a folder outside would go unseen.
	clean := repo(t, "[core]\n\tbare = false\n")
	if !GitDriverRisk(clean, "git --unknown-flag -C /somewhere status") {
		t.Error("an unknown global option with no link: want risky (it may hide -C)")
	}
}

// cd takes a folder, but not a word that is an option or a redirection: the
// folder after them is the one bash enters, and a bare cd goes to $HOME.
func TestGitDriverRiskCdOptions(t *testing.T) {
	root := repo(t, "[core]\n\tbare = false\n")
	for _, line := range []string{
		"cd -P && git status",
		"cd -- && git status",
		"cd 2>/dev/null && git status",
		"cd -P 2>/dev/null && git status",
		"cd && git status",
		"cd - && git status",
	} {
		if !GitDriverRisk(root, line) {
			t.Errorf("GitDriverRisk(%q) = false, want true: cd goes outside the project", line)
		}
	}
	for _, line := range []string{
		"cd -P sub && git status",
		"cd -- sub && git status",
		"cd sub 2>/dev/null && git status",
	} {
		if GitDriverRisk(root, line) {
			t.Errorf("GitDriverRisk(%q) = true, want false: the folder is sub, inside the project", line)
		}
	}
}

// Another program may have git as an argument, and an option that is not
// git's: without a -C on the line, git does not move, so the line is not risky.
// A backslash at the end of a line joins it to the next (the shell removes
// both), so a word split over two lines is one word: the git scan, the
// commit check and the text check all read the joined line.
func TestJoinedLinesAreOneCommand(t *testing.T) {
	if !GitDriverRisk(t.TempDir(), "git --exec-pa\\\nth=. status") {
		t.Error("a --exec-path split over two lines was not read as --exec-path")
	}
	if !commitsOrPushes("gi\\\nt commit -m x", 0) {
		t.Error("gi\\ newline t commit is a commit")
	}
	if !commitsOrPushes("gi\\\nt push origin feat/x", 0) {
		t.Error("gi\\ newline t push is a push")
	}
	if commitsOrPushes("git status", 0) {
		t.Error("git status is not a commit or a push")
	}
}

func TestGitDriverRiskOtherProgramsNamingGit(t *testing.T) {
	if os.Symlink("x", filepath.Join(t.TempDir(), "probe")) != nil {
		t.Skip("symlinks need a platform that has them")
	}
	root := repo(t, "[core]\n\tbare = false\n")
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"grep -rn git --include=*.go .", "ls git --color=auto", "echo git --foo"} {
		if GitDriverRisk(root, line) {
			t.Errorf("GitDriverRisk(%q) = true, want false: git does not run here", line)
		}
	}
}
