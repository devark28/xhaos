// Package tool defines the tools agents can call and runs them inside a
// workspace root. It does not decide whether a call may run: run asks the
// policy gate first, and only an allowed call reaches Execute.
package tool

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"xhaos/app/llm"
	"xhaos/app/policy"
)

// Effects, from least to most dangerous.
const (
	Read  = policy.EffectRead  // reads inside the root
	Write = policy.EffectWrite // changes files inside the root
	Exec  = policy.EffectExec  // runs a shell command (can do anything the server user can)
)

const (
	maxOutput    = 30_000 // bytes of tool output returned to the model
	maxReadLines = 2000
	maxMatches   = 200
	bashTimeout  = 2 * time.Minute
)

// Call is a validated tool call, ready for the gate and for Execute.
type Call struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Effect  string `json:"effect"`
	Summary string `json:"summary"`           // path or command, for the owner
	Preview string `json:"preview,omitempty"` // diff of a write, shown before approval
	Commit  bool   `json:"commit,omitempty"`  // a git commit or push
	input   input
}

type input struct {
	Path      string  `json:"path"`
	Offset    int     `json:"offset"`
	Limit     int     `json:"limit"`
	Pattern   string  `json:"pattern"`
	Content   *string `json:"content"`
	OldString string  `json:"old_string"`
	NewString string  `json:"new_string"`
	Command   string  `json:"command"`
}

var str = map[string]any{"type": "string"}

// Specs lists the tools, in a fixed order so the prompt prefix stays stable.
func Specs() []llm.Tool {
	return []llm.Tool{
		{Name: "list_dir", Description: "List a directory inside the workspace (one level; directories end with /). Path is relative to the workspace root; empty means the root.",
			Schema: llm.ToolSchema{Properties: map[string]any{"path": str}}},
		{Name: "read_file", Description: "Read a text file inside the workspace. Returns numbered lines. offset is the first line (1-based), limit the number of lines (default 2000).",
			Schema: llm.ToolSchema{Properties: map[string]any{"path": str, "offset": map[string]any{"type": "integer"}, "limit": map[string]any{"type": "integer"}}, Required: []string{"path"}}},
		{Name: "search", Description: "Search file contents inside the workspace with a Go regular expression. Returns path:line: text for each match (at most 200). Skips .git and node_modules. path narrows the search to a directory or file.",
			Schema: llm.ToolSchema{Properties: map[string]any{"pattern": str, "path": str}, Required: []string{"pattern"}}},
		{Name: "write_file", Description: "Create or overwrite a file inside the workspace with content. Parent directories are created. The owner may review it first.",
			Schema: llm.ToolSchema{Properties: map[string]any{"path": str, "content": str}, Required: []string{"path", "content"}}},
		{Name: "edit_file", Description: "Replace old_string with new_string in a file inside the workspace. old_string must occur exactly once; include enough surrounding lines to make it unique. The owner may review it first.",
			Schema: llm.ToolSchema{Properties: map[string]any{"path": str, "old_string": str, "new_string": str}, Required: []string{"path", "old_string", "new_string"}}},
		{Name: "bash", Description: "Run a shell command with bash in the workspace root (2 minute timeout). Returns combined stdout and stderr and the exit code. The owner may review it first.",
			Schema: llm.ToolSchema{Properties: map[string]any{"command": str}, Required: []string{"command"}}},
	}
}

var effects = map[string]string{"list_dir": Read, "read_file": Read, "search": Read, "write_file": Write, "edit_file": Write, "bash": Exec}

// commitRE spots commands that commit or push; policy treats them separately.
var commitRE = regexp.MustCompile(`\bgit\b[^\n;&|]*\b(commit|push)\b`)

// Prepare validates a model's tool call against root. The error is meant for
// the model: it says what was wrong so the next attempt can fix it.
func Prepare(root string, c llm.ToolCall) (Call, error) {
	call := Call{ID: c.ID, Name: c.Name, Effect: effects[c.Name]}
	if call.Effect == "" {
		return call, fmt.Errorf("unknown tool %q", c.Name)
	}
	dec := json.NewDecoder(bytes.NewReader(c.Input))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&call.input); err != nil {
		return call, fmt.Errorf("invalid input for %s: %v", c.Name, err)
	}
	in := &call.input
	need := func(field, v string) error {
		if v == "" {
			return fmt.Errorf("%s needs %s", c.Name, field)
		}
		return nil
	}
	switch c.Name {
	case "bash":
		if err := need("command", strings.TrimSpace(in.Command)); err != nil {
			return call, err
		}
		call.Summary = in.Command
		call.Commit = commitRE.MatchString(in.Command)
		call.Effect = ClassifyCommand(in.Command) // read, git, or exec
		return call, nil
	case "search":
		if err := need("pattern", in.Pattern); err != nil {
			return call, err
		}
		if _, err := regexp.Compile(in.Pattern); err != nil {
			return call, fmt.Errorf("invalid pattern: %v", err)
		}
	case "read_file", "write_file", "edit_file":
		if err := need("path", in.Path); err != nil {
			return call, err
		}
	}
	path, err := Resolve(root, in.Path)
	if err != nil {
		return call, err
	}
	if call.Effect == Write && insideGit(root, path) {
		// Hooks in .git run on commit: a file write must not plant one.
		return call, errors.New("files inside .git cannot be written; use git commands")
	}
	call.Summary = in.Path
	if c.Name == "search" {
		call.Summary = in.Pattern
		if in.Path != "" {
			call.Summary += " in " + in.Path
		}
	}
	switch c.Name {
	case "write_file":
		if in.Content == nil {
			return call, errors.New("write_file needs content")
		}
		old, _ := os.ReadFile(mustResolve(root, in.Path))
		call.Preview = diff(in.Path, string(old), *in.Content)
	case "edit_file":
		if in.OldString == "" {
			return call, errors.New("edit_file needs old_string; use write_file to create a file")
		}
		if in.OldString == in.NewString {
			return call, errors.New("old_string and new_string are the same")
		}
		data, err := os.ReadFile(mustResolve(root, in.Path))
		if err != nil {
			return call, fmt.Errorf("cannot read %s: %v", in.Path, errors.Unwrap(err))
		}
		if _, err := replaceOnce(string(data), in.OldString, in.NewString); err != nil {
			return call, err
		}
		call.Preview = diff(in.Path, in.OldString, in.NewString)
	}
	return call, nil
}

// Execute runs an allowed call. Errors are tool failures to report to the model.
func Execute(ctx context.Context, root string, call Call) (string, error) {
	in := call.input
	switch call.Name {
	case "list_dir":
		return listDir(root, in.Path)
	case "read_file":
		return readFile(root, in.Path, in.Offset, in.Limit)
	case "search":
		return search(ctx, root, in.Path, in.Pattern)
	case "write_file":
		path, err := Resolve(root, in.Path)
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(path, []byte(*in.Content), 0o644); err != nil {
			return "", err
		}
		return fmt.Sprintf("Wrote %s (%d bytes).", in.Path, len(*in.Content)), nil
	case "edit_file":
		path, err := Resolve(root, in.Path)
		if err != nil {
			return "", err
		}
		info, err := os.Stat(path)
		if err != nil {
			return "", err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		// The file may have changed since approval; check again.
		out, err := replaceOnce(string(data), in.OldString, in.NewString)
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(path, []byte(out), info.Mode().Perm()); err != nil {
			return "", err
		}
		return "Edited " + in.Path + ".", nil
	case "bash":
		return bash(ctx, root, in.Command)
	}
	return "", fmt.Errorf("unknown tool %q", call.Name)
}

// Resolve maps a workspace-relative path to an absolute one and rejects any
// path that leaves root, including through symlinks.
func Resolve(root, rel string) (string, error) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("workspace root: %w", err)
	}
	path := rel
	if !filepath.IsAbs(path) {
		path = filepath.Join(realRoot, rel)
	}
	path = filepath.Clean(path)
	// Resolve symlinks in the longest existing prefix; the rest is created later.
	existing, rest := path, ""
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			break
		}
		rest = filepath.Join(filepath.Base(existing), rest)
		existing = parent
	}
	if real, err := filepath.EvalSymlinks(existing); err == nil {
		path = filepath.Join(real, rest)
	}
	if path != realRoot && !strings.HasPrefix(path, realRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is outside the workspace", rel)
	}
	return path, nil
}

// insideGit reports whether path is in the project's .git directory.
func insideGit(root, path string) bool {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(realRoot, path)
	if err != nil {
		return false
	}
	first := strings.SplitN(filepath.ToSlash(rel), "/", 2)[0]
	return first == ".git"
}

func mustResolve(root, rel string) string { p, _ := Resolve(root, rel); return p }

func replaceOnce(text, old, new string) (string, error) {
	switch n := strings.Count(text, old); n {
	case 1:
		return strings.Replace(text, old, new, 1), nil
	case 0:
		return "", errors.New("old_string was not found; read the file again and copy it exactly")
	default:
		return "", fmt.Errorf("old_string occurs %d times; include more surrounding lines", n)
	}
}

func listDir(root, rel string) (string, error) {
	path, err := Resolve(root, rel)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, e := range entries {
		b.WriteString(e.Name())
		if e.IsDir() {
			b.WriteString("/")
		}
		b.WriteString("\n")
	}
	if b.Len() == 0 {
		return "(empty directory)", nil
	}
	return clip(b.String()), nil
}

func readFile(root, rel string, offset, limit int) (string, error) {
	path, err := Resolve(root, rel)
	if err != nil {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if offset < 1 {
		offset = 1
	}
	if limit <= 0 || limit > maxReadLines {
		limit = maxReadLines
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	var b strings.Builder
	n, shown := 0, 0
	for sc.Scan() {
		n++
		if n < offset || shown >= limit {
			continue
		}
		if bytes.IndexByte(sc.Bytes(), 0) >= 0 {
			return "", fmt.Errorf("%s looks like a binary file", rel)
		}
		fmt.Fprintf(&b, "%6d\t%s\n", n, sc.Text())
		shown++
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	if n > offset-1+shown {
		fmt.Fprintf(&b, "… %d lines total; pass offset to read more.\n", n)
	}
	if b.Len() == 0 {
		return "(empty file)", nil
	}
	return clip(b.String()), nil
}

func search(ctx context.Context, root, rel, pattern string) (string, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", err
	}
	base, err := Resolve(root, rel)
	if err != nil {
		return "", err
	}
	realRoot, _ := filepath.EvalSymlinks(root)
	var hits []string
	errStop := errors.New("stop")
	err = filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil || ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			if name := d.Name(); path != base && (name == ".git" || name == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil || bytes.IndexByte(data, 0) >= 0 {
			return nil // unreadable or binary
		}
		relPath, _ := filepath.Rel(realRoot, path)
		for i, line := range strings.Split(string(data), "\n") {
			if re.MatchString(line) {
				if len(line) > 300 {
					line = line[:300] + "…"
				}
				hits = append(hits, fmt.Sprintf("%s:%d: %s", relPath, i+1, line))
				if len(hits) >= maxMatches {
					return errStop
				}
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, errStop) {
		return "", err
	}
	if len(hits) == 0 {
		return "No matches.", nil
	}
	out := strings.Join(hits, "\n")
	if len(hits) >= maxMatches {
		out += fmt.Sprintf("\n… stopped at %d matches; narrow the pattern or path.", maxMatches)
	}
	return clip(out), nil
}

func bash(ctx context.Context, root, command string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, bashTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir = root
	// Own process group, so stopping kills the command's children too
	// (otherwise `sleep` in `sleep 20; make` outlives a stop).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	text := clip(out.String())
	var exit *exec.ExitError
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		return text, fmt.Errorf("timed out after %s\n%s", bashTimeout, text)
	case errors.As(err, &exit):
		return text, fmt.Errorf("exit code %d\n%s", exit.ExitCode(), text)
	case err != nil:
		return text, err
	}
	if text == "" {
		return "(no output, exit code 0)", nil
	}
	return text, nil
}

func clip(s string) string {
	if len(s) <= maxOutput {
		return s
	}
	return s[:maxOutput] + fmt.Sprintf("\n… output cut at %d of %d bytes.", maxOutput, len(s))
}

// diff is a minimal before/after view: removed lines, then added lines.
func diff(path, before, after string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "--- %s\n+++ %s\n", path, path)
	lines := func(s string) []string {
		if s == "" {
			return nil
		}
		return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	}
	for _, l := range lines(before) {
		b.WriteString("-" + l + "\n")
	}
	for _, l := range lines(after) {
		b.WriteString("+" + l + "\n")
	}
	return clip(b.String())
}
