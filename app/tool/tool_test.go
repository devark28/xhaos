package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/devark28/xhaos/app/llm"
)

func call(name string, input any) llm.ToolCall {
	raw, _ := json.Marshal(input)
	return llm.ToolCall{ID: "c1", Name: name, Input: raw}
}

func run(t *testing.T, root, name string, input any) (string, error) {
	t.Helper()
	c, err := Prepare(root, call(name, input))
	if err != nil {
		return "", err
	}
	return Execute(context.Background(), root, c)
}

func TestResolveStaysInsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	os.Symlink(outside, filepath.Join(root, "link"))
	for _, p := range []string{"../x", "/etc/passwd", "a/../../x", "link/x", "link"} {
		if _, err := Resolve(root, p); err == nil {
			t.Errorf("Resolve(%q) allowed a path outside the root", p)
		}
	}
	for _, p := range []string{"", ".", "a/b.txt", "new/dir/file", filepath.Join(root, "abs.txt")} {
		if _, err := Resolve(root, p); err != nil {
			t.Errorf("Resolve(%q): %v", p, err)
		}
	}
}

func TestWriteEditReadSearchList(t *testing.T) {
	root := t.TempDir()
	if _, err := run(t, root, "write_file", map[string]any{"path": "src/a.txt", "content": "one\ntwo\nthree\n"}); err != nil {
		t.Fatal(err)
	}
	c, err := Prepare(root, call("edit_file", map[string]any{"path": "src/a.txt", "old_string": "two", "new_string": "TWO"}))
	if err != nil || c.Effect != Write || !strings.Contains(c.Preview, "-two") || !strings.Contains(c.Preview, "+TWO") {
		t.Fatalf("edit prepare = %+v, %v", c, err)
	}
	if _, err := Execute(context.Background(), root, c); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, root, "read_file", map[string]any{"path": "src/a.txt"})
	if err != nil || !strings.Contains(out, "2\tTWO") {
		t.Fatalf("read = %q, %v", out, err)
	}
	out, _ = run(t, root, "search", map[string]any{"pattern": "^th"})
	if out != "src/a.txt:3: three" {
		t.Fatalf("search = %q", out)
	}
	out, _ = run(t, root, "list_dir", map[string]any{})
	if out != "src/\n" {
		t.Fatalf("list = %q", out)
	}
	if _, err := run(t, root, "edit_file", map[string]any{"path": "src/a.txt", "old_string": "missing", "new_string": "x"}); err == nil {
		t.Fatal("edit of a missing string succeeded")
	}
}

func TestPrepareRejectsBadInput(t *testing.T) {
	root := t.TempDir()
	bad := []llm.ToolCall{
		call("nope", map[string]any{}),
		call("read_file", map[string]any{}),
		call("read_file", map[string]any{"path": "a", "extra": 1}),
		call("write_file", map[string]any{"path": "a"}),
		call("write_file", map[string]any{"path": "../a", "content": ""}),
		call("bash", map[string]any{"command": "  "}),
		call("search", map[string]any{"pattern": "("}),
		{ID: "c", Name: "bash", Input: json.RawMessage(`{"command": "ls`)},
	}
	for _, c := range bad {
		if _, err := Prepare(root, c); err == nil {
			t.Errorf("Prepare(%s %s) accepted bad input", c.Name, c.Input)
		}
	}
}

func TestBash(t *testing.T) {
	root := t.TempDir()
	out, err := run(t, root, "bash", map[string]any{"command": "pwd; echo hi >&2"})
	real, _ := filepath.EvalSymlinks(root)
	if err != nil || !strings.Contains(out, real) || !strings.Contains(out, "hi") {
		t.Fatalf("bash = %q, %v", out, err)
	}
	if _, err := run(t, root, "bash", map[string]any{"command": "exit 3"}); err == nil || !strings.Contains(err.Error(), "exit code 3") {
		t.Fatalf("exit status error = %v", err)
	}
	for cmd, want := range map[string]bool{
		"git commit -m x": true, "cd a && git push": true, "git status": false, "echo commit": false,
		// quoted and nested forms are commits too (the shell runs them)
		"gi''t commit -m x":                true,
		`"git" push origin feat/x`:         true,
		"g\\it commit -m x":                true,
		"sh -c 'git commit -m x'":          true,
		`bash -c "gi''t push origin main"`: true,
		"git add -A && git status":         false,
		"echo 'git commit' > /dev/null":    true, // text check: a quoted commit counts
	} {
		c, _ := Prepare(root, call("bash", map[string]any{"command": cmd}))
		if c.Commit != want {
			t.Errorf("%q commit = %v", cmd, c.Commit)
		}
	}
}

func TestCancelKillsTheWholeCommand(t *testing.T) {
	root := t.TempDir()
	marker := fmt.Sprintf("xhaos-test-%d", time.Now().UnixNano())
	c, _ := Prepare(root, call("bash", map[string]any{"command": "sleep 20 # " + marker + "\ntouch late.txt"}))
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()
	start := time.Now()
	Execute(ctx, root, c)
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("cancel took %s; a child process kept running", d)
	}
	if out, _ := exec.Command("pgrep", "-f", marker).Output(); len(out) > 0 {
		t.Fatalf("command still running: %s", out)
	}
}

func TestClassifyCommand(t *testing.T) {
	cases := map[string]string{
		// read: inspects the project only
		"ls -la":                         Read,
		"git status":                     Read,
		"git log --oneline -5 | head -3": Read,
		"git diff HEAD~1 -- src/a.go":    Read,
		"git branch -a":                  Read,
		"git remote -v":                  Read,
		"grep -rn 'func (s' app | wc -l": Read,
		"find . -name '*.go'":            Read,
		"cat README.md && git show HEAD": Read,
		"git log 2>&1":                   Read,
		"echo 'a > b'":                   Exec, // conservative: any > asks, even quoted
		// output thrown away, or moved between the command's own streams
		"ls missing 2>/dev/null":                    Read,
		"grep -rn x . 2>/dev/null | head -5":        Read,
		"find . -name '*.go' >/dev/null 2>&1 && ls": Read,
		"git status &>/dev/null; git log -1":        Read,
		"cat README.md >> /dev/null":                Read,
		"wc -l < /dev/null":                         Read,
		"echo oops >&2":                             Read,
		"echo hi 1>&2":                              Read,
		"git add -A >/dev/null && git commit -m x":  Git,
		"ls > /dev/null/../../etc/x":                Exec, // not /dev/null itself
		"ls >/dev/nullx":                            Exec,
		"ls 2>/dev/null >out.txt":                   Exec, // a real file too
		"cat < secrets.txt":                         Exec, // input from a file still asks
		"echo hi >&2x":                              Exec, // bash: writes the file "2x"
		"echo hi 2>&1x":                             Exec, // bash: writes the file "1x"
		"echo hi >&out.txt":                         Exec, // >&word is a file when word is not a number
		"echo hi >|/dev/null":                       Exec, // noclobber override: not on the list
		"ls >(cat)":                                 Exec, // process substitution
		"cat <<<hi":                                 Exec, // here-string
		"ls 2>/dev/null>out.txt":                    Exec, // a file after /dev/null with no space
		"ls 2>&1|head":                              Read,
		"ls 2>/dev/null&&ls":                        Read,
		"ls 2>/dev/null&rm -rf build":               Exec, // backgrounded, then rm
		"ls 2>/dev/null; rm -rf build":              Exec,
		// local git changes
		"git add -A && git commit -m \"fix: a; b\"": Git,
		"git switch -c feature/x":                   Git,
		"git switch main":                           Git,
		"git status; git add a.go":                  Git,
		// everything else asks
		"git push":                 Exec,
		"git push origin main":     Exec,
		"git add . && git push":    Exec,
		"git branch -D old":        Exec,
		"git checkout -- .":        Exec,
		"git reset --hard":         Exec,
		"git -C /etc status":       Exec,
		"git -c core.pager=sh log": Exec,
		"git diff --output=x.txt":  Exec,
		"cat /etc/passwd":          Exec,
		"cat ../secret":            Exec,
		"ls ~":                     Exec,
		"echo hi > a.txt":          Exec,
		"cat $(echo /etc/passwd)":  Exec,
		"cat `x`":                  Exec,
		"echo \"$HOME\"":           Exec,
		"FOO=1 git status":         Exec,
		"./script.sh":              Exec,
		"find . -name x -delete":   Exec,
		"find . -exec rm {} ;":     Exec,
		"rm -rf build":             Exec,
		"sleep 1 & ls":             Exec,
		"npm test":                 Exec,
		"ls 'unbalanced":           Exec,
		"git status | sh":          Exec,
	}
	for line, want := range cases {
		if got := ClassifyCommand(line); got != want {
			t.Errorf("ClassifyCommand(%q) = %s, want %s", line, got, want)
		}
	}
}

func TestWritesIntoGitAreRefused(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".git", "hooks"), 0o755)
	if _, err := Prepare(root, call("write_file", map[string]any{"path": ".git/hooks/pre-commit", "content": "#!/bin/sh\n"})); err == nil {
		t.Fatal("a write into .git was accepted")
	}
	if _, err := Prepare(root, call("write_file", map[string]any{"path": ".gitignore", "content": "x\n"})); err != nil {
		t.Fatalf(".gitignore refused: %v", err)
	}
}

func TestAskUserInput(t *testing.T) {
	root := t.TempDir()
	bad := []any{
		map[string]any{},
		map[string]any{"questions": []any{}},
		map[string]any{"questions": []any{map[string]any{"prompt": " "}}},
		map[string]any{"questions": []any{map[string]any{"prompt": "x", "options": []any{map[string]any{"label": "a"}, map[string]any{"label": "a"}}}}},
	}
	for _, in := range bad {
		if _, err := Prepare(root, call("ask_user", in)); err == nil {
			t.Errorf("ask_user %v accepted", in)
		}
	}
	c, err := Prepare(root, call("ask_user", map[string]any{"questions": []any{map[string]any{"prompt": "Name?"}}}))
	if err != nil || c.Effect != Ask || c.Summary != "Name?" {
		t.Fatalf("prepare = %+v, %v", c, err)
	}
	qs, _ := ParseQuestions(c.Preview)
	if out, err := FormatAnswers(qs, map[string]Answer{"q1": {Text: "Ada"}}); err != nil || out != "Q: Name?\nA: Ada" {
		t.Fatalf("format = %q, %v", out, err)
	}
	if _, err := FormatAnswers(qs, map[string]Answer{"q1": {Choices: []string{"o9"}}}); err == nil {
		t.Fatal("an unknown option was accepted")
	}
}

// Agents learn where scratch files go, so logs and downloads stay out of
// their project.
func TestBashTellsAgentsAboutTMPDIR(t *testing.T) {
	for _, s := range Specs() {
		if s.Name == "bash" {
			if !strings.Contains(s.Description, "$TMPDIR") || !strings.Contains(s.Description, "kept between commands") {
				t.Fatalf("bash description does not mention $TMPDIR: %s", s.Description)
			}
			return
		}
	}
	t.Fatal("no bash tool")
}
