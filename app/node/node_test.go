package node_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/devark28/xhaos/app/agent"
	"github.com/devark28/xhaos/app/backend"
	"github.com/devark28/xhaos/app/llm"
	"github.com/devark28/xhaos/app/node"
	"github.com/devark28/xhaos/app/node/client"
	"github.com/devark28/xhaos/app/sandbox"
	"github.com/devark28/xhaos/app/tool"
)

const ws = "default"

func TestMain(m *testing.M) {
	sandbox.MaybeRun()
	os.Exit(m.Run())
}

type env struct {
	s     *backend.Services
	url   string
	root  string // the agent's project
	agent agent.Agent
	tools *node.Tools
	ended map[string]chan error // each node client's Run result
}

func setup(t *testing.T) *env {
	t.Helper()
	var h http.Handler = http.NotFoundHandler()
	// Every request goes through a writer that holds the status line back,
	// as the real server's (Wails's content type sniffer) does.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.ServeHTTP(&holding{w: w}, r) }))
	t.Cleanup(srv.Close)
	root := t.TempDir()
	s, err := backend.Open(filepath.Join(t.TempDir(), "x.db"), backend.Options{Root: root, DisableRuns: true, NodeURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	h = s.NodeHub
	a, err := s.Agents.Create(agent.CreateRequest{IdempotencyKey: "a", WorkspaceID: ws, Title: "a"})
	if err != nil {
		t.Fatal(err)
	}
	return &env{s: s, url: srv.URL, root: root, agent: a, ended: map[string]chan error{}, tools: node.NewTools(s.Nodes, func(string, int64) (string, error) { return root, nil })}
}

// holding keeps WriteHeader until a Content-Type or a body, like Wails's
// content type sniffer; it can only be hijacked through Unwrap.
type holding struct {
	w      http.ResponseWriter
	status int
}

func (h *holding) Header() http.Header         { return h.w.Header() }
func (h *holding) Unwrap() http.ResponseWriter { return h.w }
func (h *holding) WriteHeader(code int) {
	if h.Header().Get("Content-Type") != "" {
		h.w.WriteHeader(code)
		return
	}
	h.status = code
}
func (h *holding) Write(b []byte) (int, error) {
	if h.status != 0 {
		h.w.WriteHeader(h.status)
		h.status = 0
	}
	return h.w.Write(b)
}

// join adds a node and joins a client to it; the client serves until the
// test ends.
func (e *env) join(t *testing.T, name string) (node.View, string) {
	t.Helper()
	added, err := e.s.Nodes.Add(node.AddRequest{WorkspaceID: ws, Name: name, Labels: []string{"Pi5", "pi5", " cam "}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(added.Command, "--code "+added.Code) || added.Node.State != node.Waiting {
		t.Fatalf("added = %+v", added)
	}
	state, nodeRoot := t.TempDir(), t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	c, err := client.Join(ctx, state, client.Config{Server: e.url, Root: nodeRoot, Unsandboxed: true}, strings.ToLower(added.Code), "test")
	if err != nil {
		t.Fatal(err)
	}
	_, key, err := client.Load(state)
	if err != nil {
		t.Fatal(err)
	}
	n := &client.Node{Config: c, Key: key, Version: "test", State: state}
	ended := make(chan error, 1)
	e.ended[name] = ended
	go func() { ended <- n.Run(ctx) }()
	t.Cleanup(cancel)
	for i := 0; ; i++ {
		v := e.view(t, added.Node.ID)
		if v.State == node.Online {
			return v, nodeRoot
		}
		if i > 200 {
			t.Fatalf("node never came online: %+v", v)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (e *env) view(t *testing.T, id int64) node.View {
	t.Helper()
	list, err := e.s.Nodes.Nodes(ws)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range list.Nodes {
		if v.ID == id {
			return v
		}
	}
	t.Fatalf("no node %d", id)
	return node.View{}
}

func (e *env) call(t *testing.T, name string, in any) (tool.Call, string, error) {
	t.Helper()
	raw, _ := json.Marshal(in)
	c, err := e.tools.Prepare(ws, e.agent, llm.ToolCall{ID: "1", Name: name, Input: raw})
	if err != nil {
		return c, "", err
	}
	out, err := e.tools.Execute(context.Background(), ws, e.agent, c)
	return c, out, err
}

func TestJoinOnceAndCodes(t *testing.T) {
	e := setup(t)
	v, _ := e.join(t, "pi5")
	if strings.Join(v.Labels, ",") != "pi5,cam" || v.Facts == nil || v.Facts.Version != "test" {
		t.Fatalf("view = %+v", v)
	}
	var codes int
	e.s.DB().QueryRow(`SELECT COUNT(*) FROM node_codes`).Scan(&codes)
	if codes != 0 {
		t.Fatalf("%d codes left after joining", codes)
	}
	// Only the hash is stored, and a code works once.
	added, err := e.s.Nodes.Add(node.AddRequest{WorkspaceID: ws, Name: "zero"})
	if err != nil {
		t.Fatal(err)
	}
	var hash string
	e.s.DB().QueryRow(`SELECT hash FROM node_codes`).Scan(&hash)
	if hash == "" || strings.Contains(hash, strings.ReplaceAll(added.Code, "-", "")) {
		t.Fatalf("stored %q for code %s", hash, added.Code)
	}
	if time.Until(added.Expires) > node.CodeTTL || time.Until(added.Expires) < node.CodeTTL-time.Minute {
		t.Fatalf("expires %s", added.Expires)
	}
	if _, err := client.Join(context.Background(), t.TempDir(), client.Config{Server: e.url, Root: t.TempDir()}, "AAAA-BBBB-CCCC", "test"); err == nil || !strings.Contains(err.Error(), "wrong, used or expired") {
		t.Fatalf("wrong code: %v", err)
	}
	if _, err := e.s.Nodes.Add(node.AddRequest{WorkspaceID: ws, Name: "pi5"}); err == nil {
		t.Fatal("two nodes named pi5")
	}
	if _, err := e.s.Nodes.NewCode(node.CodeRequest{WorkspaceID: ws, NodeID: v.ID}); err == nil {
		t.Fatal("new code for a joined node")
	}
}

func TestPiFiles(t *testing.T) {
	e := setup(t)
	added, err := e.s.Nodes.Add(node.AddRequest{WorkspaceID: ws, Name: "pizero", Pi: &node.PiSetup{
		Hostname: "PiZero", SSHKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIB me@laptop", WifiSSID: `Lab "5G"`, WifiPassword: "correct horse", Country: "rw",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(added.Files) != 2 || added.Warning == "" || added.Command != node.FlashCommand || time.Until(added.Expires) < 59*time.Minute {
		t.Fatalf("added = %+v", added)
	}
	user, net := added.Files[0].Content, added.Files[1].Content
	for _, want := range []string{"#cloud-config\n", `hostname: "pizero"`, `name: "pi"`, "lock_passwd: true", "enable_ssh: true", "--code " + added.Code, "--server " + e.url, "&& break"} {
		if !strings.Contains(user, want) {
			t.Errorf("user-data lacks %q:\n%s", want, user)
		}
	}
	for _, want := range []string{`"Lab \"5G\"":`, `password: "correct horse"`, `regulatory-domain: "RW"`} {
		if !strings.Contains(net, want) {
			t.Errorf("network-config lacks %q:\n%s", want, net)
		}
	}
	if _, err := e.s.Nodes.Add(node.AddRequest{WorkspaceID: ws, Name: "bad", Pi: &node.PiSetup{Hostname: "bad_host"}}); err == nil {
		t.Fatal("bad hostname accepted")
	}
}

func TestToolsNeedAGrant(t *testing.T) {
	e := setup(t)
	v, _ := e.join(t, "pi5")
	if _, _, err := e.call(t, "node_run", map[string]any{"node": "pi5", "command": "echo hi"}); err == nil || !strings.Contains(err.Error(), "not granted") {
		t.Fatalf("ungranted run: %v", err)
	}
	if _, err := e.s.Nodes.Grant(node.GrantRequest{WorkspaceID: ws, NodeID: v.ID, AgentID: e.agent.ID, Granted: true}); err != nil {
		t.Fatal(err)
	}
	c, out, err := e.call(t, "node_run", map[string]any{"node": "PI5", "command": "echo hi; echo oops >&2; exit 3"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Effect != tool.Exec || !strings.HasPrefix(c.Summary, "pi5: ") {
		t.Fatalf("call = %+v", c)
	}
	if !strings.Contains(out, "exit 3") || !strings.Contains(out, "hi\n") || !strings.Contains(out, "oops") {
		t.Fatalf("out = %q", out)
	}
	if c, _, _ := e.call(t, "node_run", map[string]any{"node": "pi5", "command": "ls -la"}); c.Effect != tool.Read {
		t.Fatalf("ls is %s", c.Effect)
	}
	_, out, err = e.call(t, "nodes_list", map[string]any{})
	if err != nil || !strings.Contains(out, "pi5 (#") || !strings.Contains(out, "you: granted") {
		t.Fatalf("list = %q, %v", out, err)
	}
}

func TestBackgroundJob(t *testing.T) {
	e := setup(t)
	v, _ := e.join(t, "box")
	e.s.Nodes.Grant(node.GrantRequest{WorkspaceID: ws, NodeID: v.ID, AgentID: e.agent.ID, Granted: true})
	_, out, err := e.call(t, "node_run", map[string]any{"node": "box", "command": "echo start; sleep 30; echo never", "background": true})
	if err != nil || !strings.Contains(out, "still running") {
		t.Fatalf("out = %q, %v", out, err)
	}
	id := out[strings.Index(out, "Job ")+4:]
	id = id[:strings.Index(id, " ")]
	if j := e.view(t, v.ID).Jobs; len(j) != 1 || !j[0].Running {
		// The node reports its jobs; give it a moment.
		time.Sleep(200 * time.Millisecond)
		if j := e.view(t, v.ID).Jobs; len(j) != 1 || !j[0].Running {
			t.Fatalf("jobs = %+v", j)
		}
	}
	for i := 0; !strings.Contains(out, "start"); i++ { // let it print first
		if i > 100 {
			t.Fatalf("no output: %q", out)
		}
		time.Sleep(20 * time.Millisecond)
		_, out, _ = e.call(t, "node_job", map[string]any{"node": "box", "job": id, "action": "logs"})
	}
	c, out, err := e.call(t, "node_job", map[string]any{"node": "box", "job": id, "action": "stop"})
	if err != nil || c.Effect != tool.Exec {
		t.Fatalf("stop: %v %+v", err, c)
	}
	_, out, err = e.call(t, "node_job", map[string]any{"node": "box", "job": id, "action": "wait", "wait": 5})
	if err != nil || !strings.Contains(out, "killed by") || !strings.Contains(out, "start") || strings.Contains(out, "never") {
		t.Fatalf("after stop: %q, %v", out, err)
	}
}

func TestCopies(t *testing.T) {
	e := setup(t)
	a, rootA := e.join(t, "a")
	b, rootB := e.join(t, "b")
	for _, id := range []int64{a.ID, b.ID} {
		e.s.Nodes.Grant(node.GrantRequest{WorkspaceID: ws, NodeID: id, AgentID: e.agent.ID, Granted: true})
	}
	// A file bigger than a chunk, with its mode.
	big := strings.Repeat("0123456789abcdef", 50_000) // 800 kB
	os.WriteFile(filepath.Join(e.root, "tool.sh"), []byte(big), 0o755)
	_, out, err := e.call(t, "node_copy", map[string]any{"from": "tool.sh", "to_node": "a", "to": "bin/tool.sh"})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(big))
	if !strings.Contains(out, hex.EncodeToString(sum[:])) {
		t.Fatalf("out = %q", out)
	}
	got, _ := os.ReadFile(filepath.Join(rootA, "bin", "tool.sh"))
	fi, _ := os.Stat(filepath.Join(rootA, "bin", "tool.sh"))
	if string(got) != big || fi.Mode().Perm() != 0o755 {
		t.Fatalf("node a has %d bytes, mode %v", len(got), fi.Mode())
	}
	// A folder: .git and ignored files stay behind (no git here: only .git).
	os.MkdirAll(filepath.Join(e.root, "app", "sub"), 0o755)
	os.MkdirAll(filepath.Join(e.root, "app", ".git"), 0o755)
	os.WriteFile(filepath.Join(e.root, "app", "main.go"), []byte("package main\n"), 0o644)
	os.WriteFile(filepath.Join(e.root, "app", "sub", "x.txt"), []byte("x"), 0o600)
	os.WriteFile(filepath.Join(e.root, "app", ".git", "HEAD"), []byte("ref"), 0o644)
	if _, _, err := e.call(t, "node_copy", map[string]any{"from": "app", "to_node": "a", "to": "app"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(rootA, "app", "sub", "x.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(rootA, "app", ".git")); err == nil {
		t.Fatal(".git was copied")
	}
	// Node to node, then node to project.
	if _, _, err := e.call(t, "node_copy", map[string]any{"from_node": "a", "from": "app", "to_node": "b", "to": "copy"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(rootB, "copy", "main.go")); string(got) != "package main\n" {
		t.Fatalf("node b: %q", got)
	}
	if _, _, err := e.call(t, "node_copy", map[string]any{"from_node": "a", "from": "bin/tool.sh", "to": "back/tool.sh"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(e.root, "back", "tool.sh")); string(got) != big {
		t.Fatalf("back in the project: %d bytes", len(got))
	}
	// Paths stay inside: the node's folder, the project, never .git.
	if _, _, err := e.call(t, "node_copy", map[string]any{"from": "tool.sh", "to_node": "a", "to": "../escape"}); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("escape on the node: %v", err)
	}
	if _, _, err := e.call(t, "node_copy", map[string]any{"from_node": "a", "from": "bin/tool.sh", "to": "../escape"}); err == nil {
		t.Fatal("escaped the project")
	}
	if _, _, err := e.call(t, "node_copy", map[string]any{"from_node": "a", "from": "bin/tool.sh", "to": ".git/hooks/pre-commit"}); err == nil {
		t.Fatal("wrote into .git")
	}
	if _, _, err := e.call(t, "node_copy", map[string]any{"from_node": "a", "from": "/etc/passwd", "to": "passwd"}); err == nil {
		t.Fatal("read outside the node's folder")
	}
}

func TestRemovedNodeIsRefused(t *testing.T) {
	e := setup(t)
	v, _ := e.join(t, "gone")
	if err := e.s.Nodes.Remove(node.NodeRequest{WorkspaceID: ws, NodeID: v.ID}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-e.ended["gone"]:
		if err == nil || !strings.Contains(err.Error(), "removed") {
			t.Fatalf("the client ended with %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the removed node's client kept running")
	}
	list, _ := e.s.Nodes.Nodes(ws)
	if len(list.Nodes) != 0 || e.s.NodeHub.Online(v.ID) {
		t.Fatalf("still there: %+v", list)
	}
}
