package run_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"xhaos/app/attachment"
	"xhaos/app/memory"

	"xhaos/app/agent"
	"xhaos/app/backend"
	"xhaos/app/btw"
	"xhaos/app/eventlog"
	"xhaos/app/llm"
	"xhaos/app/llm/openai"
	"xhaos/app/mcp"
	"xhaos/app/policy"
	"xhaos/app/project"
	"xhaos/app/provider"
	"xhaos/app/run"
	"xhaos/app/tool"
	"xhaos/app/workspace"
)

const ws = workspace.DefaultID

// fake is a scripted model adapter that records the turns it receives.
type fake struct {
	mu    sync.Mutex
	turns []llm.Turn
	reply func(llm.Turn) (llm.Result, error)
}

func (*fake) Kind() string { return "fake" }
func (f *fake) Stream(_ context.Context, _ llm.Credentials, t llm.Turn, onDelta func(string)) (llm.Result, error) {
	f.mu.Lock()
	f.turns = append(f.turns, t)
	f.mu.Unlock()
	res, err := f.reply(t)
	if err == nil {
		onDelta(res.Text)
	}
	return res, err
}
func (*fake) Models(context.Context, llm.Credentials) ([]string, error) { return []string{"m1"}, nil }

type harness struct {
	s      *backend.Services
	fake   *fake
	mu     sync.Mutex
	events []eventlog.Event
	deltas []run.Delta
}

func setup(t *testing.T, reply func(llm.Turn) (llm.Result, error)) *harness {
	return setupIn(t, t.TempDir(), reply)
}

func setupIn(t *testing.T, root string, reply func(llm.Turn) (llm.Result, error)) *harness {
	t.Helper()
	h := &harness{fake: &fake{reply: reply}}
	s, err := backend.Open(filepath.Join(t.TempDir(), "x.db"), backend.Options{
		Root:     root,
		Adapters: []llm.Adapter{h.fake},
		Publish:  func(e eventlog.Event) { h.mu.Lock(); h.events = append(h.events, e); h.mu.Unlock() },
		Notify:   func(d run.Delta) { h.mu.Lock(); h.deltas = append(h.deltas, d); h.mu.Unlock() },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	h.s = s
	return h
}

// waitStatus waits for the agent's latest status event to be want.
func (h *harness) waitStatus(t *testing.T, id int64, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		last := ""
		for _, e := range h.events {
			if e.AgentID == id && e.Kind == agent.EventStatus {
				last = string(e.Data)
			}
		}
		h.mu.Unlock()
		if strings.Contains(last, `"`+want+`"`) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("agent #%d never reached status %q", id, want)
}

func transcript(t *testing.T, h *harness) []string {
	snap, err := h.s.Agents.Snapshot(ws)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range snap.Transcript {
		if e.Kind == agent.EventMessage {
			out = append(out, string(e.Data))
		}
	}
	return out
}

func TestTurnStreamsReplyAndReplaysHistory(t *testing.T) {
	h := setup(t, func(turn llm.Turn) (llm.Result, error) {
		return llm.Result{Text: "reply " + turn.Messages[len(turn.Messages)-1].Text, StopReason: "end_turn", Raw: []byte(`{"native":true}`)}, nil
	})
	if _, err := h.s.Providers.Save(provider.SaveRequest{ID: "f", Kind: "fake", APIKey: "k", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	a, err := h.s.Agents.Create(agent.CreateRequest{IdempotencyKey: "a", WorkspaceID: ws, Prompt: "first", Provider: "f", Model: "m1"})
	if err != nil {
		t.Fatal(err)
	}
	h.waitStatus(t, a.ID, agent.StatusComplete)
	if _, err := h.s.Agents.Message(agent.MessageRequest{IdempotencyKey: "m", WorkspaceID: ws, AgentIDs: []int64{a.ID}, Body: "second"}); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	h.events = nil
	h.mu.Unlock()
	h.waitStatus(t, a.ID, agent.StatusComplete)

	got := transcript(t, h)
	if len(got) != 4 || !strings.Contains(got[1], `"reply first"`) || !strings.Contains(got[3], `"reply second"`) {
		t.Fatalf("transcript = %v", got)
	}
	h.fake.mu.Lock()
	second := h.fake.turns[1]
	h.fake.mu.Unlock()
	if len(second.Messages) != 3 || second.Messages[1].Role != "assistant" || string(second.Messages[1].Raw) != `{"native":true}` || second.Messages[1].RawKind != "fake" {
		t.Fatalf("second turn did not replay the native reply: %+v", second.Messages)
	}
	if second.Messages[1].StripThinking {
		t.Fatal("a reply made under the same prompt and tools lost its thinking")
	}
	if second.System != h.fake.turns[0].System {
		t.Fatal("system prompt changed between turns")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.deltas) == 0 || h.deltas[0].AgentID != a.ID {
		t.Fatalf("no live deltas: %+v", h.deltas)
	}
}

func TestFailuresBecomeSystemNotesAndAttention(t *testing.T) {
	h := setup(t, func(llm.Turn) (llm.Result, error) { return llm.Result{}, errors.New("401 invalid x-api-key") })
	// No provider selected.
	a, _ := h.s.Agents.Create(agent.CreateRequest{IdempotencyKey: "a", WorkspaceID: ws, Prompt: "hi"})
	h.waitStatus(t, a.ID, agent.StatusAttention)
	// Provider fails.
	h.s.Providers.Save(provider.SaveRequest{ID: "f", Kind: "fake", APIKey: "k", Models: []string{"m1"}})
	b, _ := h.s.Agents.Create(agent.CreateRequest{IdempotencyKey: "b", WorkspaceID: ws, Prompt: "hi", Provider: "f", Model: "m1"})
	h.waitStatus(t, b.ID, agent.StatusAttention)
	got := strings.Join(transcript(t, h), "\n")
	for _, want := range []string{"No model is selected", "Model call failed", "401 invalid x-api-key"} {
		if !strings.Contains(got, want) {
			t.Errorf("transcript missing %q:\n%s", want, got)
		}
	}
}

func TestMessageDuringTurnGetsItsOwnTurnInOrder(t *testing.T) {
	release := make(chan struct{})
	first := true
	h := setup(t, func(turn llm.Turn) (llm.Result, error) {
		if first {
			first = false
			<-release // hold the first turn open while the owner sends more
		}
		return llm.Result{Text: "re: " + turn.Messages[len(turn.Messages)-1].Text, StopReason: "end_turn"}, nil
	})
	h.s.Providers.Save(provider.SaveRequest{ID: "f", Kind: "fake", APIKey: "k", Models: []string{"m1"}})
	a, _ := h.s.Agents.Create(agent.CreateRequest{IdempotencyKey: "a", WorkspaceID: ws, Prompt: "one", Provider: "f", Model: "m1"})
	h.waitStatus(t, a.ID, agent.StatusRunning)
	h.s.Agents.Message(agent.MessageRequest{IdempotencyKey: "m", WorkspaceID: ws, AgentIDs: []int64{a.ID}, Body: "two"})
	close(release)
	deadline := time.Now().Add(3 * time.Second)
	for len(transcript(t, h)) < 4 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	got := transcript(t, h)
	if len(got) != 4 || !strings.Contains(got[3], `"re: two"`) {
		t.Fatalf("mid-turn message was not answered: %v", got)
	}
	h.fake.mu.Lock()
	defer h.fake.mu.Unlock()
	second := h.fake.turns[1].Messages
	roles := []string{}
	for _, m := range second {
		roles = append(roles, m.Role+":"+m.Text)
	}
	if strings.Join(roles, ",") != "user:one,assistant:re: one,user:two" {
		t.Fatalf("second turn order = %v", roles)
	}
}

func TestConversationPlacesRepliesAfterWhatTheySaw(t *testing.T) {
	log := []agent.MessageData{
		{From: agent.FromOwner, Body: "a"},
		{From: agent.FromOwner, Body: "b"}, // sent during the first turn
		{From: agent.FromAgent, Body: "re a", Seen: 1},
		{From: agent.FromSystem, Body: "note"},
	}
	var got []string
	for _, m := range run.Conversation(log) {
		got = append(got, m.Body)
	}
	if strings.Join(got, ",") != "a,re a,b" {
		t.Fatalf("conversation = %v", got)
	}
}

// toolModel calls the scripted tools one reply at a time, then ends the turn
// with a summary of the tool results it got back.
func toolModel(steps ...[]llm.ToolCall) func(llm.Turn) (llm.Result, error) {
	return func(turn llm.Turn) (llm.Result, error) {
		replies := 0
		for _, m := range turn.Messages {
			if m.Role == "assistant" {
				replies++
			}
		}
		if replies < len(steps) {
			return llm.Result{Calls: steps[replies], StopReason: "tool_use"}, nil
		}
		var got []string
		for _, m := range turn.Messages {
			for _, r := range m.Results {
				got = append(got, fmt.Sprintf("%v:%s", r.IsError, r.Content))
			}
		}
		return llm.Result{Text: strings.Join(got, " | "), StopReason: "end_turn"}, nil
	}
}

func tc(id, name string, input map[string]any) llm.ToolCall {
	raw, _ := json.Marshal(input)
	return llm.ToolCall{ID: id, Name: name, Input: raw}
}

func (h *harness) tools(t *testing.T, id int64) map[string]agent.ToolData {
	t.Helper()
	snap, _ := h.s.Agents.Snapshot(ws)
	out := map[string]agent.ToolData{}
	for _, e := range snap.Transcript {
		if e.Kind == agent.EventTool && e.AgentID == id {
			var d agent.ToolData
			json.Unmarshal(e.Data, &d)
			out[d.CallID] = d
		}
	}
	return out
}

func (h *harness) lastReply(t *testing.T) string {
	t.Helper()
	got := transcript(t, h)
	var m agent.MessageData
	json.Unmarshal([]byte(got[len(got)-1]), &m)
	return m.Body
}

func TestToolsWaitForTheOwnerUnderManualReview(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello\n"), 0o644)
	h := setupIn(t, root, toolModel(
		[]llm.ToolCall{tc("r", "read_file", map[string]any{"path": "a.txt"})},
		[]llm.ToolCall{
			tc("w", "write_file", map[string]any{"path": "b.txt", "content": "new\n"}),
			tc("x", "bash", map[string]any{"command": "touch c.txt"}),
		},
	))
	h.s.Providers.Save(provider.SaveRequest{ID: "f", Kind: "fake", APIKey: "k", Models: []string{"m1"}})
	a, _ := h.s.Agents.Create(agent.CreateRequest{IdempotencyKey: "a", WorkspaceID: ws, Prompt: "go", Provider: "f", Model: "m1"})
	h.waitStatus(t, a.ID, agent.StatusAttention)

	calls := h.tools(t, a.ID)
	if calls["r"].State != agent.ToolDone || calls["w"].State != agent.ToolPending || calls["x"].State != agent.ToolPending {
		t.Fatalf("states = %+v", calls)
	}
	if !strings.Contains(calls["w"].Preview, "+new") || calls["x"].Summary != "touch c.txt" {
		t.Fatalf("approval details = %+v", calls)
	}
	if _, err := os.Stat(filepath.Join(root, "b.txt")); err == nil {
		t.Fatal("write ran before approval")
	}
	// An owner message while calls wait must not reach the model first.
	h.s.Agents.Message(agent.MessageRequest{IdempotencyKey: "m", WorkspaceID: ws, AgentIDs: []int64{a.ID}, Body: "also this"})

	if _, err := h.s.Agents.Decide(agent.DecideRequest{IdempotencyKey: "d1", WorkspaceID: ws, AgentID: a.ID, CallID: "w", Allow: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.Agents.Decide(agent.DecideRequest{IdempotencyKey: "d1b", WorkspaceID: ws, AgentID: a.ID, CallID: "w", Allow: true}); err == nil {
		t.Fatal("deciding twice succeeded")
	}
	if _, err := os.Stat(filepath.Join(root, "b.txt")); err == nil {
		t.Fatal("ran before every call was decided")
	}
	h.mu.Lock()
	h.events = nil
	h.mu.Unlock()
	h.s.Agents.Decide(agent.DecideRequest{IdempotencyKey: "d2", WorkspaceID: ws, AgentID: a.ID, CallID: "x", Allow: false})
	h.waitStatus(t, a.ID, agent.StatusComplete)

	if data, _ := os.ReadFile(filepath.Join(root, "b.txt")); string(data) != "new\n" {
		t.Fatalf("b.txt = %q", data)
	}
	if _, err := os.Stat(filepath.Join(root, "c.txt")); err == nil {
		t.Fatal("denied command ran")
	}
	if got := h.lastReply(t); !strings.Contains(got, "false:     1\thello") || !strings.Contains(got, "false:Wrote b.txt") || !strings.Contains(got, "true:Denied: the user denied it.") {
		t.Fatalf("model saw = %q", got)
	}
	h.fake.mu.Lock()
	defer h.fake.mu.Unlock()
	last := h.fake.turns[len(h.fake.turns)-1].Messages
	var shape []string
	for _, m := range last {
		shape = append(shape, fmt.Sprintf("%s:%d:%s", m.Role, len(m.Results), m.Text))
	}
	if strings.Join(shape, ",") != "user:0:go,assistant:0:,user:1:,assistant:0:,user:2:,user:0:also this" {
		t.Fatalf("final turn = %v", shape)
	}
}

func TestPolicyDecidesWithoutTheOwner(t *testing.T) {
	root := t.TempDir()
	h := setupIn(t, root, toolModel([]llm.ToolCall{
		tc("w", "write_file", map[string]any{"path": "b.txt", "content": "x"}),
		tc("bad", "read_file", map[string]any{"path": "../../etc/passwd"}),
	}))
	h.s.Providers.Save(provider.SaveRequest{ID: "f", Kind: "fake", APIKey: "k", Models: []string{"m1"}})
	ro := policy.Policy{Access: "ro", Review: "manual", Commit: "blocked"}
	a, _ := h.s.Agents.Create(agent.CreateRequest{IdempotencyKey: "a", WorkspaceID: ws, Prompt: "go", Provider: "f", Model: "m1", Policy: &ro})
	h.waitStatus(t, a.ID, agent.StatusComplete)
	calls := h.tools(t, a.ID)
	if calls["w"].State != agent.ToolDenied || calls["w"].By != "policy" || calls["bad"].State != agent.ToolFailed {
		t.Fatalf("states = %+v", calls)
	}
	if got := h.lastReply(t); !strings.Contains(got, "true:Denied: this agent is read-only.") || !strings.Contains(got, "outside the workspace") {
		t.Fatalf("model saw = %q", got)
	}
}

func TestConversationPutsToolResultsAfterTheirCall(t *testing.T) {
	log := []agent.MessageData{
		{From: agent.FromOwner, Body: "a"},
		{From: agent.FromAgent, Body: "call", Seen: 1, Calls: []llm.ToolCall{{ID: "1"}}},
		{From: agent.FromOwner, Body: "b"}, // sent while the call waited
		{From: agent.FromTool, Body: "result"},
	}
	var got []string
	for _, m := range run.Conversation(log) {
		got = append(got, m.Body)
	}
	if strings.Join(got, ",") != "a,call,result,b" {
		t.Fatalf("conversation = %v", got)
	}
}

func TestPrefixCoversSystemPromptAndTools(t *testing.T) {
	base := llm.Turn{System: "s", Tools: []llm.Tool{{Name: "a"}}}
	if run.Prefix(base) != run.Prefix(llm.Turn{System: "s", Tools: []llm.Tool{{Name: "a"}}}) {
		t.Fatal("prefix is not deterministic")
	}
	if run.Prefix(base) == run.Prefix(llm.Turn{System: "s2", Tools: base.Tools}) || run.Prefix(base) == run.Prefix(llm.Turn{System: "s"}) {
		t.Fatal("prefix ignores the system prompt or the tools")
	}
}

func TestStopKillsARunningCommandAndPauses(t *testing.T) {
	root := t.TempDir()
	h := setupIn(t, root, toolModel([]llm.ToolCall{
		tc("s", "bash", map[string]any{"command": "sleep 20; touch late.txt"}),
		tc("n", "write_file", map[string]any{"path": "never.txt", "content": "x"}),
	}))
	h.s.Providers.Save(provider.SaveRequest{ID: "f", Kind: "fake", APIKey: "k", Models: []string{"m1"}})
	auto := policy.Policy{Access: "rw", Review: "auto", Commit: "review"}
	a, _ := h.s.Agents.Create(agent.CreateRequest{IdempotencyKey: "a", WorkspaceID: ws, Prompt: "go", Provider: "f", Model: "m1", Policy: &auto})
	// sleep is not a read-only command: even under auto review it asks.
	deadline := time.Now().Add(3 * time.Second)
	for h.tools(t, a.ID)["s"].State != agent.ToolPending && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := h.s.Agents.Decide(agent.DecideRequest{IdempotencyKey: "d", WorkspaceID: ws, AgentID: a.ID, CallID: "s", Allow: true}); err != nil {
		t.Fatal(err)
	}
	for h.tools(t, a.ID)["s"].State != agent.ToolAllowed && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond) // let bash start
	start := time.Now()
	if err := h.s.Agents.Stop(agent.ControlRequest{WorkspaceID: ws, AgentID: a.ID}); err != nil {
		t.Fatal(err)
	}
	h.waitStatus(t, a.ID, agent.StatusPaused)
	if time.Since(start) > 7*time.Second {
		t.Fatal("stop waited for the command")
	}
	calls := h.tools(t, a.ID)
	if calls["s"].State != agent.ToolFailed || !strings.Contains(calls["s"].Output, "Stopped") || calls["n"].State != agent.ToolFailed || !strings.Contains(calls["n"].Output, "Not run") {
		t.Fatalf("states = %+v", calls)
	}
	if _, err := os.Stat(filepath.Join(root, "never.txt")); err == nil {
		t.Fatal("an allowed call ran after stop")
	}
	if err := h.s.Agents.Stop(agent.ControlRequest{WorkspaceID: ws, AgentID: a.ID}); err == nil {
		t.Fatal("stopping an idle agent succeeded")
	}
	// A message continues it; the model sees the stopped results.
	h.mu.Lock()
	h.events = nil
	h.mu.Unlock()
	h.s.Agents.Message(agent.MessageRequest{IdempotencyKey: "m", WorkspaceID: ws, AgentIDs: []int64{a.ID}, Body: "carry on"})
	h.waitStatus(t, a.ID, agent.StatusComplete)
	if got := h.lastReply(t); !strings.Contains(got, "true:Stopped before it finished.") {
		t.Fatalf("model saw = %q", got)
	}
}

func TestTerminateDeniesWaitingCallsAndEndsSubagents(t *testing.T) {
	root := t.TempDir()
	h := setupIn(t, root, toolModel([]llm.ToolCall{tc("w", "write_file", map[string]any{"path": "b.txt", "content": "x"})}))
	h.s.Providers.Save(provider.SaveRequest{ID: "f", Kind: "fake", APIKey: "k", Models: []string{"m1"}})
	a, _ := h.s.Agents.Create(agent.CreateRequest{IdempotencyKey: "a", WorkspaceID: ws, Prompt: "go", Provider: "f", Model: "m1"})
	h.waitStatus(t, a.ID, agent.StatusAttention)
	child, _ := h.s.Agents.Create(agent.CreateRequest{IdempotencyKey: "c", WorkspaceID: ws, ParentID: a.ID, Title: "child"})
	if _, err := h.s.Agents.Terminate(agent.ControlRequest{IdempotencyKey: "t", WorkspaceID: ws, AgentID: a.ID}); err != nil {
		t.Fatal(err)
	}
	h.waitStatus(t, a.ID, agent.StatusTerminated)
	h.waitStatus(t, child.ID, agent.StatusTerminated)
	if c := h.tools(t, a.ID)["w"]; c.State != agent.ToolDenied || c.Reason != "the agent was terminated" {
		t.Fatalf("waiting call = %+v", c)
	}
	if _, err := h.s.Agents.Decide(agent.DecideRequest{IdempotencyKey: "d", WorkspaceID: ws, AgentID: a.ID, CallID: "w", Allow: true}); err == nil {
		t.Fatal("decided a call of a terminated agent")
	}
	if _, err := h.s.Agents.Message(agent.MessageRequest{IdempotencyKey: "m", WorkspaceID: ws, AgentIDs: []int64{a.ID}, Body: "hi"}); err == nil {
		t.Fatal("messaged a terminated agent")
	}
	if _, err := h.s.Agents.Terminate(agent.ControlRequest{IdempotencyKey: "t2", WorkspaceID: ws, AgentID: a.ID}); err == nil {
		t.Fatal("terminated twice")
	}
	if _, err := os.Stat(filepath.Join(root, "b.txt")); err == nil {
		t.Fatal("the denied write ran")
	}
}

func TestBtwAnswersWithoutTouchingAnyAgent(t *testing.T) {
	h := setup(t, func(turn llm.Turn) (llm.Result, error) {
		return llm.Result{Text: "answer", StopReason: "end_turn"}, nil
	})
	if _, err := h.s.Providers.Save(provider.SaveRequest{ID: "f", Kind: "fake", APIKey: "k", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	a, err := h.s.Agents.Create(agent.CreateRequest{IdempotencyKey: "a", WorkspaceID: ws, Prompt: "fix the parser", Provider: "f", Model: "m1"})
	if err != nil {
		t.Fatal(err)
	}
	h.waitStatus(t, a.ID, agent.StatusComplete)
	h.mu.Lock()
	events, deltas := len(h.events), len(h.deltas)
	h.mu.Unlock()
	before := transcript(t, h)

	got, err := h.s.Btw.Ask(btw.AskRequest{WorkspaceID: ws, Question: "what is #1 doing?", Provider: "f", Model: "m1"})
	if err != nil || got.Text != "answer" {
		t.Fatalf("Ask = %+v, %v", got, err)
	}
	h.fake.mu.Lock()
	last := h.fake.turns[len(h.fake.turns)-1]
	h.fake.mu.Unlock()
	if len(last.Tools) != 0 || !strings.Contains(last.Messages[0].Text, "## Agent #1: fix the parser") || !strings.Contains(last.Messages[0].Text, "- agent: answer") || !strings.HasSuffix(last.Messages[0].Text, "Question: what is #1 doing?") {
		t.Fatalf("btw turn = %+v", last)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.events) != events || len(h.deltas) != deltas {
		t.Fatalf("btw emitted %d events and %d deltas", len(h.events)-events, len(h.deltas)-deltas)
	}
	if after := transcript(t, h); len(after) != len(before) {
		t.Fatalf("transcript changed: %v", after)
	}
	if _, err := h.s.Btw.Ask(btw.AskRequest{WorkspaceID: ws, Question: " ", Provider: "f", Model: "m1"}); err == nil {
		t.Fatal("empty question accepted")
	}
}

func TestAgentsWorkInTheirProject(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	os.MkdirAll(filepath.Join(other, "src"), 0o755)
	os.WriteFile(filepath.Join(other, "src", "a.go"), []byte("package a\n"), 0o644)
	os.Symlink(root, filepath.Join(other, "escape"))
	h := setupIn(t, root, toolModel([]llm.ToolCall{tc("w", "write_file", map[string]any{"path": "note.txt", "content": "hi\n"})}))
	if _, err := h.s.Providers.Save(provider.SaveRequest{ID: "f", Kind: "fake", APIKey: "k", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}

	// The server root is the first project, created at startup.
	projects, err := h.s.Projects.Projects(ws)
	realRoot, _ := filepath.EvalSymlinks(root)
	if err != nil || len(projects) != 1 || projects[0].Path != realRoot {
		t.Fatalf("default projects = %+v, %v", projects, err)
	}
	p, err := h.s.Projects.Add(project.AddRequest{WorkspaceID: ws, Path: other, Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"relative/dir", filepath.Join(other, "missing"), filepath.Join(other, "src", "a.go"), other} {
		if _, err := h.s.Projects.Add(project.AddRequest{WorkspaceID: ws, Path: bad}); err == nil {
			t.Errorf("Add(%q) succeeded", bad)
		}
	}

	files, err := h.s.Projects.Files(project.FilesRequest{WorkspaceID: ws, ProjectID: p.ID})
	if err != nil || len(files) != 2 || files[0].Name != "escape" && files[0].Name != "src" {
		t.Fatalf("files = %+v, %v", files, err)
	}
	inner, err := h.s.Projects.Files(project.FilesRequest{WorkspaceID: ws, ProjectID: p.ID, Dir: "src"})
	if err != nil || len(inner) != 1 || inner[0].Path != "src/a.go" || inner[0].Size != 10 {
		t.Fatalf("src = %+v, %v", inner, err)
	}
	for _, dir := range []string{"..", "escape", "/etc"} {
		if _, err := h.s.Projects.Files(project.FilesRequest{WorkspaceID: ws, ProjectID: p.ID, Dir: dir}); err == nil {
			t.Errorf("Files(%q) left the project", dir)
		}
	}

	// An agent in the other project writes there, not in the server root.
	auto := policy.Policy{Access: "rw", Review: "auto", Commit: "review"}
	a, err := h.s.Agents.Create(agent.CreateRequest{IdempotencyKey: "p", WorkspaceID: ws, Prompt: "go", Provider: "f", Model: "m1", Policy: &auto, ProjectID: p.ID})
	if err != nil || a.ProjectID != p.ID {
		t.Fatalf("Create = %+v, %v", a, err)
	}
	h.waitStatus(t, a.ID, agent.StatusComplete)
	if _, err := os.Stat(filepath.Join(other, "note.txt")); err != nil {
		t.Fatal("write did not land in the project folder")
	}
	if _, err := os.Stat(filepath.Join(root, "note.txt")); err == nil {
		t.Fatal("write landed in the server root")
	}

	// Subagents inherit the project; top-level agents default to the first.
	sub, err := h.s.Agents.Create(agent.CreateRequest{IdempotencyKey: "s", WorkspaceID: ws, ParentID: a.ID, Title: "sub"})
	if err != nil || sub.ProjectID != p.ID {
		t.Fatalf("subagent = %+v, %v", sub, err)
	}
	if _, err := h.s.Agents.Create(agent.CreateRequest{IdempotencyKey: "s2", WorkspaceID: ws, ParentID: a.ID, Title: "sub", ProjectID: projects[0].ID}); err == nil {
		t.Fatal("subagent moved to another project")
	}
	top, err := h.s.Agents.Create(agent.CreateRequest{IdempotencyKey: "t", WorkspaceID: ws, Title: "top"})
	if err != nil || top.ProjectID != projects[0].ID {
		t.Fatalf("top-level default = %+v, %v", top, err)
	}

	// A project with agents cannot be removed; an unused one can.
	if err := h.s.Projects.Remove(project.RemoveRequest{WorkspaceID: ws, ID: p.ID}); err == nil {
		t.Fatal("removed a project that has agents")
	}
	empty := t.TempDir()
	q, _ := h.s.Projects.Add(project.AddRequest{WorkspaceID: ws, Path: empty})
	if err := h.s.Projects.Remove(project.RemoveRequest{WorkspaceID: ws, ID: q.ID}); err != nil {
		t.Fatal(err)
	}
}

// An agent on an OpenAI-compatible endpoint: the runner's tool loop works
// through the openai adapter (tool call, gate, result, final answer).
func TestOpenAICompatibleAgentUsesTools(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hi"), 0o644)
	var mu sync.Mutex
	var requests []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		requests = append(requests, body)
		n := len(requests)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"function\":{\"name\":\"list_dir\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n")
		} else {
			msgs := body["messages"].([]any)
			last := msgs[len(msgs)-1].(map[string]any)
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"saw %s\"},\"finish_reason\":\"stop\"}]}\n\n", strings.TrimSpace(last["content"].(string)))
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	s, err := backend.Open(filepath.Join(t.TempDir(), "x.db"), backend.Options{Root: root, Adapters: []llm.Adapter{openai.Adapter{}}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Providers.Save(provider.SaveRequest{ID: "local", Kind: "openai", BaseURL: srv.URL, Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	a, err := s.Agents.Create(agent.CreateRequest{IdempotencyKey: "a", WorkspaceID: ws, Prompt: "what is here?", Provider: "local", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		snap, _ := s.Agents.Snapshot(ws)
		got := ""
		for _, e := range snap.Transcript {
			if e.Kind == agent.EventMessage && strings.Contains(string(e.Data), `"from":"agent"`) && strings.Contains(string(e.Data), "saw") {
				got = string(e.Data)
			}
		}
		if got != "" {
			if !strings.Contains(got, "saw hello.txt") {
				t.Fatalf("final reply = %s", got)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no final reply; transcript: %v", snap.Transcript)
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 || requests[0]["model"] != "m" || requests[0]["tools"] == nil {
		t.Fatalf("requests = %d, first = %v", len(requests), requests[0])
	}
	_ = a
}

// An agent's MCP calls go through the same gate as the built-in tools: a
// server's trust decides whether they wait for the owner.
func TestMCPCallsPassTheGate(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "t", Version: "1"}, nil)
	sdk.AddTool(server, &sdk.Tool{Name: "echo"}, func(_ context.Context, _ *sdk.CallToolRequest, in struct {
		Text string `json:"text"`
	}) (*sdk.CallToolResult, any, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "echo: " + in.Text}}}, nil, nil
	})
	srv := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil))
	t.Cleanup(srv.Close)
	h := setup(t, toolModel([]llm.ToolCall{tc("m", "mcp__srv__echo", map[string]any{"text": "hi"})}))
	h.s.Providers.Save(provider.SaveRequest{ID: "f", Kind: "fake", APIKey: "k", Models: []string{"m1"}})
	if got, err := h.s.MCP.Save(mcp.SaveRequest{WorkspaceID: ws, ID: "srv", Transport: "http", URL: srv.URL, Trust: mcp.TrustAsk, Enabled: true}); err != nil || got.Status != "connected" {
		t.Fatalf("mcp save = %+v, %v", got, err)
	}
	auto := policy.Policy{Access: "rw", Review: "auto", Commit: "review"}

	// Trust "ask": the call waits, even under auto review.
	a, _ := h.s.Agents.Create(agent.CreateRequest{IdempotencyKey: "a", WorkspaceID: ws, Prompt: "go", Provider: "f", Model: "m1", Policy: &auto})
	h.waitStatus(t, a.ID, agent.StatusAttention)
	if st := h.tools(t, a.ID)["m"]; st.State != agent.ToolPending || !strings.Contains(st.Summary, "echo") {
		t.Fatalf("call = %+v", st)
	}
	h.s.Agents.Decide(agent.DecideRequest{IdempotencyKey: "d", WorkspaceID: ws, AgentID: a.ID, CallID: "m", Allow: true})
	h.waitStatus(t, a.ID, agent.StatusComplete)
	if reply := h.lastReply(t); !strings.Contains(reply, "false:echo: hi") {
		t.Fatalf("model saw %q", reply)
	}
	// The turn offered the MCP tool beside the built-ins.
	h.fake.mu.Lock()
	tools := h.fake.turns[len(h.fake.turns)-1].Tools
	h.fake.mu.Unlock()
	offered := false
	for _, tl := range tools {
		offered = offered || tl.Name == "mcp__srv__echo"
	}
	if !offered {
		t.Fatalf("the MCP tool was not offered: %+v", tools)
	}

	// Trust "read": the call runs at once.
	h.s.MCP.Save(mcp.SaveRequest{WorkspaceID: ws, ID: "srv", Transport: "http", URL: srv.URL, Trust: mcp.TrustRead, Enabled: true})
	b, _ := h.s.Agents.Create(agent.CreateRequest{IdempotencyKey: "b", WorkspaceID: ws, Prompt: "go", Provider: "f", Model: "m1"})
	h.waitStatus(t, b.ID, agent.StatusComplete)
	if st := h.tools(t, b.ID)["m"]; st.State != agent.ToolDone || st.By != "" {
		t.Fatalf("read-trust call = %+v", st)
	}
}

// ask_user needs no permission: the call waits for the user's answers, which
// become its result for the model.
func TestAskUserWaitsForAnswers(t *testing.T) {
	ask := tc("q", "ask_user", map[string]any{"title": "Before I start", "questions": []any{
		map[string]any{"prompt": "Which database?", "options": []any{map[string]any{"label": "SQLite"}, map[string]any{"label": "Postgres", "description": "needs a server"}}},
		map[string]any{"prompt": "Anything else?", "required": false},
	}})
	h := setup(t, toolModel([]llm.ToolCall{ask}))
	h.s.Providers.Save(provider.SaveRequest{ID: "f", Kind: "fake", APIKey: "k", Models: []string{"m1"}})
	auto := policy.Policy{Access: "rw", Review: "auto", Commit: "allowed"}
	a, _ := h.s.Agents.Create(agent.CreateRequest{IdempotencyKey: "a", WorkspaceID: ws, Prompt: "go", Provider: "f", Model: "m1", Policy: &auto})
	h.waitStatus(t, a.ID, agent.StatusAttention)
	st := h.tools(t, a.ID)["q"]
	if st.State != agent.ToolPending || st.Effect != tool.Ask || st.Summary != "Before I start" || !strings.Contains(st.Preview, `"id":"o2","label":"Postgres"`) {
		t.Fatalf("waiting call = %+v", st)
	}
	answer := func(key string, answers map[string]tool.Answer) error {
		_, err := h.s.Agents.Answer(agent.AnswerRequest{IdempotencyKey: key, WorkspaceID: ws, AgentID: a.ID, CallID: "q", Answers: answers})
		return err
	}
	// Allow makes no sense for questions; a required question must be answered.
	if _, err := h.s.Agents.Decide(agent.DecideRequest{IdempotencyKey: "d", WorkspaceID: ws, AgentID: a.ID, CallID: "q", Allow: true}); err == nil {
		t.Fatal("Allow on a question was accepted")
	}
	if err := answer("x", map[string]tool.Answer{"q2": {Text: "no"}}); err == nil {
		t.Fatal("a missing required answer was accepted")
	}
	if err := answer("y", map[string]tool.Answer{"q1": {Choices: []string{"o1", "o2"}}}); err == nil {
		t.Fatal("two choices on a single-choice question were accepted")
	}
	if err := answer("ok", map[string]tool.Answer{"q1": {Choices: []string{"o2"}, Text: "we have one"}}); err != nil {
		t.Fatal(err)
	}
	h.waitStatus(t, a.ID, agent.StatusComplete)
	got := h.lastReply(t)
	if !strings.Contains(got, "false:Q: Which database?\nA: Postgres\n   we have one\nQ: Anything else?\nA: (skipped)") {
		t.Fatalf("model saw %q", got)
	}
}

func TestDismissedQuestionsTellTheModel(t *testing.T) {
	h := setup(t, toolModel([]llm.ToolCall{tc("q", "ask_user", map[string]any{"questions": []any{map[string]any{"prompt": "Proceed?"}}})}))
	h.s.Providers.Save(provider.SaveRequest{ID: "f", Kind: "fake", APIKey: "k", Models: []string{"m1"}})
	a, _ := h.s.Agents.Create(agent.CreateRequest{IdempotencyKey: "a", WorkspaceID: ws, Prompt: "go", Provider: "f", Model: "m1"})
	h.waitStatus(t, a.ID, agent.StatusAttention)
	h.s.Agents.Decide(agent.DecideRequest{IdempotencyKey: "d", WorkspaceID: ws, AgentID: a.ID, CallID: "q", Allow: false})
	h.waitStatus(t, a.ID, agent.StatusComplete)
	if got := h.lastReply(t); !strings.Contains(got, "true:Denied: the user dismissed the questions.") {
		t.Fatalf("model saw %q", got)
	}
}

// Near the context budget, older messages are summarized by the agent's own
// model; the model then sees the summary and the recent messages, and the
// log keeps everything.
func TestCompactionKeepsTheRequestWithinBudget(t *testing.T) {
	var summaries []string
	h := setup(t, func(turn llm.Turn) (llm.Result, error) {
		if strings.HasPrefix(turn.System, "You compact") {
			summaries = append(summaries, turn.Messages[0].Text)
			return llm.Result{Text: fmt.Sprintf("SUMMARY %d", len(summaries)), StopReason: "end_turn"}, nil
		}
		last := turn.Messages[len(turn.Messages)-1].Text
		return llm.Result{Text: "re: " + last, StopReason: "end_turn", InputTokens: 900}, nil
	})
	if _, err := h.s.Providers.Save(provider.SaveRequest{ID: "f", Kind: "fake", APIKey: "k", Models: []string{"m1"}, ContextTokens: 1000}); err != nil {
		t.Fatal(err)
	}
	a, _ := h.s.Agents.Create(agent.CreateRequest{IdempotencyKey: "a", WorkspaceID: ws, Prompt: "first", Provider: "f", Model: "m1"})
	h.waitStatus(t, a.ID, agent.StatusComplete)
	send := func(key, body string) {
		h.mu.Lock()
		h.events = nil
		h.mu.Unlock()
		h.s.Agents.Message(agent.MessageRequest{IdempotencyKey: key, WorkspaceID: ws, AgentIDs: []int64{a.ID}, Body: body})
		h.waitStatus(t, a.ID, agent.StatusComplete)
	}
	send("m2", "second") // 900 of 1000 tokens used: compacts before answering
	if len(summaries) != 1 || !strings.Contains(summaries[0], "[user]\nfirst") || !strings.Contains(summaries[0], "[agent]\nre: first") {
		t.Fatalf("summarizer saw %q", summaries)
	}
	h.fake.mu.Lock()
	turn := h.fake.turns[len(h.fake.turns)-1]
	h.fake.mu.Unlock()
	if len(turn.Messages) != 2 || !strings.Contains(turn.Messages[0].Text, "SUMMARY 1") || turn.Messages[1].Text != "second" {
		t.Fatalf("after compaction the model saw %+v", turn.Messages)
	}
	send("m3", "third") // compacts again, folding in the previous summary
	if len(summaries) != 2 || !strings.HasPrefix(summaries[1], "Summary so far:\nSUMMARY 1") || !strings.Contains(summaries[1], "[user]\nsecond") {
		t.Fatalf("second summary input = %q", summaries[1:])
	}
	// The log keeps every message, plus the two compaction records.
	kinds := map[string]int{}
	for _, raw := range transcript(t, h) {
		var m agent.MessageData
		json.Unmarshal([]byte(raw), &m)
		kinds[m.From]++
	}
	if kinds[agent.FromOwner] != 3 || kinds[agent.FromAgent] != 3 || kinds[agent.FromCompaction] != 2 {
		t.Fatalf("log = %v", kinds)
	}
}

func TestTooLongErrorCompactsAndRetries(t *testing.T) {
	compacted := false
	h := setup(t, func(turn llm.Turn) (llm.Result, error) {
		if strings.HasPrefix(turn.System, "You compact") {
			compacted = true
			return llm.Result{Text: "short summary", StopReason: "end_turn"}, nil
		}
		if !compacted && len(turn.Messages) > 1 {
			return llm.Result{}, errors.New("400: prompt is too long: 210000 tokens > 200000 maximum")
		}
		return llm.Result{Text: "ok", StopReason: "end_turn"}, nil
	})
	h.s.Providers.Save(provider.SaveRequest{ID: "f", Kind: "fake", APIKey: "k", Models: []string{"m1"}})
	a, _ := h.s.Agents.Create(agent.CreateRequest{IdempotencyKey: "a", WorkspaceID: ws, Prompt: "first", Provider: "f", Model: "m1"})
	h.waitStatus(t, a.ID, agent.StatusComplete)
	h.mu.Lock()
	h.events = nil
	h.mu.Unlock()
	h.s.Agents.Message(agent.MessageRequest{IdempotencyKey: "m", WorkspaceID: ws, AgentIDs: []int64{a.ID}, Body: "second"})
	h.waitStatus(t, a.ID, agent.StatusComplete)
	if !compacted || h.lastReply(t) != "ok" {
		t.Fatalf("compacted = %v, reply = %q", compacted, h.lastReply(t))
	}
}

// Channels: who may talk to whom, delivery into transcripts, and waking.
func TestChannelsFollowTheFamilyRule(t *testing.T) {
	h := setup(t, func(turn llm.Turn) (llm.Result, error) {
		return llm.Result{Text: "ok", StopReason: "end_turn"}, nil
	})
	h.s.Providers.Save(provider.SaveRequest{ID: "f", Kind: "fake", APIKey: "k", Models: []string{"m1"}})
	other, _ := h.s.Projects.Add(project.AddRequest{WorkspaceID: ws, Path: t.TempDir()})
	mk := func(key, title string, parent, proj int64) agent.Agent {
		a, err := h.s.Agents.Create(agent.CreateRequest{IdempotencyKey: key, WorkspaceID: ws, Title: title, ParentID: parent, ProjectID: proj, Provider: "f", Model: "m1"})
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	top1 := mk("1", "top one", 0, 0)
	top2 := mk("2", "top two", 0, other.ID) // another project
	sub1 := mk("3", "sub one", top1.ID, 0)
	sub2 := mk("4", "sub two", top1.ID, 0)
	subX := mk("5", "sub of two", top2.ID, 0)
	ch := h.s.Channels
	if _, err := ch.Create(top1, "plan", []int64{top2.ID, sub1.ID}, ""); err != nil {
		t.Fatalf("top-level with another project's agent and own subagent: %v", err)
	}
	if _, err := ch.Create(sub1, "family", []int64{sub2.ID, top1.ID}, ""); err != nil {
		t.Fatalf("subagent with sibling and parent: %v", err)
	}
	for _, bad := range []struct {
		from agent.Agent
		to   int64
	}{{sub1, top2.ID}, {sub1, subX.ID}, {top1, subX.ID}} {
		if _, err := ch.Create(bad.from, "x", []int64{bad.to}, ""); err == nil {
			t.Errorf("#%d could open a channel with #%d", bad.from.ID, bad.to)
		}
	}

	// A message reaches the other members' transcripts and wakes them.
	c, _ := ch.Create(top1, "sync", []int64{top2.ID}, "")
	if _, err := ch.Send(top1, c.ID, "hello there"); err != nil {
		t.Fatal(err)
	}
	h.waitStatus(t, top2.ID, agent.StatusComplete)
	h.fake.mu.Lock()
	seen := ""
	for _, turn := range h.fake.turns {
		if strings.Contains(turn.System, fmt.Sprintf("agent #%d ", top2.ID)) {
			seen = turn.Messages[len(turn.Messages)-1].Text
		}
	}
	h.fake.mu.Unlock()
	if !strings.Contains(seen, fmt.Sprintf("message from agent #%d (top one):\nhello there", top1.ID)) {
		t.Fatalf("top two saw %q", seen)
	}
	if _, err := ch.Send(subX, c.ID, "sneaky"); err == nil {
		t.Fatal("a non-member sent to the channel")
	}
	views, _ := ch.Channels(ws)
	if len(views) != 3 || len(views[2].Messages) != 1 || views[2].Messages[0].Body != "hello there" {
		t.Fatalf("channels = %+v", views)
	}
}

func TestChannelBurstLimit(t *testing.T) {
	h := setup(t, func(llm.Turn) (llm.Result, error) { return llm.Result{Text: "ok", StopReason: "end_turn"}, nil })
	h.s.Providers.Save(provider.SaveRequest{ID: "f", Kind: "fake", APIKey: "k", Models: []string{"m1"}})
	a, _ := h.s.Agents.Create(agent.CreateRequest{IdempotencyKey: "a", WorkspaceID: ws, Title: "a", Provider: "f", Model: "m1"})
	b, _ := h.s.Agents.Create(agent.CreateRequest{IdempotencyKey: "b", WorkspaceID: ws, Title: "b", Provider: "f", Model: "m1"})
	c, _ := h.s.Channels.Create(a, "chatty", []int64{b.ID}, "")
	for i := 0; i < 30; i++ {
		if _, err := h.s.Channels.Send(a, c.ID, fmt.Sprint("msg ", i)); err != nil {
			t.Fatalf("message %d: %v", i, err)
		}
	}
	if _, err := h.s.Channels.Send(a, c.ID, "one too many"); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("31st message = %v", err)
	}
}

// spawn_agent: the parent starts a subagent; the subagent's final answer is
// posted to their channel and wakes the parent.
func TestSpawnedSubagentReportsBack(t *testing.T) {
	h := setup(t, func(turn llm.Turn) (llm.Result, error) {
		last := turn.Messages[len(turn.Messages)-1]
		switch {
		case strings.Contains(last.Text, "Final answer:"):
			return llm.Result{Text: "thanks, got it", StopReason: "end_turn"}, nil
		case strings.Contains(last.Text, "You are a subagent"):
			return llm.Result{Text: "child done: 42", StopReason: "end_turn"}, nil
		case last.Text == "split the work":
			return llm.Result{Calls: []llm.ToolCall{tc("s", "spawn_agent", map[string]any{"task": "count the files", "title": "Counter"})}, StopReason: "tool_use"}, nil
		}
		return llm.Result{Text: "ok", StopReason: "end_turn"}, nil
	})
	h.s.Providers.Save(provider.SaveRequest{ID: "f", Kind: "fake", APIKey: "k", Models: []string{"m1"}})
	auto := policy.Policy{Access: "rw", Review: "auto", Commit: "review"}
	parent, _ := h.s.Agents.Create(agent.CreateRequest{IdempotencyKey: "p", WorkspaceID: ws, Prompt: "split the work", Provider: "f", Model: "m1", Policy: &auto})
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.fake.mu.Lock()
		n := 0
		for _, turn := range h.fake.turns {
			if strings.Contains(turn.Messages[len(turn.Messages)-1].Text, "Final answer:\nchild done: 42") {
				n++
			}
		}
		h.fake.mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the parent never received the subagent's answer; transcript: %v", transcript(t, h))
		}
		time.Sleep(20 * time.Millisecond)
	}
	snap, _ := h.s.Agents.Snapshot(ws)
	var child agent.Agent
	for _, a := range snap.Agents {
		if a.ParentID == parent.ID {
			child = a
		}
	}
	if child.ID == 0 || child.Title != "Counter" || child.Policy != auto || child.ProjectID != parent.ProjectID || child.Model != "m1" {
		t.Fatalf("child = %+v", child)
	}
	if st := h.tools(t, parent.ID)["s"]; st.State != agent.ToolDone || !strings.Contains(st.Output, fmt.Sprintf("Started subagent #%d", child.ID)) {
		t.Fatalf("spawn call = %+v", st)
	}
}

// Attachments: text is inlined after the message, images go as files, and
// the bytes live on disk, not in the event log.
func TestAttachmentsReachTheModel(t *testing.T) {
	h := setup(t, func(llm.Turn) (llm.Result, error) { return llm.Result{Text: "seen", StopReason: "end_turn"}, nil })
	h.s.Providers.Save(provider.SaveRequest{ID: "f", Kind: "fake", APIKey: "k", Models: []string{"m1"}})
	notes, err := h.s.Files.Upload(attachment.UploadRequest{WorkspaceID: ws, Name: "notes.md", Data: []byte("# Plan\nship it")})
	if err != nil {
		t.Fatal(err)
	}
	pic, _ := h.s.Files.Upload(attachment.UploadRequest{WorkspaceID: ws, Name: "shot.png", Type: "image/png", Data: []byte("\x89PNG fake")})
	blob, _ := h.s.Files.Upload(attachment.UploadRequest{WorkspaceID: ws, Name: "x.bin", Data: []byte{0, 1, 2}})
	if notes.Type != "text/markdown" {
		t.Errorf("type from extension = %q", notes.Type)
	}
	a, err := h.s.Agents.Create(agent.CreateRequest{IdempotencyKey: "a", WorkspaceID: ws, Prompt: "review these", Provider: "f", Model: "m1", Attachments: []string{notes.ID, pic.ID, blob.ID}})
	if err != nil {
		t.Fatal(err)
	}
	h.waitStatus(t, a.ID, agent.StatusComplete)
	h.fake.mu.Lock()
	first := h.fake.turns[0].Messages[0]
	h.fake.mu.Unlock()
	if !strings.HasPrefix(first.Text, "review these") || !strings.Contains(first.Text, "Attached file notes.md:\n````\n# Plan\nship it\n````") ||
		!strings.Contains(first.Text, "(Attached shot.png follows.)") || !strings.Contains(first.Text, "x.bin, application/octet-stream, 3 bytes") {
		t.Fatalf("text = %q", first.Text)
	}
	if len(first.Files) != 1 || first.Files[0].Type != "image/png" || string(first.Files[0].Data) != "\x89PNG fake" {
		t.Fatalf("files = %+v", first.Files)
	}
	for _, raw := range transcript(t, h) {
		if strings.Contains(raw, "ship it") {
			t.Fatalf("attachment bytes in the log: %s", raw)
		}
	}
	if _, err := h.s.Agents.Message(agent.MessageRequest{IdempotencyKey: "m", WorkspaceID: ws, AgentIDs: []int64{a.ID}, Body: "x", Attachments: []string{"nope"}}); err == nil {
		t.Fatal("an unknown attachment was accepted")
	}
	got, err := h.s.Files.Read(ws, pic.ID)
	if err != nil || string(got.Data) != "\x89PNG fake" {
		t.Fatalf("read = %v %v", got, err)
	}
	if _, err := h.s.Files.Read("other", pic.ID); err == nil {
		t.Fatal("read across workspaces")
	}
}

// Memories: the user's and agents' notes reach every agent's system prompt
// as an index; memory_read gives the full text; memory_save is gated.
func TestMemoriesReachEveryAgent(t *testing.T) {
	h := setup(t, func(turn llm.Turn) (llm.Result, error) {
		last := turn.Messages[len(turn.Messages)-1]
		switch {
		case last.Text == "remember":
			return llm.Result{Calls: []llm.ToolCall{tc("s", "memory_save", map[string]any{"content": "Use tabs in Go files\nbecause gofmt."})}, StopReason: "tool_use"}, nil
		case last.Text == "recall":
			return llm.Result{Calls: []llm.ToolCall{tc("r", "memory_read", map[string]any{"id": 1})}, StopReason: "tool_use"}, nil
		}
		return llm.Result{Text: "ok", StopReason: "end_turn"}, nil
	})
	h.s.Providers.Save(provider.SaveRequest{ID: "f", Kind: "fake", APIKey: "k", Models: []string{"m1"}})
	if _, err := h.s.Memories.Save(memory.SaveRequest{WorkspaceID: ws, Content: "The user prefers short replies.\nDetails: no preamble."}); err != nil {
		t.Fatal(err)
	}
	a, _ := h.s.Agents.Create(agent.CreateRequest{IdempotencyKey: "a", WorkspaceID: ws, Prompt: "recall", Provider: "f", Model: "m1"})
	h.waitStatus(t, a.ID, agent.StatusComplete)
	h.fake.mu.Lock()
	system := h.fake.turns[0].System
	h.fake.mu.Unlock()
	if !strings.Contains(system, "- #1 The user prefers short replies. …") || strings.Contains(system, "no preamble") {
		t.Fatalf("system prompt index:\n%s", system)
	}
	if out := h.tools(t, a.ID)["r"].Output; !strings.Contains(out, "Details: no preamble.") || !strings.Contains(out, "from user") {
		t.Fatalf("memory_read = %q", out)
	}
	// memory_save waits under manual review, then reaches another agent.
	h.mu.Lock()
	h.events = nil
	h.mu.Unlock()
	h.s.Agents.Message(agent.MessageRequest{IdempotencyKey: "m", WorkspaceID: ws, AgentIDs: []int64{a.ID}, Body: "remember"})
	h.waitStatus(t, a.ID, agent.StatusAttention)
	if st := h.tools(t, a.ID)["s"]; st.State != agent.ToolPending {
		t.Fatalf("memory_save = %+v", st)
	}
	h.s.Agents.Decide(agent.DecideRequest{IdempotencyKey: "d", WorkspaceID: ws, AgentID: a.ID, CallID: "s", Allow: true})
	h.waitStatus(t, a.ID, agent.StatusComplete)
	b, _ := h.s.Agents.Create(agent.CreateRequest{IdempotencyKey: "b", WorkspaceID: ws, Prompt: "hi", Provider: "f", Model: "m1"})
	h.waitStatus(t, b.ID, agent.StatusComplete)
	h.fake.mu.Lock()
	system = h.fake.turns[len(h.fake.turns)-1].System
	h.fake.mu.Unlock()
	if !strings.Contains(system, "- #2 Use tabs in Go files …") {
		t.Fatalf("the new agent's index:\n%s", system)
	}
	list, _ := h.s.Memories.Memories(ws)
	if len(list) != 2 || list[0].Source != fmt.Sprintf("agent #%d", a.ID) {
		t.Fatalf("memories = %+v", list)
	}
	// Import skips what is already saved.
	list, _ = h.s.Memories.Import(ws, []string{"brand new", "The user prefers short replies.\nDetails: no preamble."})
	if len(list) != 3 {
		t.Fatalf("after import = %+v", list)
	}
}
