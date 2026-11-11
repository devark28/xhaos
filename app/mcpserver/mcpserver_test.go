package mcpserver_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/devark28/xhaos/app/agent"
	"github.com/devark28/xhaos/app/auth"
	"github.com/devark28/xhaos/app/backend"
	"github.com/devark28/xhaos/app/egress"
	"github.com/devark28/xhaos/app/llm"
	"github.com/devark28/xhaos/app/llm/sim"
	"github.com/devark28/xhaos/app/mcpserver"
	"github.com/devark28/xhaos/app/provider"
	"github.com/devark28/xhaos/app/sandbox"
	"github.com/devark28/xhaos/app/workspace"
)

const ws = workspace.DefaultID

// The test binary doubles as the sandbox helper, like the server binary.
func TestMain(m *testing.M) {
	sandbox.MaybeRun()
	egress.MaybeConnect()
	os.Exit(m.Run())
}

type env struct {
	s    *backend.Services
	root string
	url  string // the endpoint, through the sign-in middleware
}

// setup opens a server on a project folder with the simulated model and
// serves its page routes (the sign-in middleware included) on a test
// server. runs false keeps agents from taking turns (calls stay as set).
func setup(t *testing.T, runs bool, passphrase bool) *env {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := backend.Open(filepath.Join(t.TempDir(), "x.db"), backend.Options{Root: root, Adapters: []llm.Adapter{sim.Adapter{}}, DisableRuns: !runs, Passphrase: passphrase})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	s.MCPEndpoint.Poll = 20 * time.Millisecond
	var h http.Handler = s.MCPEndpoint
	if s.Vault != nil {
		page := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "page", http.StatusTeapot) })
		h = s.Vault.Middleware(http.NotFoundHandler(), nil, nil, s.MCPEndpoint)(page)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	if _, err := s.Providers.Save(provider.SaveRequest{WorkspaceID: ws, ID: "sim", Kind: sim.Kind, Models: []string{"scripted-fast"}}); err != nil && !passphrase {
		t.Fatal(err)
	}
	return &env{s: s, root: root, url: srv.URL + "/mcp"}
}

func (e *env) token(t *testing.T, name string, scopes ...string) string {
	t.Helper()
	issued, err := e.s.MCPTokens.Create(mcpserver.CreateRequest{WorkspaceID: ws, Name: name, Scopes: scopes, Passphrase: "correct horse battery"})
	if err != nil {
		t.Fatal(err)
	}
	return issued.Secret
}

type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

// connect is a real MCP client session (the SDK's Streamable HTTP client).
func (e *env) connect(t *testing.T, token string) *sdk.ClientSession {
	t.Helper()
	client := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(context.Background(), &sdk.StreamableClientTransport{Endpoint: e.url, HTTPClient: &http.Client{Transport: bearer{token}}, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

// call calls a tool and returns its text and whether it was an error.
func call(t *testing.T, cs *sdk.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String(), res.IsError
}

// ok calls a tool that must succeed and decodes its JSON.
func ok(t *testing.T, cs *sdk.ClientSession, name string, args map[string]any) map[string]any {
	t.Helper()
	text, isErr := call(t, cs, name, args)
	if isErr {
		t.Fatalf("%s(%v) failed: %s", name, args, text)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("%s: %v in %s", name, err, text)
	}
	return out
}

func toolNames(t *testing.T, cs *sdk.ClientSession) []string {
	t.Helper()
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range res.Tools {
		names = append(names, tl.Name)
	}
	return names
}

// rpc posts one raw JSON-RPC message and returns the status and body.
func rpc(t *testing.T, url, token, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

const initialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"curl","version":"1"}}}`

func TestTokensAreRequiredAndRevocable(t *testing.T) {
	e := setup(t, false, false)
	tok := e.token(t, "Claude Code", mcpserver.ScopeRead)
	if !strings.HasPrefix(tok, mcpserver.TokenPrefix) {
		t.Fatalf("token %q", tok)
	}
	if code, body := rpc(t, e.url, "", initialize); code != http.StatusUnauthorized || !strings.Contains(body, "MCP token") {
		t.Fatalf("no token: %d %s", code, body)
	}
	if code, _ := rpc(t, e.url, tok+"x", initialize); code != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", code)
	}
	if code, body := rpc(t, e.url, tok, initialize); code != http.StatusOK || !strings.Contains(body, `"name":"xhaos"`) {
		t.Fatalf("initialize: %d %s", code, body)
	}
	list, _ := e.s.MCPTokens.Tokens(ws)
	if len(list) != 1 || list[0].Name != "Claude Code" || list[0].LastUsedAt == nil || strings.Join(list[0].Scopes, " ") != "read" {
		t.Fatalf("tokens = %+v", list)
	}
	// Only the hash is stored.
	var stored string
	e.s.DB().QueryRow(`SELECT hash FROM mcp_tokens`).Scan(&stored)
	if stored == tok || strings.Contains(stored, tok[4:]) {
		t.Fatal("the token is stored in the clear")
	}
	if err := e.s.MCPTokens.Revoke(mcpserver.RevokeRequest{WorkspaceID: ws, ID: list[0].ID}); err != nil {
		t.Fatal(err)
	}
	if code, _ := rpc(t, e.url, tok, initialize); code != http.StatusUnauthorized {
		t.Fatalf("revoked token: %d", code)
	}
	// Wrong tokens are slowed down per address, like wrong passphrases.
	limited := false
	for i := 0; i < 6 && !limited; i++ {
		code, _ := rpc(t, e.url, "xhm_nope", initialize)
		limited = code == http.StatusTooManyRequests
	}
	if !limited {
		t.Fatal("wrong tokens were never rate-limited")
	}
	// Names are unique; scopes must be known.
	if _, err := e.s.MCPTokens.Create(mcpserver.CreateRequest{WorkspaceID: ws, Name: "x", Scopes: []string{"admin"}}); err == nil {
		t.Fatal("an unknown scope was accepted")
	}
	if _, err := e.s.MCPTokens.Create(mcpserver.CreateRequest{WorkspaceID: ws, Name: "x"}); err == nil {
		t.Fatal("a token without scopes was accepted")
	}
}

func TestProtocolBasics(t *testing.T) {
	e := setup(t, false, false)
	tok := e.token(t, "raw", mcpserver.ScopeRead)
	code, body := rpc(t, e.url, tok, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if code != http.StatusOK || !strings.Contains(body, `"list_agents"`) || !strings.Contains(body, `"inputSchema"`) {
		t.Fatalf("tools/list: %d %s", code, body)
	}
	code, body = rpc(t, e.url, tok, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_agents","arguments":{}}}`)
	if code != http.StatusOK || !strings.Contains(body, `\"agents\"`) {
		t.Fatalf("tools/call: %d %s", code, body)
	}
	_, body = rpc(t, e.url, tok, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"create_agent","arguments":{"task":"x"}}}`)
	if !strings.Contains(body, `"error"`) {
		t.Fatalf("a tool outside the token's scopes ran: %s", body)
	}
	if code, body = rpc(t, e.url, tok, `{"jsonrpc":"2.0","id":5,"method":"no/such"}`); code == http.StatusOK && !strings.Contains(body, `"error"`) {
		t.Fatalf("unknown method: %d %s", code, body)
	}
	if code, _ = rpc(t, e.url, tok, `{not json`); code < 400 {
		t.Fatalf("bad JSON: %d", code)
	}
	// The canonical path works too.
	if code, _ := rpc(t, strings.TrimSuffix(e.url, "/mcp")+"/xhaos/mcp", tok, initialize); code != http.StatusOK {
		t.Fatalf("/xhaos/mcp: %d", code)
	}
}

func TestScopesDecideTheTools(t *testing.T) {
	e := setup(t, false, false)
	readOnly := e.connect(t, e.token(t, "reader", mcpserver.ScopeRead))
	names := strings.Join(toolNames(t, readOnly), " ")
	for _, want := range []string{"list_agents", "get_agent", "list_questions", "wait_for_events", "list_channels", "read_channel"} {
		if !strings.Contains(names, want) {
			t.Errorf("read scope lacks %s: %s", want, names)
		}
	}
	for _, not := range []string{"create_agent", "send_message", "answer_question", "post_channel"} {
		if strings.Contains(names, not) {
			t.Errorf("read scope has %s", not)
		}
	}
	if _, err := readOnly.CallTool(context.Background(), &sdk.CallToolParams{Name: "send_message", Arguments: map[string]any{"agent": 1, "body": "x"}}); err == nil {
		t.Error("read scope sent a message")
	}
	all := e.connect(t, e.token(t, "all", mcpserver.Scopes...))
	if n := len(toolNames(t, all)); n != 15 {
		t.Errorf("all scopes: %d tools", n)
	}
	answerOnly := e.connect(t, e.token(t, "answers", mcpserver.ScopeAnswer))
	if names := toolNames(t, answerOnly); strings.Join(names, " ") != "answer_question" {
		t.Errorf("answer scope: %v", names)
	}
}

func TestLockedWorkspaceRefusesTools(t *testing.T) {
	e := setup(t, false, true)
	if _, err := e.s.Vault.Setup("Home", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	tok := e.token(t, "cc", mcpserver.ScopeRead)
	cs := e.connect(t, tok)
	ok(t, cs, "list_agents", nil)
	e.s.Vault.Lock(ws)
	if e.s.Vault.State(ws) != auth.StateLocked {
		t.Fatal("not locked")
	}
	text, isErr := call(t, cs, "list_agents", nil)
	if !isErr || !strings.Contains(text, "server locked: the owner must sign in") {
		t.Fatalf("locked: %v %s", isErr, text)
	}
	// Signing in again brings the tools back; a token needs the passphrase.
	if _, err := e.s.Vault.SignIn(ws, "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	ok(t, cs, "list_agents", nil)
	if _, err := e.s.MCPTokens.Create(mcpserver.CreateRequest{WorkspaceID: ws, Name: "y", Scopes: []string{"read"}, Passphrase: "wrong passphrase!"}); err == nil {
		t.Fatal("a token was created with a wrong passphrase")
	}
}

// waitFor polls a read tool until cond holds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestDriveAnswerAndFollowAnAgent(t *testing.T) {
	e := setup(t, true, false)
	cs := e.connect(t, e.token(t, "Claude Code", mcpserver.ScopeRead, mcpserver.ScopeDrive, mcpserver.ScopeAnswer))
	start := ok(t, cs, "wait_for_events", nil)
	seq := start["seq"].(float64)

	models := ok(t, cs, "list_models", nil)
	if d := models["default"].(map[string]any); d["provider"] != "sim" || d["model"] != "scripted-fast" {
		t.Fatalf("default model: %v", models)
	}
	projects := ok(t, cs, "list_projects", nil)["projects"].([]any)
	if len(projects) != 1 {
		t.Fatalf("projects: %v", projects)
	}
	if text, isErr := call(t, cs, "create_agent", map[string]any{"task": "x", "commit": "allowed"}); !isErr || !strings.Contains(text, "owner's choice") {
		t.Fatalf("commit allowed: %s", text)
	}
	created := ok(t, cs, "create_agent", map[string]any{"task": "Please ask me which approach to take", "title": "Asker", "project": projects[0].(map[string]any)["name"]})
	n := created["agent"].(map[string]any)["number"].(float64)

	// The question reaches list_questions under its agent, with its options.
	var q map[string]any
	waitFor(t, "the question", func() bool {
		qs := ok(t, cs, "list_questions", nil)["questions"].([]any)
		if len(qs) == 1 {
			q = qs[0].(map[string]any)
		}
		return q != nil
	})
	if q["agent"].(float64) != n || q["agent_title"] != "Asker" || q["kind"] != "question" || q["you_can_answer"] != true {
		t.Fatalf("question = %v", q)
	}
	if !strings.Contains(mustJSON(q), "The thorough one") {
		t.Fatalf("options missing: %v", q)
	}
	// wait_for_events saw the agent start and ask.
	ev := ok(t, cs, "wait_for_events", map[string]any{"since": seq, "timeout_seconds": 5})
	if !strings.Contains(mustJSON(ev), `"kind":"question"`) && !strings.Contains(mustJSON(ev), `"kind":"agent_created"`) {
		t.Fatalf("events: %v", ev)
	}
	got := ok(t, cs, "get_agent", map[string]any{"agent": n})
	if len(got["pending"].([]any)) != 1 || !strings.Contains(mustJSON(got["recent"]), "Please ask me") {
		t.Fatalf("get_agent: %v", got)
	}
	// A wrong label is refused with the options; a right one answers.
	if text, isErr := call(t, cs, "answer_question", map[string]any{"question_id": q["id"], "choice": "the medium one"}); !isErr || !strings.Contains(text, "The small change") {
		t.Fatalf("bad choice: %s", text)
	}
	ok(t, cs, "answer_question", map[string]any{"question_id": q["id"], "choice": "the thorough one", "text": "take your time"})
	waitFor(t, "completion", func() bool {
		a := ok(t, cs, "get_agent", map[string]any{"agent": n})["agent"].(map[string]any)
		return a["status"] == agent.StatusComplete
	})
	// The page sees who answered, and who sent the task.
	states, _ := agentStore(e).Tools(ws, int64(n))
	for _, d := range states {
		if d.Name == "ask_user" && (d.Via != "Claude Code" || !strings.Contains(d.Output, "The thorough one")) {
			t.Fatalf("answer = %+v", d)
		}
	}
	msgs, _ := agentStore(e).Messages(ws, int64(n))
	if msgs[0].From != agent.FromOwner || msgs[0].Via != "Claude Code" {
		t.Fatalf("first message = %+v", msgs[0])
	}

	// send_message, stop and start.
	ok(t, cs, "send_message", map[string]any{"agent": n, "body": "One more look, please."})
	waitFor(t, "the second reply", func() bool {
		m, _ := agentStore(e).Messages(ws, int64(n))
		return len(m) > len(msgs)+1 && m[len(m)-1].From == agent.FromAgent
	})
	ok(t, cs, "stop_agent", map[string]any{"agent": n, "terminate": true})
	if text, isErr := call(t, cs, "send_message", map[string]any{"agent": n, "body": "hi"}); !isErr || !strings.Contains(text, "start_agent") {
		t.Fatalf("message to terminated: %s", text)
	}
	ok(t, cs, "start_agent", map[string]any{"agent": n})
	all, _ := agentStore(e).Messages(ws, int64(n))
	notes := 0
	for _, m := range all {
		if m.From == agent.FromSystem && strings.Contains(m.Body, `through MCP by "Claude Code"`) {
			notes++
		}
	}
	if notes != 2 {
		t.Fatalf("expected terminate and restore notes, got %d", notes)
	}
}

func TestChannelsThroughMCP(t *testing.T) {
	e := setup(t, true, false)
	cs := e.connect(t, e.token(t, "Claude Code", mcpserver.ScopeRead, mcpserver.ScopeDrive))
	created := ok(t, cs, "create_agent", map[string]any{"task": "Look around", "title": "Helper"})
	n := created["agent"].(map[string]any)["number"].(float64)
	waitFor(t, "first turn", func() bool {
		return ok(t, cs, "get_agent", map[string]any{"agent": n})["agent"].(map[string]any)["status"] == agent.StatusComplete
	})
	opened := ok(t, cs, "open_channel", map[string]any{"agent": n, "subject": "status", "message": "How is it going?"})
	ch := opened["channel"].(map[string]any)["id"].(float64)
	if opened["message"].(map[string]any)["from"] != "mcp: Claude Code" {
		t.Fatalf("opened = %v", opened)
	}
	list := ok(t, cs, "list_channels", nil)["channels"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["messages"].(float64) != 1 {
		t.Fatalf("channels = %v", list)
	}
	// The agent got it, marked as from an MCP client acting for the user.
	waitFor(t, "delivery", func() bool {
		m, _ := agentStore(e).Messages(ws, int64(n))
		for _, x := range m {
			if x.From == agent.FromChannel && strings.Contains(x.Body, `message from "Claude Code", an MCP client acting for the user`) {
				return true
			}
		}
		return false
	})
	ok(t, cs, "post_channel", map[string]any{"channel": ch, "body": "Second note"})
	read := ok(t, cs, "read_channel", map[string]any{"channel": ch, "after": 1})
	msgs := read["messages"].([]any)
	if len(msgs) != 1 || msgs[0].(map[string]any)["text"] != "Second note" || read["next_after"].(float64) != 2 {
		t.Fatalf("read = %v", read)
	}
	// A wait returns when nothing comes, after the wait.
	t0 := time.Now()
	read = ok(t, cs, "read_channel", map[string]any{"channel": ch, "after": 2, "wait_seconds": 1})
	if len(read["messages"].([]any)) != 0 || time.Since(t0) < 900*time.Millisecond {
		t.Fatalf("waited read = %v after %s", read, time.Since(t0))
	}
	// wait_for_events holds until something happens, then returns it.
	since := ok(t, cs, "wait_for_events", nil)["seq"]
	got := make(chan string, 1)
	go func() {
		res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: "wait_for_events", Arguments: map[string]any{"since": since, "timeout_seconds": 20}})
		if err != nil || res.IsError {
			got <- "error"
			return
		}
		got <- res.Content[0].(*sdk.TextContent).Text
	}()
	time.Sleep(200 * time.Millisecond)
	t0 = time.Now()
	ok(t, cs, "post_channel", map[string]any{"channel": ch, "body": "Wake up"})
	select {
	case text := <-got:
		if !strings.Contains(text, `"kind": "channel_message"`) || !strings.Contains(text, "Wake up") || time.Since(t0) > 5*time.Second {
			t.Fatalf("wait_for_events = %s", text)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("wait_for_events did not return on a new message")
	}
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func agentStore(e *env) *agent.Store { return agent.NewStore(e.s.DB(), nil) }

// pendingCommit makes agent #id wait for a commit or push command.
func pendingCommit(t *testing.T, e *env, id int64, callID, command string, secret bool) {
	t.Helper()
	st := agentStore(e)
	d := agent.ToolData{CallID: callID, Name: "bash", Effect: "git", Summary: command, Commit: true, State: agent.ToolPending, Reason: "commits and pushes need review"}
	if secret {
		d.Effect = "secret"
	}
	if err := st.RecordTool(ws, id, d); err != nil {
		t.Fatal(err)
	}
	if err := st.SetStatus(ws, id, agent.StatusAttention); err != nil {
		t.Fatal(err)
	}
}

func gitHead(t *testing.T, dir, branch string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/"+branch+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCommitApprovalsStayOnFeatureBranches(t *testing.T) {
	e := setup(t, false, false)
	gitHead(t, e.root, "main")
	if err := os.MkdirAll(filepath.Join(e.root, "wt"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitHead(t, filepath.Join(e.root, "wt"), "feat/login")
	a, err := e.s.Agents.Create(agent.CreateRequest{IdempotencyKey: "a", WorkspaceID: ws, Title: "Committer", Provider: "sim", Model: "scripted-fast"})
	if err != nil {
		t.Fatal(err)
	}
	approver := e.connect(t, e.token(t, "approver", mcpserver.ScopeRead, mcpserver.ScopeApprove))
	answerer := e.connect(t, e.token(t, "answerer", mcpserver.ScopeRead, mcpserver.ScopeAnswer))

	cases := []struct {
		command string
		secret  bool
		allowed bool
	}{
		{"cd wt && git add -A && git commit -m 'Add login'", false, true},
		{"git -C wt commit -m wip", false, true},
		{"git push -u origin feat/login", false, true},
		{"git push origin HEAD:feat/login", false, true},
		{"git commit -m 'on main'", false, false},                      // the project's checkout is on main
		{"git push origin main", false, false},                         // protected
		{"git push origin integration", false, false},                  // protected
		{"git push origin release/1.2", false, false},                  // protected
		{"git push origin v1.0:refs/tags/v1.0", false, false},          // a tag
		{"git push --tags origin feat/login", false, false},            // tags
		{"git push --force origin feat/login", false, false},           // force
		{"git push origin +feat/login", false, false},                  // force refspec
		{"git push origin :feat/login", false, false},                  // delete
		{"git push origin", false, false},                              // unnamed branch
		{"cd wt && git commit -m x && rm -rf ..", false, false},        // more than git
		{"cd wt && git commit -m \"$(whoami)\"", false, false},         // substitution
		{"cd wt && git commit -m x > /tmp/out", false, false},          // redirection
		{"git -c core.hooksPath=/tmp -C wt commit -m x", false, false}, // git options
		{"cd .. && git commit -m x", false, false},                     // outside the project
		{"cd wt && git commit -m x", true, false},                      // untrusted secrets
		{"cd wt && git tag v1 && git push origin feat/login", false, false},
	}
	for i, c := range cases {
		callID := "c" + string(rune('a'+i))
		pendingCommit(t, e, a.ID, callID, c.command, c.secret)
		qid := mustQID(a.ID, callID)
		// list_questions says whether it can be answered, and why not.
		var item map[string]any
		for _, q := range ok(t, approver, "list_questions", nil)["questions"].([]any) {
			if q.(map[string]any)["id"] == qid {
				item = q.(map[string]any)
			}
		}
		if item == nil || item["kind"] != "commit" || item["you_can_answer"] != c.allowed {
			t.Errorf("%q listed as %v", c.command, item)
		}
		// The answer scope alone never decides commits.
		if text, isErr := call(t, answerer, "answer_question", map[string]any{"question_id": qid, "decision": "allow"}); !isErr || !strings.Contains(text, "approve-feature-branch") {
			t.Errorf("%q: answer scope: %s", c.command, text)
		}
		text, isErr := call(t, approver, "answer_question", map[string]any{"question_id": qid, "decision": "allow"})
		if isErr == c.allowed {
			t.Errorf("%q: allowed=%v, got error=%v: %s", c.command, c.allowed, isErr, text)
			continue
		}
		if !c.allowed {
			// The approver can still deny it.
			ok(t, approver, "answer_question", map[string]any{"question_id": qid, "decision": "deny"})
		}
		states, _ := agentStore(e).Tools(ws, a.ID)
		want := agent.ToolDenied
		if c.allowed {
			want = agent.ToolAllowed
		}
		if d := states[callID]; d.State != want || d.Via != "approver" {
			t.Errorf("%q: state %+v", c.command, d)
		}
	}
	// Other approvals stay the owner's.
	if err := agentStore(e).RecordTool(ws, a.ID, agent.ToolData{CallID: "w", Name: "write_file", Effect: "write", Summary: "x.go", State: agent.ToolPending}); err != nil {
		t.Fatal(err)
	}
	if text, isErr := call(t, approver, "answer_question", map[string]any{"question_id": mustQID(a.ID, "w"), "decision": "allow"}); !isErr || !strings.Contains(text, "only the owner") {
		t.Fatalf("write approval: %s", text)
	}
}

func mustQID(agentID int64, callID string) string {
	return strconv.FormatInt(agentID, 10) + "/" + callID
}
