package claudecli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/devark28/xhaos/app/llm"
)

// The tests run this test binary as a fake `claude`: FAKE_SCENARIO picks what
// it does, and it writes what it was given to FAKE_LOG. What it prints copies
// the stream-json of Claude Code 2.1.322 (the not-logged-in run was captured
// from the real program); the rest follows the shapes Hermes' driver reads.
func TestMain(m *testing.M) {
	MaybeServeMCP()
	if os.Getenv("FAKE_SCENARIO") != "" {
		fakeClaude(os.Getenv("FAKE_SCENARIO"))
		return
	}
	os.Exit(m.Run())
}

func emit(v any) {
	raw, _ := json.Marshal(v)
	fmt.Println(string(raw))
}

func result(extra map[string]any) map[string]any {
	r := map[string]any{"type": "result", "subtype": "success", "is_error": false, "num_turns": 1, "stop_reason": "end_turn",
		"usage": map[string]any{"input_tokens": 12, "cache_read_input_tokens": 300, "cache_creation_input_tokens": 40, "output_tokens": 7}}
	for k, v := range extra {
		r[k] = v
	}
	return r
}

func assistant(blocks ...any) map[string]any {
	return map[string]any{"type": "assistant", "message": map[string]any{"id": "msg_1", "role": "assistant", "type": "message", "content": blocks,
		"usage": map[string]any{"input_tokens": 12, "output_tokens": 7}}}
}

func textDelta(s string) map[string]any {
	return map[string]any{"type": "stream_event", "event": map[string]any{"type": "content_block_delta", "delta": map[string]any{"type": "text_delta", "text": s}}}
}

func stopDelta(reason string) map[string]any {
	return map[string]any{"type": "stream_event", "event": map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": reason}}}
}

func fakeClaude(scenario string) {
	if len(os.Args) > 2 && os.Args[1] == "auth" {
		if scenario == "loggedout" {
			fmt.Println(`{"loggedIn": false, "authMethod": "none"}`)
		} else {
			fmt.Println(`{"loggedIn": true, "authMethod": "claude.ai", "subscriptionType": "max"}`)
		}
		return
	}
	var frames []map[string]any
	in := bufio.NewReaderSize(os.Stdin, 1<<20)
	emit(map[string]any{"type": "system", "subtype": "init"})
	for {
		line, err := in.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var f map[string]any
			_ = json.Unmarshal(line, &f)
			frames = append(frames, f)
			if q, ok := f["shouldQuery"].(bool); ok && !q {
				emit(result(map[string]any{"num_turns": 0, "stop_reason": nil}))
			}
		}
		if err != nil {
			break
		}
	}
	if path := os.Getenv("FAKE_LOG"); path != "" {
		var env []string
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, "FAKE_") {
				env = append(env, kv)
			}
		}
		cwd, _ := os.Getwd()
		var system []byte
		for i, a := range os.Args {
			if a == "--system-prompt-file" {
				system, _ = os.ReadFile(os.Args[i+1])
			}
		}
		raw, _ := json.Marshal(map[string]any{"args": os.Args[1:], "env": env, "frames": frames, "cwd": cwd, "system": string(system)})
		_ = os.WriteFile(path, raw, 0o600)
	}
	switch scenario {
	case "text":
		emit(textDelta("Hel"))
		emit(textDelta("lo"))
		emit(stopDelta("end_turn"))
		emit(assistant(map[string]any{"type": "text", "text": "Hello"}))
		emit(result(nil))
	case "unstreamed": // the text arrives only in the final message
		emit(textDelta("He"))
		emit(assistant(map[string]any{"type": "text", "text": "Hello"}))
		emit(result(nil))
	case "tool":
		emit(stopDelta("tool_use"))
		emit(assistant(map[string]any{"type": "thinking", "thinking": "hm", "signature": "sig"}))
		emit(assistant(map[string]any{"type": "text", "text": "Reading."}))
		emit(assistant(map[string]any{"type": "tool_use", "id": "toolu_1", "name": "mcp__xhaos__read_file", "input": map[string]any{"path": "a.txt"}}))
		emit(result(map[string]any{"subtype": "error_max_turns", "is_error": true, "stop_reason": "tool_use"}))
		os.Exit(1)
	case "loggedout":
		emit(map[string]any{"type": "assistant", "error": "authentication_failed", "message": map[string]any{"id": "x", "role": "assistant", "model": "<synthetic>",
			"content": []any{map[string]any{"type": "text", "text": "Not logged in · Please run /login"}}}})
		emit(result(map[string]any{"is_error": true, "stop_reason": "stop_sequence", "result": "Not logged in · Please run /login"}))
	case "ratelimit":
		emit(map[string]any{"type": "assistant", "error": "rate_limit", "message": map[string]any{"id": "x", "role": "assistant", "model": "<synthetic>",
			"content": []any{map[string]any{"type": "text", "text": "You've hit your session limit"}}}})
		emit(result(map[string]any{"is_error": true}))
	case "toolong":
		emit(map[string]any{"type": "assistant", "error": "invalid_request", "message": map[string]any{"id": "x", "role": "assistant", "model": "<synthetic>",
			"content": []any{map[string]any{"type": "text", "text": "Prompt is too long"}}}})
		emit(result(map[string]any{"is_error": true}))
	case "failed":
		emit(result(map[string]any{"subtype": "error_during_execution", "is_error": true, "result": "it broke"}))
		os.Exit(1)
	case "crash":
		fmt.Fprintln(os.Stderr, "boom: out of luck")
		os.Exit(3)
	case "garbage":
		fmt.Println("Welcome banner, not JSON")
	case "hang", "slow":
		emit(textDelta("partial"))
		// A child in the same group, as the node program behind an npm shim is.
		child := exec.Command("sleep", "60")
		_ = child.Start()
		_ = os.WriteFile(os.Getenv("FAKE_PIDS"), []byte(strconv.Itoa(os.Getpid())+" "+strconv.Itoa(child.Process.Pid)), 0o600)
		time.Sleep(time.Minute)
	}
}

type seen struct {
	Args   []string
	Env    []string
	Frames []map[string]any
	Cwd    string
	System string
}

func (s seen) flag(name string) string {
	for i, a := range s.Args {
		if a == name && i+1 < len(s.Args) {
			return s.Args[i+1]
		}
	}
	return "\x00missing"
}

func (s seen) env(key string) (string, bool) {
	for _, kv := range s.Env {
		if v, ok := strings.CutPrefix(kv, key+"="); ok {
			return v, true
		}
	}
	return "", false
}

// adapter returns an adapter whose Claude Code is this test binary doing scenario.
func adapter(t *testing.T, scenario string, extra ...string) (Adapter, string) {
	t.Helper()
	dir := t.TempDir()
	self, _ := os.Executable()
	log := filepath.Join(dir, "log.json")
	env := append(os.Environ(), "FAKE_SCENARIO="+scenario, "FAKE_LOG="+log, "FAKE_PIDS="+filepath.Join(dir, "pids"))
	return Adapter{Command: self, Self: self, Env: append(env, extra...)}, dir
}

func read(t *testing.T, dir string) seen {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "log.json"))
	if err != nil {
		t.Fatal(err)
	}
	var s seen
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

var readFile = llm.Tool{Name: "read_file", Description: "Read a file.", Schema: llm.ToolSchema{
	Properties: map[string]any{"path": map[string]any{"type": "string"}}, Required: []string{"path"}}}

func ask(text string) llm.Turn {
	return llm.Turn{Model: "claude-sonnet-5-5", System: "You are a test.", Messages: []llm.Message{{Role: "user", Text: text}}}
}

func TestTextTurn(t *testing.T) {
	a, dir := adapter(t, "text", "ANTHROPIC_API_KEY=sk-leak", "ANTHROPIC_AUTH_TOKEN=tok-leak")
	var deltas []string
	res, err := a.Stream(context.Background(), llm.Credentials{}, ask("hi"), func(s string) { deltas = append(deltas, s) })
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "Hello" || strings.Join(deltas, "|") != "Hel|lo" {
		t.Fatalf("text %q deltas %q", res.Text, deltas)
	}
	if res.StopReason != "end_turn" || res.InputTokens != 352 || res.CacheReadTokens != 300 || res.CacheWriteTokens != 40 || res.OutputTokens != 7 {
		t.Fatalf("result %+v", res)
	}
	var raw []map[string]any
	if json.Unmarshal(res.Raw, &raw) != nil || len(raw) != 1 {
		t.Fatalf("raw %s", res.Raw)
	}
	got := read(t, dir)
	for flag, want := range map[string]string{"--model": "claude-sonnet-5-5", "--input-format": "stream-json", "--output-format": "stream-json",
		"--tools": "", "--max-turns": "1", "--permission-mode": "dontAsk", "--setting-sources": "", "--effort": "high", "--mcp-config": `{"mcpServers":{}}`} {
		if v := got.flag(flag); v != want {
			t.Errorf("%s = %q, want %q", flag, v, want)
		}
	}
	for _, flag := range []string{"--no-session-persistence", "--strict-mcp-config", "--include-partial-messages", "--disable-slash-commands"} {
		if got.flag(flag) == "" && !contains(got.Args, flag) {
			t.Errorf("missing %s", flag)
		}
	}
	if got.System != "You are a test." {
		t.Errorf("system prompt %q", got.System)
	}
	if _, ok := got.env("ANTHROPIC_API_KEY"); ok {
		t.Error("the API key reached Claude Code")
	}
	if _, ok := got.env("ANTHROPIC_AUTH_TOKEN"); ok {
		t.Error("the auth token reached Claude Code")
	}
	if v, _ := got.env("DISABLE_AUTO_COMPACT"); v != "1" {
		t.Error("Claude Code may compact the history itself")
	}
	if len(got.Frames) != 1 || got.Frames[0]["type"] != "user" || got.Frames[0]["shouldQuery"] != nil {
		t.Fatalf("frames %v", got.Frames)
	}
	if _, err := os.Stat(got.flag("--system-prompt-file")); !os.IsNotExist(err) {
		t.Error("the turn's files were not removed")
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestSystemPromptAndWorkdir(t *testing.T) {
	a, dir := adapter(t, "text")
	if _, err := a.Stream(context.Background(), llm.Credentials{}, ask("hi"), nil); err != nil {
		t.Fatal(err)
	}
	first := read(t, dir)
	if !strings.Contains(first.Cwd, "xhaos-claude") || !ours(first.Cwd) {
		t.Errorf("cwd %q", first.Cwd)
	}
	a2, dir2 := adapter(t, "text")
	if _, err := a2.Stream(context.Background(), llm.Credentials{}, ask("hi"), nil); err != nil {
		t.Fatal(err)
	}
	second := read(t, dir2)
	if first.Cwd != second.Cwd {
		t.Errorf("the working folder moved between turns: %q, %q", first.Cwd, second.Cwd)
	}
	if first.flag("--session-id") != second.flag("--session-id") {
		t.Error("one conversation got two session ids")
	}
	other := ask("something else")
	a3, dir3 := adapter(t, "text")
	if _, err := a3.Stream(context.Background(), llm.Credentials{}, other, nil); err != nil {
		t.Fatal(err)
	}
	if read(t, dir3).flag("--session-id") == first.flag("--session-id") {
		t.Error("two conversations share a session id")
	}
}

func TestToolTurnAndReplay(t *testing.T) {
	a, dir := adapter(t, "tool")
	turn := ask("read a.txt")
	turn.Tools = []llm.Tool{readFile}
	res, err := a.Stream(context.Background(), llm.Credentials{}, turn, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Calls) != 1 || res.Calls[0].Name != "read_file" || res.Calls[0].ID != "toolu_1" || string(res.Calls[0].Input) != `{"path":"a.txt"}` {
		t.Fatalf("calls %+v", res.Calls)
	}
	if res.Text != "Reading." || res.StopReason != "tool_use" {
		t.Fatalf("text %q stop %q", res.Text, res.StopReason)
	}
	got := read(t, dir)
	var cfg struct {
		McpServers map[string]struct {
			Command string
			Args    []string
		}
	}
	if err := json.Unmarshal([]byte(got.flag("--mcp-config")), &cfg); err != nil || cfg.McpServers["xhaos"].Args[0] != "claude-mcp" {
		t.Fatalf("mcp config %s (%v)", got.flag("--mcp-config"), err)
	}

	// The reply is replayed as Claude Code made it; its tool result follows; a new question ends it.
	next := llm.Turn{Model: turn.Model, System: turn.System, Tools: turn.Tools, Messages: []llm.Message{
		{Role: "user", Text: "read a.txt"},
		{Role: "assistant", RawKind: Kind, Raw: res.Raw, Text: res.Text, Calls: res.Calls},
		{Role: "user", Results: []llm.ToolResult{{CallID: "toolu_1", Content: "contents"}}},
		{Role: "assistant", Text: "From another provider.", Calls: []llm.ToolCall{{ID: "t2", Name: "read_file", Input: json.RawMessage(`{"path":"b"}`)}}},
		{Role: "user", Results: []llm.ToolResult{{CallID: "t2", Content: "nope", IsError: true}}},
		{Role: "user", Text: "and now?"},
	}}
	a2, dir2 := adapter(t, "text")
	if _, err := a2.Stream(context.Background(), llm.Credentials{}, next, nil); err != nil {
		t.Fatal(err)
	}
	frames := read(t, dir2).Frames
	var kinds []string
	for _, f := range frames {
		k := f["type"].(string)
		if q, ok := f["shouldQuery"].(bool); ok && !q {
			k += "(restore)"
		}
		kinds = append(kinds, k)
	}
	// user, the two assistant messages (thinking, text, tool_use come as three), tool result, another provider's call, results+question merged
	want := "user(restore) assistant assistant assistant user(restore) assistant user"
	if strings.Join(kinds, " ") != want {
		t.Fatalf("frames %v, want %s", kinds, want)
	}
	if _, has := frames[1]["message"].(map[string]any)["usage"]; has {
		t.Error("usage was replayed")
	}
	thinking := frames[1]["message"].(map[string]any)["content"].([]any)[0].(map[string]any)
	if thinking["type"] != "thinking" || thinking["signature"] != "sig" {
		t.Errorf("thinking not replayed verbatim: %v", thinking)
	}
	call := frames[5]["message"].(map[string]any)["content"].([]any)[1].(map[string]any)
	if call["name"] != "mcp__xhaos__read_file" {
		t.Errorf("another provider's call replayed as %v", call["name"])
	}
	last := frames[6]["message"].(map[string]any)["content"].([]any)
	if len(last) != 2 || last[0].(map[string]any)["type"] != "tool_result" || last[0].(map[string]any)["is_error"] != true || last[1].(map[string]any)["text"] != "and now?" {
		t.Errorf("results must come first, then the words: %v", last)
	}

	// StripThinking drops the signed blocks and keeps the rest.
	next.Messages[1].StripThinking = true
	a3, dir3 := adapter(t, "text")
	if _, err := a3.Stream(context.Background(), llm.Credentials{}, next, nil); err != nil {
		t.Fatal(err)
	}
	for _, f := range read(t, dir3).Frames {
		if f["type"] != "assistant" {
			continue
		}
		for _, b := range f["message"].(map[string]any)["content"].([]any) {
			if b.(map[string]any)["type"] == "thinking" {
				t.Fatal("thinking was replayed")
			}
		}
	}
}

func TestSmallTurn(t *testing.T) {
	a, dir := adapter(t, "text")
	turn := ask("title please")
	turn.Small = true
	if _, err := a.Stream(context.Background(), llm.Credentials{}, turn, nil); err != nil {
		t.Fatal(err)
	}
	got := read(t, dir)
	if got.flag("--effort") != "low" {
		t.Errorf("effort %q", got.flag("--effort"))
	}
	if v, _ := got.env("CLAUDE_CODE_MAX_OUTPUT_TOKENS"); v != "1024" {
		t.Errorf("max output %q", v)
	}
}

func TestFilesGoAsBlocks(t *testing.T) {
	a, dir := adapter(t, "text")
	turn := ask("see")
	turn.Messages[0].Files = []llm.File{{Name: "a.png", Type: "image/png", Data: []byte("png")}, {Name: "a.pdf", Type: "application/pdf", Data: []byte("pdf")}}
	if _, err := a.Stream(context.Background(), llm.Credentials{}, turn, nil); err != nil {
		t.Fatal(err)
	}
	blocks := read(t, dir).Frames[0]["message"].(map[string]any)["content"].([]any)
	if len(blocks) != 3 || blocks[1].(map[string]any)["type"] != "image" || blocks[2].(map[string]any)["type"] != "document" ||
		blocks[1].(map[string]any)["source"].(map[string]any)["data"] != "cG5n" {
		t.Fatalf("blocks %v", blocks)
	}
}

func TestUnstreamedTextIsDelivered(t *testing.T) {
	a, _ := adapter(t, "unstreamed")
	var got strings.Builder
	res, err := a.Stream(context.Background(), llm.Credentials{}, ask("hi"), func(s string) { got.WriteString(s) })
	if err != nil || res.Text != "Hello" || got.String() != "Hello" {
		t.Fatalf("%v %q %q", err, res.Text, got.String())
	}
}

func TestFailures(t *testing.T) {
	for _, c := range []struct {
		scenario string
		want     string
		is       error
	}{
		{"loggedout", "claude auth login", ErrLoggedOut},
		{"ratelimit", "session limit", nil},
		{"toolong", "Prompt is too long", nil},
		{"failed", "it broke", nil},
		{"crash", "boom: out of luck", nil},
		{"garbage", "not stream-json", nil},
	} {
		a, _ := adapter(t, c.scenario)
		_, err := a.Stream(context.Background(), llm.Credentials{}, ask("hi"), nil)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.scenario, err, c.want)
		}
		if c.is != nil && !errors.Is(err, c.is) {
			t.Errorf("%s: %v is not %v", c.scenario, err, c.is)
		}
	}
}

func TestRefusesOtherBackends(t *testing.T) {
	for _, kv := range []string{"ANTHROPIC_BASE_URL=http://proxy", "CLAUDE_CODE_USE_BEDROCK=1"} {
		a, _ := adapter(t, "text", kv)
		if _, err := a.Stream(context.Background(), llm.Credentials{}, ask("hi"), nil); err == nil || !strings.Contains(err.Error(), "subscription") {
			t.Errorf("%s: %v", kv, err)
		}
	}
	a, _ := adapter(t, "text", "CLAUDE_CODE_USE_BEDROCK=0")
	if _, err := a.Stream(context.Background(), llm.Credentials{}, ask("hi"), nil); err != nil {
		t.Errorf("an off switch is not a conflict: %v", err)
	}
}

func TestNotInstalled(t *testing.T) {
	a := Adapter{Command: "/nonexistent/claude", Env: []string{"HOME=/nonexistent"}}
	if _, err := a.Stream(context.Background(), llm.Credentials{}, ask("hi"), nil); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("%v", err)
	}
	if _, err := a.Models(context.Background(), llm.Credentials{}); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("%v", err)
	}
}

func TestModels(t *testing.T) {
	a, _ := adapter(t, "text")
	models, err := a.Models(context.Background(), llm.Credentials{})
	if err != nil || strings.Join(models, ",") != strings.Join(DefaultModels, ",") {
		t.Fatalf("%v %v", models, err)
	}
	a, _ = adapter(t, "loggedout")
	if _, err := a.Models(context.Background(), llm.Credentials{}); !errors.Is(err, ErrLoggedOut) {
		t.Fatalf("%v", err)
	}
}

func pids(t *testing.T, dir string) (int, int) {
	t.Helper()
	var raw []byte
	for i := 0; i < 100; i++ {
		var err error
		if raw, err = os.ReadFile(filepath.Join(dir, "pids")); err == nil && len(strings.Fields(string(raw))) == 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	f := strings.Fields(string(raw))
	if len(f) != 2 {
		t.Fatalf("no pids: %q", raw)
	}
	a, _ := strconv.Atoi(f[0])
	b, _ := strconv.Atoi(f[1])
	return a, b
}

func gone(pid int) bool {
	for i := 0; i < 100; i++ {
		if err := syscall.Kill(pid, 0); err != nil {
			return true
		}
		// A killed child of the fake is reparented and reaped by init; a zombie still answers.
		if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil && strings.Contains(string(data), ") Z") {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func TestCancelKillsTheGroup(t *testing.T) {
	a, dir := adapter(t, "slow")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	var mu sync.Mutex
	var deltas []string
	go func() {
		_, err := a.Stream(ctx, llm.Credentials{}, ask("hi"), func(s string) { mu.Lock(); deltas = append(deltas, s); mu.Unlock() })
		done <- err
	}()
	parent, child := pids(t, dir)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stream did not return after cancel")
	}
	if !gone(parent) || !gone(child) {
		t.Errorf("claude (%d) or its child (%d) survived the cancel", parent, child)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(deltas, "") != "partial" {
		t.Errorf("deltas %v", deltas)
	}
}

func TestSilenceIsGivenUp(t *testing.T) {
	a, dir := adapter(t, "hang")
	a.Idle = 3 * time.Second
	start := time.Now()
	_, err := a.Stream(context.Background(), llm.Credentials{}, ask("hi"), nil)
	if err == nil || !strings.Contains(err.Error(), "silent") {
		t.Fatalf("%v", err)
	}
	if time.Since(start) > 15*time.Second {
		t.Error("too slow")
	}
	parent, child := pids(t, dir)
	if !gone(parent) || !gone(child) {
		t.Errorf("claude (%d) or its child (%d) survived", parent, child)
	}
}

func TestConversationMustEndWithUser(t *testing.T) {
	a, _ := adapter(t, "text")
	turn := ask("hi")
	turn.Messages = append(turn.Messages, llm.Message{Role: "assistant", Text: "yes"})
	if _, err := a.Stream(context.Background(), llm.Credentials{}, turn, nil); err == nil {
		t.Fatal("an assistant message cannot be the last")
	}
}

func TestLongToolNamesAreShortenedAndMappedBack(t *testing.T) {
	long := strings.Repeat("x", 60)
	n, err := toolNames([]llm.Tool{{Name: long}, {Name: "read_file"}})
	if err != nil {
		t.Fatal(err)
	}
	wire := n.wire(long)
	if len(strings.TrimPrefix(wire, toolPrefix)) > maxToolName {
		t.Errorf("%q is too long", wire)
	}
	if back, ok := n.ours(wire); !ok || back != long {
		t.Errorf("%q came back as %q, %v", wire, back, ok)
	}
	if back, ok := n.ours("read_file"); !ok || back != "read_file" { // prefix dropped by Claude Code
		t.Errorf("dropped prefix: %q %v", back, ok)
	}
	if back, ok := n.ours(toolPrefix + "unknown"); ok || back != "unknown" {
		t.Errorf("unknown: %q %v", back, ok)
	}
	if _, err := toolNames([]llm.Tool{{Name: "bad name"}}); err == nil {
		t.Error("a name with a space is not valid MCP")
	}
}

func TestMCPServer(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tools.json")
	manifest, _ := json.Marshal([]manifestTool{{Name: "read_file", Description: "d", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}}}})
	if err := os.WriteFile(file, manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}
{"jsonrpc":"2.0","method":"notifications/initialized"}
{"jsonrpc":"2.0","id":2,"method":"tools/list"}
{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file"}}
{"jsonrpc":"2.0","id":4,"method":"nope"}
`)
	var out bytes.Buffer
	if err := ServeMCP(in, &out, file); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("a notification got an answer, or one was missing:\n%s", out.String())
	}
	for i, want := range []string{`"protocolVersion":"2025-03-26"`, `"name":"read_file"`, `"isError":true`, `-32601`} {
		if !strings.Contains(lines[i], want) {
			t.Errorf("line %d lacks %s: %s", i, want, lines[i])
		}
	}
}

func TestMCPServerAsChild(t *testing.T) {
	// What Claude Code does: start `<self> claude-mcp <file>` and talk over its stdio.
	self, _ := os.Executable()
	file := filepath.Join(t.TempDir(), "tools.json")
	_ = os.WriteFile(file, []byte(`[{"name":"a","description":"","inputSchema":{"type":"object","properties":{}}}]`), 0o600)
	cmd := exec.Command(self, "claude-mcp", file)
	cmd.Stdin = strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n")
	out, err := cmd.Output()
	if err != nil || !strings.Contains(string(out), `"name":"a"`) {
		t.Fatalf("%v %s", err, out)
	}
}

// TestRealClaude runs the adapter against the real Claude Code: set
// XHAOS_REAL_CLAUDE=1, with `claude` signed in as this user (or
// XHAOS_CLAUDE_COMMAND pointing at it). It uses the subscription: two short
// requests. Logged out, it still proves the flags and the history replay are
// accepted, because the login error comes back from the request itself.
func TestRealClaude(t *testing.T) {
	if os.Getenv("XHAOS_REAL_CLAUDE") == "" {
		t.Skip("set XHAOS_REAL_CLAUDE=1 to run against the real Claude Code")
	}
	a := Adapter{}
	if _, err := a.Models(context.Background(), llm.Credentials{}); err != nil {
		t.Logf("Models: %v", err)
	}
	turn := llm.Turn{Model: "sonnet", System: "Answer in as few words as you can.", Tools: []llm.Tool{readFile},
		Messages: []llm.Message{
			{Role: "user", Text: "Hello"},
			{Role: "assistant", Text: "Hello."},
			{Role: "user", Text: "Call the read_file tool on notes.txt."},
		}}
	res, err := a.Stream(context.Background(), llm.Credentials{}, turn, nil)
	if errors.Is(err, ErrLoggedOut) {
		t.Skipf("not signed in, but Claude Code accepted the request and said so: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("calls %+v text %q stop %s tokens in %d out %d", res.Calls, res.Text, res.StopReason, res.InputTokens, res.OutputTokens)
	if len(res.Calls) != 1 || res.Calls[0].Name != "read_file" {
		t.Fatalf("expected a read_file call, got %+v", res)
	}
	turn.Messages = append(turn.Messages,
		llm.Message{Role: "assistant", RawKind: Kind, Raw: res.Raw, Text: res.Text, Calls: res.Calls},
		llm.Message{Role: "user", Results: []llm.ToolResult{{CallID: res.Calls[0].ID, Content: "The notes say: pineapple."}}})
	res, err = a.Stream(context.Background(), llm.Credentials{}, turn, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("answer %q cache read %d", res.Text, res.CacheReadTokens)
	if !strings.Contains(strings.ToLower(res.Text), "pineapple") {
		t.Errorf("the tool result did not reach the model: %q", res.Text)
	}
}
