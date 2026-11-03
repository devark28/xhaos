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

	"xhaos/app/llm"
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
	for cmd, want := range map[string]bool{"git commit -m x": true, "cd a && git push": true, "git status": false, "echo commit": false} {
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
