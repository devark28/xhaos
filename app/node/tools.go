package node

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/devark28/xhaos/app/agent"
	"github.com/devark28/xhaos/app/llm"
	"github.com/devark28/xhaos/app/node/wire"
	"github.com/devark28/xhaos/app/tool"
)

// Tools are the node_* tools. Root maps an agent's project to its folder.
type Tools struct {
	s    *Service
	Root func(workspaceID string, projectID int64) (string, error)
}

func NewTools(s *Service, root func(string, int64) (string, error)) *Tools {
	return &Tools{s: s, Root: root}
}

const (
	toolList = "nodes_list"
	toolRun  = "node_run"
	toolJob  = "node_job"
	toolCopy = "node_copy"
)

func (t *Tools) Owns(name string) bool {
	switch name {
	case toolList, toolRun, toolJob, toolCopy:
		return true
	}
	return false
}

var (
	str  = map[string]any{"type": "string"}
	num  = map[string]any{"type": "integer"}
	flag = map[string]any{"type": "boolean"}
)

func (t *Tools) Specs(string) []llm.Tool {
	return []llm.Tool{
		{Name: toolList,
			Description: "List the workspace's nodes: other machines (a build PC, Raspberry Pis…) joined to Xhaos, with whether each is online, what it is (OS, arch, model, CPUs, memory), its labels, its jobs, and whether you may use it. A node is granted to an agent by the user on the Nodes tab.",
			Schema:      llm.ToolSchema{Properties: map[string]any{}}},
		{Name: toolRun,
			Description: "Run a shell command on a node granted to you, in its folder (the node's root, e.g. ~/xhaos-work; dir is relative to it). It waits up to `wait` seconds (default and most 120) and returns the exit code and output; a command still running then keeps running as a job: follow it with node_job. Set background to return at once (servers, long builds). The command runs confined to the node's folder (Landlock) with no sudo; hardware such as /dev/video0 only if the node's owner opened it. Each call waits for the user's approval like bash.",
			Schema: llm.ToolSchema{Properties: map[string]any{
				"node": str, "command": str, "dir": str,
				"wait": num, "background": flag,
			}, Required: []string{"node", "command"}}},
		{Name: toolJob,
			Description: "Follow jobs on a node: action list (the node's jobs; default when job is empty), logs (output from offset; the answer says the next offset), wait (up to `wait` seconds, default and most 120, for the job to end, then logs), or stop.",
			Schema: llm.ToolSchema{Properties: map[string]any{
				"node": str, "job": str, "action": map[string]any{"type": "string", "enum": []string{"list", "logs", "wait", "stop"}},
				"offset": num, "wait": num,
			}, Required: []string{"node"}}},
		{Name: toolCopy,
			Description: "Copy a file or folder between your project and a node, or between two nodes. from_node / to_node name the node; leave one empty for your project (paths relative to it), or set both for node to node. Node paths are relative to the node's folder. A folder is copied into `to` (made if missing); from the project, .git and git-ignored files are left out. Copies are streamed through the server, at most 1 GiB, and checked with SHA-256. Copies are gated like file writes.",
			Schema: llm.ToolSchema{Properties: map[string]any{
				"from": str, "from_node": str, "to": str, "to_node": str,
			}, Required: []string{"from", "to"}}},
	}
}

func (t *Tools) Catalog() []tool.Info {
	effects := map[string]string{toolList: tool.Read, toolRun: tool.Exec, toolJob: tool.Read, toolCopy: tool.Write}
	var out []tool.Info
	for _, s := range t.Specs("") {
		out = append(out, tool.Info{Name: s.Name, Description: s.Description, Effect: effects[s.Name], Source: "built-in"})
	}
	return out
}

type runInput struct {
	Node       string `json:"node"`
	Command    string `json:"command"`
	Dir        string `json:"dir"`
	Wait       *int   `json:"wait"`
	Background bool   `json:"background"`
}

type jobInput struct {
	Node   string `json:"node"`
	Job    string `json:"job"`
	Action string `json:"action"`
	Offset int64  `json:"offset"`
	Wait   *int   `json:"wait"`
}

type copyInput struct {
	From     string `json:"from"`
	FromNode string `json:"from_node"`
	To       string `json:"to"`
	ToNode   string `json:"to_node"`
}

func decode(c llm.ToolCall, v any) error {
	raw := c.Input
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid input for %s: %v", c.Name, err)
	}
	return nil
}

// target is a node an agent named, checked.
type target struct {
	id   int64
	name string
}

// usable finds a node by name (or #id) and checks it is granted to a.
func (t *Tools) usable(ws string, a agent.Agent, name string) (target, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return target{}, errors.New("name the node (nodes_list shows them)")
	}
	var (
		n   target
		err error
	)
	if id, perr := strconv.ParseInt(strings.TrimPrefix(name, "#"), 10, 64); perr == nil && strings.HasPrefix(name, "#") {
		err = t.s.db.QueryRow(`SELECT id, name FROM nodes WHERE workspace_id = ? AND id = ?`, ws, id).Scan(&n.id, &n.name)
	} else {
		err = t.s.db.QueryRow(`SELECT id, name FROM nodes WHERE workspace_id = ? AND name = ? COLLATE NOCASE`, ws, name).Scan(&n.id, &n.name)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return n, fmt.Errorf("there is no node %q (nodes_list shows them)", name)
	}
	if err != nil {
		return n, err
	}
	var granted int
	if err := t.s.db.QueryRow(`SELECT COUNT(*) FROM node_grants WHERE node_id = ? AND agent_id = ?`, n.id, a.ID).Scan(&granted); err != nil {
		return n, err
	}
	if granted == 0 {
		return n, fmt.Errorf("node %s is not granted to you: ask the user to grant it on the Nodes tab", n.name)
	}
	if !t.s.hub.Online(n.id) {
		return n, fmt.Errorf("node %s is offline", n.name)
	}
	return n, nil
}

func waitArg(w *int) time.Duration {
	if w == nil {
		return time.Duration(wire.DefaultWait) * time.Millisecond
	}
	return time.Duration(min(max(*w, 0)*1000, wire.MaxWait)) * time.Millisecond
}

func (t *Tools) Prepare(ws string, a agent.Agent, c llm.ToolCall) (tool.Call, error) {
	call := tool.Call{ID: c.ID, Name: c.Name, Effect: tool.Read, Args: c.Input}
	switch c.Name {
	case toolList:
		call.Summary = "nodes"
		return call, nil
	case toolRun:
		var in runInput
		if err := decode(c, &in); err != nil {
			return call, err
		}
		if strings.TrimSpace(in.Command) == "" {
			return call, errors.New("node_run needs a command")
		}
		n, err := t.usable(ws, a, in.Node)
		if err != nil {
			return call, err
		}
		// Read-only commands (the same classifier as bash) run like reads;
		// anything else, git included, is a command.
		if tool.ClassifyCommand(in.Command) != tool.Read {
			call.Effect = tool.Exec
		}
		call.Summary = n.name + ": " + in.Command
		if in.Dir != "" {
			call.Summary = n.name + " (in " + in.Dir + "): " + in.Command
		}
		return call, nil
	case toolJob:
		var in jobInput
		if err := decode(c, &in); err != nil {
			return call, err
		}
		n, err := t.usable(ws, a, in.Node)
		if err != nil {
			return call, err
		}
		switch in.Action {
		case "", "list", "logs", "wait":
		case "stop":
			call.Effect = tool.Exec
		default:
			return call, fmt.Errorf("action is list, logs, wait or stop")
		}
		if in.Job == "" && in.Action != "" && in.Action != "list" {
			return call, fmt.Errorf("%s needs a job (node_job with action list shows them)", in.Action)
		}
		call.Summary = strings.TrimSpace(fmt.Sprintf("%s: %s %s", n.name, firstOf(in.Action, "list"), in.Job))
		return call, nil
	case toolCopy:
		var in copyInput
		if err := decode(c, &in); err != nil {
			return call, err
		}
		if strings.TrimSpace(in.From) == "" || strings.TrimSpace(in.To) == "" {
			return call, errors.New("node_copy needs from and to")
		}
		if in.FromNode == "" && in.ToNode == "" {
			return call, errors.New("name from_node or to_node (or both); to copy inside your project, use bash")
		}
		from, to := "project:"+in.From, "project:"+in.To
		if in.FromNode != "" {
			n, err := t.usable(ws, a, in.FromNode)
			if err != nil {
				return call, err
			}
			from = n.name + ":" + in.From
		}
		if in.ToNode != "" {
			n, err := t.usable(ws, a, in.ToNode)
			if err != nil {
				return call, err
			}
			to = n.name + ":" + in.To
		} else {
			root, err := t.root(a)
			if err != nil {
				return call, err
			}
			if _, err := projectPath(root, in.To); err != nil {
				return call, err
			}
		}
		call.Effect = tool.Write
		call.Summary = "copy " + from + " → " + to
		return call, nil
	}
	return call, fmt.Errorf("unknown tool %q", c.Name)
}

func firstOf(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (t *Tools) root(a agent.Agent) (string, error) {
	if a.ProjectID == 0 || t.Root == nil {
		return "", errors.New("you have no project to copy from or to")
	}
	return t.Root(a.WorkspaceID, a.ProjectID)
}

// projectPath is a path in the project, never in .git.
func projectPath(root, rel string) (string, error) {
	p, err := tool.Resolve(root, rel)
	if err != nil {
		return "", err
	}
	real, _ := filepath.EvalSymlinks(root)
	if r, err := filepath.Rel(real, p); err == nil && (r == ".git" || strings.HasPrefix(filepath.ToSlash(r), ".git/")) {
		return "", errors.New("copies do not reach into .git")
	}
	return p, nil
}

func (t *Tools) Execute(ctx context.Context, ws string, a agent.Agent, call tool.Call) (string, error) {
	c := llm.ToolCall{ID: call.ID, Name: call.Name, Input: call.Args}
	switch call.Name {
	case toolList:
		return t.list(ws, a)
	case toolRun:
		var in runInput
		if err := decode(c, &in); err != nil {
			return "", err
		}
		n, err := t.usable(ws, a, in.Node)
		if err != nil {
			return "", err
		}
		wait := waitArg(in.Wait)
		if in.Background {
			wait = 0
		}
		m, err := t.s.hub.exec(ctx, n.id, in.Command, in.Dir, wait)
		if err != nil {
			return "", fmt.Errorf("%s: %w", n.name, err)
		}
		return describe(n.name, m), nil
	case toolJob:
		var in jobInput
		if err := decode(c, &in); err != nil {
			return "", err
		}
		n, err := t.usable(ws, a, in.Node)
		if err != nil {
			return "", err
		}
		if in.Job == "" {
			m, err := t.s.hub.jobAction(n.id, wire.Msg{})
			if err != nil {
				return "", err
			}
			return listJobs(n.name, m.Jobs), nil
		}
		act := map[string]string{"": wire.ActLogs, "logs": wire.ActLogs, "wait": wire.ActWait, "stop": wire.ActKill, "list": wire.ActLogs}[in.Action]
		m, err := t.s.hub.jobAction(n.id, wire.Msg{JobID: in.Job, Action: act, Offset: in.Offset, Wait: int(waitArg(in.Wait) / time.Millisecond)})
		if err != nil {
			return "", fmt.Errorf("%s: %w", n.name, err)
		}
		return describe(n.name, m), nil
	case toolCopy:
		var in copyInput
		if err := decode(c, &in); err != nil {
			return "", err
		}
		return t.copy(ctx, ws, a, in)
	}
	return "", fmt.Errorf("unknown tool %q", call.Name)
}

func (t *Tools) list(ws string, a agent.Agent) (string, error) {
	list, err := t.s.Nodes(ws)
	if err != nil {
		return "", err
	}
	if len(list.Nodes) == 0 {
		return "There are no nodes in this workspace. The user adds them on the Nodes tab.", nil
	}
	var b strings.Builder
	for _, v := range list.Nodes {
		you := "not granted to you (ask the user to grant it on the Nodes tab)"
		for _, g := range v.Grants {
			if g == a.ID {
				you = "granted"
			}
		}
		fmt.Fprintf(&b, "%s (#%d): %s", v.Name, v.ID, v.State)
		if v.State == Offline && v.LastSeen != nil {
			fmt.Fprintf(&b, ", last seen %s", v.LastSeen.Format(time.RFC3339))
		}
		b.WriteString("\n")
		if len(v.Labels) > 0 {
			fmt.Fprintf(&b, "  labels: %s\n", strings.Join(v.Labels, ", "))
		}
		if f := v.Facts; f != nil {
			what := []string{f.OS + "/" + f.Arch}
			for _, s := range []string{f.Model, f.Distro, f.Kernel} {
				if s != "" {
					what = append(what, s)
				}
			}
			if f.CPUs > 0 {
				what = append(what, fmt.Sprintf("%d CPUs", f.CPUs))
			}
			if f.MemoryMB > 0 {
				what = append(what, fmt.Sprintf("%d MB memory", f.MemoryMB))
			}
			fmt.Fprintf(&b, "  machine: %s (host %s, xhaos-node %s)\n", strings.Join(what, ", "), f.Hostname, f.Version)
			sandbox := "commands run under Landlock, confined to the folder"
			if !f.Sandboxed {
				sandbox = "commands run unconfined (its owner allowed it)"
			}
			fmt.Fprintf(&b, "  folder: %s; %s\n", f.Root, sandbox)
			if len(f.Devices) > 0 {
				fmt.Fprintf(&b, "  devices opened to commands: %s\n", strings.Join(f.Devices, ", "))
			}
			if len(f.Hardware) > 0 {
				fmt.Fprintf(&b, "  hardware seen: %s\n", strings.Join(f.Hardware, ", "))
			}
		}
		running := 0
		for _, j := range v.Jobs {
			if j.Running {
				running++
			}
		}
		if len(v.Jobs) > 0 {
			fmt.Fprintf(&b, "  jobs: %d (%d running)\n", len(v.Jobs), running)
		}
		fmt.Fprintf(&b, "  you: %s\n", you)
	}
	return b.String(), nil
}

func listJobs(node string, jobs []wire.Job) string {
	if len(jobs) == 0 {
		return node + " has no jobs."
	}
	var b strings.Builder
	for _, j := range jobs {
		state := "running"
		if !j.Running {
			state = exitText(j)
		}
		fmt.Fprintf(&b, "%s: %s — %s (started %s, %d bytes of output)\n", j.ID, j.Command, state, time.UnixMilli(j.Started).Format(time.RFC3339), j.Size)
	}
	return b.String()
}

func exitText(j wire.Job) string {
	if j.Signal != "" {
		return "killed by " + j.Signal
	}
	return fmt.Sprintf("exit %d", j.Exit)
}

// describe turns a job answer into the tool's result: state, then output
// (the newest part when long).
func describe(node string, m wire.Msg) string {
	if m.Job == nil {
		return listJobs(node, m.Jobs)
	}
	j := *m.Job
	var b strings.Builder
	if j.Running {
		fmt.Fprintf(&b, "Job %s on %s is still running (follow it with node_job: job %q).\n", j.ID, node, j.ID)
	} else {
		fmt.Fprintf(&b, "Job %s on %s: %s after %s.\n", j.ID, node, exitText(j), time.Duration(j.Ended-j.Started)*time.Millisecond)
	}
	data, from := m.Data, m.Offset
	const max = 30_000
	if len(data) > max {
		from += int64(len(data) - max)
		data = data[len(data)-max:]
	}
	if from > 0 {
		fmt.Fprintf(&b, "(output from byte %d; earlier bytes: node_job logs with a lower offset, the node keeps the newest 10 MiB)\n", from)
	}
	end := m.Offset + int64(len(m.Data))
	if data == "" {
		b.WriteString("(no output)\n")
	} else {
		b.WriteString(data)
		if !strings.HasSuffix(data, "\n") {
			b.WriteString("\n")
		}
	}
	if j.Running {
		fmt.Fprintf(&b, "(next offset: %d)\n", end)
	}
	return b.String()
}

// copy runs a node_copy.
func (t *Tools) copy(ctx context.Context, ws string, a agent.Agent, in copyInput) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Hour)
	defer cancel()
	switch {
	case in.FromNode != "" && in.ToNode != "": // node → node
		src, err := t.usable(ws, a, in.FromNode)
		if err != nil {
			return "", err
		}
		dst, err := t.usable(ws, a, in.ToNode)
		if err != nil {
			return "", err
		}
		d, err := t.s.hub.open(ctx, src.id, in.From)
		if err != nil {
			return "", fmt.Errorf("%s: %w", src.name, err)
		}
		defer d.Close()
		done, err := t.s.hub.put(ctx, dst.id, in.To, d.Meta.Mode, d.Meta.Tar, d)
		if err != nil {
			return "", fmt.Errorf("copying to %s: %w", dst.name, err)
		}
		return copied(src.name+":"+in.From, dst.name+":"+in.To, done.Size, done.SHA256), nil

	case in.ToNode != "": // project → node
		dst, err := t.usable(ws, a, in.ToNode)
		if err != nil {
			return "", err
		}
		root, err := t.root(a)
		if err != nil {
			return "", err
		}
		src, err := projectPath(root, in.From)
		if err != nil {
			return "", err
		}
		fi, err := os.Stat(src)
		if err != nil {
			return "", fmt.Errorf("%s: not found in the project", in.From)
		}
		var (
			r    io.Reader
			mode uint32
		)
		if fi.IsDir() {
			files, err := projectFiles(ctx, src)
			if err != nil {
				return "", err
			}
			pr, pw := io.Pipe()
			go func() { pw.CloseWithError(wire.WriteTar(pw, src, files)) }()
			defer pr.Close()
			r = pr
		} else {
			if !fi.Mode().IsRegular() {
				return "", fmt.Errorf("%s is not a regular file", in.From)
			}
			if fi.Size() > wire.MaxCopy {
				return "", fmt.Errorf("%s is %d MiB, over the copy limit (%d MiB)", in.From, fi.Size()>>20, wire.MaxCopy>>20)
			}
			f, err := os.Open(src)
			if err != nil {
				return "", err
			}
			defer f.Close()
			r, mode = f, uint32(fi.Mode().Perm())
		}
		done, err := t.s.hub.put(ctx, dst.id, in.To, mode, fi.IsDir(), r)
		if err != nil {
			return "", fmt.Errorf("copying to %s: %w", dst.name, err)
		}
		return copied("project:"+in.From, dst.name+":"+in.To, done.Size, done.SHA256), nil

	default: // node → project
		src, err := t.usable(ws, a, in.FromNode)
		if err != nil {
			return "", err
		}
		root, err := t.root(a)
		if err != nil {
			return "", err
		}
		dest, err := projectPath(root, in.To)
		if err != nil {
			return "", err
		}
		d, err := t.s.hub.open(ctx, src.id, in.From)
		if err != nil {
			return "", fmt.Errorf("%s: %w", src.name, err)
		}
		defer d.Close()
		size, err := receive(root, dest, d)
		if err != nil {
			return "", fmt.Errorf("copying from %s: %w", src.name, err)
		}
		return copied(src.name+":"+in.From, "project:"+in.To, size, d.Sum), nil
	}
}

func copied(from, to string, size int64, sum string) string {
	return fmt.Sprintf("Copied %s → %s: %d bytes, SHA-256 %s (checked).", from, to, size, sum)
}

// projectFiles lists a project folder's files for a copy: git's view
// (tracked and untracked, without ignored files) when it is in a git
// repository, else everything but .git.
func projectFiles(ctx context.Context, dir string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", ".")
	out, err := cmd.Output()
	if err != nil {
		return wire.WalkFiles(dir)
	}
	var files []string
	for _, f := range strings.Split(string(out), "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files, nil
}

// receive writes a download into the project: the bytes go to a partial
// file first, and only a whole, checked copy takes the target's place (or
// is unpacked, for a folder).
func receive(root, dest string, d *download) (int64, error) {
	parent := filepath.Dir(dest)
	if d.Meta.Tar {
		parent = dest
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return 0, err
	}
	tmp, err := os.CreateTemp(parent, ".xhaos-partial-*")
	if err != nil {
		return 0, err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	size, err := io.Copy(tmp, d)
	if err != nil {
		return 0, err
	}
	if d.Meta.Tar {
		if _, err := tmp.Seek(0, io.SeekStart); err != nil {
			return 0, err
		}
		_, err := wire.ExtractTar(tmp, dest, func(rel string) (string, error) {
			return projectPath(root, filepath.Join(dest, rel))
		})
		return size, err
	}
	mode := os.FileMode(d.Meta.Mode).Perm()
	if mode == 0 {
		mode = 0o644
	}
	if err := tmp.Chmod(mode); err != nil {
		return 0, err
	}
	if err := tmp.Close(); err != nil {
		return 0, err
	}
	if fi, err := os.Lstat(dest); err == nil && fi.IsDir() {
		return 0, fmt.Errorf("%s is a folder in the project", dest)
	}
	return size, os.Rename(tmp.Name(), dest)
}
