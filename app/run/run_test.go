package run_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"xhaos/app/agent"
	"xhaos/app/backend"
	"xhaos/app/btw"
	"xhaos/app/eventlog"
	"xhaos/app/llm"
	"xhaos/app/policy"
	"xhaos/app/provider"
	"xhaos/app/run"
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
	if got := h.lastReply(t); !strings.Contains(got, "false:     1\thello") || !strings.Contains(got, "false:Wrote b.txt") || !strings.Contains(got, "true:Denied: the owner denied it.") {
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
	deadline := time.Now().Add(3 * time.Second)
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
