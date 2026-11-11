// Package claudecli runs a model turn through the Claude Code program
// (`claude`), signed in with the owner's Claude subscription, and adapts it to
// llm. Xhaos sends the whole conversation each turn as stream-json input,
// with Claude Code's own tools switched off, and runs every tool itself: Claude
// Code only decides what to answer or call. It is one process per turn, in its
// own process group, that dies with the request.
package claudecli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/devark28/xhaos/app/llm"
)

const Kind = "claude-cli"

// DefaultModels are offered when a provider config lists none. A plain id runs
// in Claude Code's 200,000-token window; the 1M window is chosen by naming the
// model with a `[1m]` suffix (and setting the provider's context size).
var DefaultModels = []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5"}

// defaultIdle is how long Claude Code may say nothing before the turn is given
// up. Any event, even a thinking delta, restarts it.
const defaultIdle = 3 * time.Minute

type Adapter struct {
	// Command is the Claude Code program; empty: $XHAOS_CLAUDE_COMMAND, then `claude`.
	Command string
	// Env is the environment Claude Code starts from; nil: this process's.
	Env []string
	// Self is the program that serves the tool list to Claude Code, started as
	// `Self claude-mcp <file>`; empty: this executable (cmd/xhaos).
	Self string
	// Idle overrides how long a silent Claude Code is waited for.
	Idle time.Duration
}

func (Adapter) Kind() string { return Kind }

// KeyOptional reports that this kind has no API key: Claude Code holds the login.
func (Adapter) KeyOptional() bool { return true }

// Verified is what a passing Test says: it checked the login, not a key.
func (Adapter) Verified() string { return "Claude Code is signed in" }

func (a Adapter) command() string {
	if a.Command != "" {
		return a.Command
	}
	return os.Getenv(CommandEnv)
}

func (a Adapter) baseEnv() []string {
	if a.Env != nil {
		return a.Env
	}
	return os.Environ()
}

// Models checks that Claude Code is installed and logged in (`claude auth
// status` reads the local login and sends nothing), then lists the pinned models.
func (a Adapter) Models(ctx context.Context, _ llm.Credentials) ([]string, error) {
	env, err := childEnv(a.baseEnv())
	if err != nil {
		return nil, err
	}
	program, err := resolve(a.command(), env)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, program, "auth", "status")
	cmd.Env = env
	cmd.Stdin = nil
	out, err := cmd.Output()
	var status struct {
		LoggedIn         bool   `json:"loggedIn"`
		SubscriptionType string `json:"subscriptionType"`
	}
	if jerr := json.Unmarshal(bytes.TrimSpace(out), &status); jerr != nil {
		if ctx.Err() != nil {
			return nil, errors.New("`claude auth status` did not answer in time")
		}
		if err != nil {
			return nil, fmt.Errorf("`claude auth status` failed: %v", err)
		}
		return nil, errors.New("`claude auth status` gave no readable answer")
	}
	if !status.LoggedIn {
		return nil, ErrLoggedOut
	}
	return append([]string(nil), DefaultModels...), nil
}

// event is one stream-json output line, with only what is read from it.
type event struct {
	Type    string          `json:"type"`
	Subtype string          `json:"subtype"`
	Error   string          `json:"error"`
	Message json.RawMessage `json:"message"`
	Event   struct {
		Type  string `json:"type"`
		Delta struct {
			Type       string `json:"type"`
			Text       string `json:"text"`
			StopReason string `json:"stop_reason"`
		} `json:"delta"`
	} `json:"event"`
	// result
	IsError    bool            `json:"is_error"`
	NumTurns   *int            `json:"num_turns"`
	StopReason string          `json:"stop_reason"`
	Result     string          `json:"result"`
	Usage      json.RawMessage `json:"usage"`
}

type usage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
}

func (a Adapter) Stream(ctx context.Context, _ llm.Credentials, turn llm.Turn, onDelta func(string)) (llm.Result, error) {
	if turn.Model == "" {
		return llm.Result{}, errors.New("a model is required")
	}
	n, err := toolNames(turn.Tools)
	if err != nil {
		return llm.Result{}, err
	}
	in, err := frames(turn.Messages, n)
	if err != nil {
		return llm.Result{}, err
	}
	env, err := childEnv(a.baseEnv(),
		"ENABLE_TOOL_SEARCH=false",              // every tool is described in full, never looked up later
		"DISABLE_AUTO_COMPACT=1",                // Xhaos summarizes the conversation itself
		"DISABLE_COMPACT=1",                     //
		"CLAUDE_CODE_TOTAL_TOKENS_REMINDER=off", // it would change replayed history
		"CLAUDE_CODE_DISABLE_AUTO_MEMORY=1",     // Xhaos owns memory and instructions
		"CLAUDE_CODE_DISABLE_CLAUDE_MDS=1",
		"DISABLE_AUTOUPDATER=1", // never change the owner's install mid-session
		"DISABLE_FEEDBACK_COMMAND=1",
		"CLAUDE_CODE_DISABLE_TERMINAL_TITLE=1", // its own title request would be an extra call
	)
	if err != nil {
		return llm.Result{}, err
	}
	program, err := resolve(a.command(), env)
	if err != nil {
		return llm.Result{}, err
	}
	cwd, err := workDir()
	if err != nil {
		return llm.Result{}, err
	}
	tmp, err := os.MkdirTemp("", "xhaos-claude-turn-")
	if err != nil {
		return llm.Result{}, err
	}
	defer os.RemoveAll(tmp)

	mcp := `{"mcpServers":{}}`
	if len(turn.Tools) > 0 {
		self := a.Self
		if self == "" {
			if self, err = os.Executable(); err != nil {
				return llm.Result{}, err
			}
		}
		manifest := make([]manifestTool, 0, len(turn.Tools))
		for _, t := range turn.Tools {
			schema := map[string]any{"type": "object", "properties": t.Schema.Properties}
			if t.Schema.Properties == nil {
				schema["properties"] = map[string]any{}
			}
			if len(t.Schema.Required) > 0 {
				schema["required"] = t.Schema.Required
			}
			manifest = append(manifest, manifestTool{Name: n.native[t.Name], Description: t.Description, InputSchema: schema})
		}
		raw, _ := json.Marshal(manifest)
		file := filepath.Join(tmp, "tools.json")
		if err := os.WriteFile(file, raw, 0o600); err != nil {
			return llm.Result{}, err
		}
		cfg, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"xhaos": map[string]any{"command": self, "args": []string{mcpArg, file}}}})
		mcp = string(cfg)
	}
	systemFile := filepath.Join(tmp, "system.md")
	if err := os.WriteFile(systemFile, []byte(turn.System), 0o600); err != nil {
		return llm.Result{}, err
	}
	effort := "high"
	if turn.Small {
		effort = "low"
		env = append(env, "CLAUDE_CODE_MAX_OUTPUT_TOKENS=1024")
	}
	args := []string{"-p", "--model", turn.Model,
		"--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--include-partial-messages",
		"--tools", "", // none of Claude Code's own; Xhaos' come through the MCP server
		"--system-prompt-file", systemFile,
		"--setting-sources", "", "--strict-mcp-config", "--mcp-config", mcp, "--disable-slash-commands",
		"--max-turns", "1", // a tool call ends the turn; Xhaos runs it and asks again
		"--permission-mode", "dontAsk", "--no-session-persistence",
		"--effort", effort,
		"--session-id", sessionID(turn.System, in[0]),
	}
	return a.run(ctx, program, args, env, cwd, in, n, turn.Model, onDelta)
}

// proc is one running Claude Code and the lines it writes.
type proc struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	lines    chan []byte
	exited   chan struct{}
	stderr   ring
	stopRead chan struct{}
	killOnce sync.Once
	timedOut atomic.Bool
}

// kill ends Claude Code and everything it started: the node program behind an
// npm shim holds the request open, so the whole process group goes.
func (p *proc) kill() {
	p.killOnce.Do(func() {
		if p.cmd.Process != nil {
			_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
		}
	})
}

func start(program string, args, env []string, cwd string) (*proc, error) {
	cmd := exec.Command(program, args...)
	cmd.Dir, cmd.Env = cwd, env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	p := &proc{cmd: cmd, stdin: stdin, lines: make(chan []byte, 1024), exited: make(chan struct{}), stopRead: make(chan struct{})}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	var readers sync.WaitGroup
	readers.Add(2)
	go func() { // stderr: the last lines explain a failure
		defer readers.Done()
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
		for sc.Scan() {
			p.stderr.add(sc.Text())
		}
		_, _ = io.Copy(io.Discard, stderr)
	}()
	go func() {
		defer readers.Done()
		defer close(p.lines)
		br := bufio.NewReaderSize(stdout, 64<<10)
		for {
			line, err := br.ReadBytes('\n')
			if len(bytes.TrimSpace(line)) > 0 {
				select {
				case p.lines <- line:
				case <-p.stopRead:
					_, _ = io.Copy(io.Discard, br) // keep Claude Code from blocking on a full pipe
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		readers.Wait() // Wait closes the pipes: only after everything is read
		_ = cmd.Wait()
		close(p.exited)
	}()
	return p, nil
}

func (a Adapter) run(ctx context.Context, program string, args, env []string, cwd string, in []frame, n names, model string, onDelta func(string)) (llm.Result, error) {
	p, err := start(program, args, env, cwd)
	if err != nil {
		return llm.Result{}, err
	}
	defer func() {
		close(p.stopRead)
		p.kill()
		<-p.exited
	}()
	idle := a.Idle
	if idle <= 0 {
		idle = defaultIdle
	}
	watchdog := time.AfterFunc(idle, func() { p.timedOut.Store(true); p.kill() })
	defer watchdog.Stop()
	go func() { // cancelling the request ends Claude Code at once
		select {
		case <-ctx.Done():
			p.kill()
		case <-p.exited:
		}
	}()

	// fail names why the turn ended early: the owner's cancel, the idle limit, or
	// Claude Code exiting by itself.
	fail := func(what string) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if p.timedOut.Load() {
			return fmt.Errorf("Claude Code was silent for %s and was stopped%s", idle.Round(time.Second), p.stderr.tail())
		}
		select { // let its last words reach the stderr reader
		case <-p.exited:
		case <-time.After(time.Second):
		}
		return fmt.Errorf("%s%s", what, p.stderr.tail())
	}
	// next waits for the next line; nil means Claude Code closed its output.
	next := func() (*event, error) {
		line, ok := <-p.lines
		if !ok {
			return nil, nil
		}
		watchdog.Reset(idle)
		var ev event
		if err := json.Unmarshal(line, &ev); err != nil {
			s := string(bytes.TrimSpace(line))
			if len(s) > 300 {
				s = s[:300]
			}
			return nil, fail(fmt.Sprintf("Claude Code wrote something that is not stream-json: %q", s))
		}
		return &ev, nil
	}

	for _, f := range in {
		raw, err := json.Marshal(f)
		if err != nil {
			return llm.Result{}, err
		}
		if _, err := p.stdin.Write(append(raw, '\n')); err != nil {
			return llm.Result{}, fail("Claude Code stopped reading its input")
		}
		if f.ShouldQuery == nil || *f.ShouldQuery {
			continue
		}
		// A restored user message is answered by a result with no turns.
		for {
			ev, err := next()
			if err != nil {
				return llm.Result{}, err
			}
			if ev == nil {
				return llm.Result{}, fail("Claude Code exited before it restored the conversation")
			}
			if ev.Type != "result" {
				continue
			}
			if (ev.NumTurns != nil && *ev.NumTurns != 0) || ev.IsError {
				return llm.Result{}, fail("Claude Code could not restore the conversation history")
			}
			break
		}
	}
	_ = p.stdin.Close()

	var (
		assistants         []map[string]any
		nativeErr, errCode string
		emitted            strings.Builder
		stopReason         string
		result             *event
	)
read:
	for {
		ev, err := next()
		if err != nil {
			return llm.Result{}, err
		}
		if ev == nil {
			break
		}
		switch ev.Type {
		case "assistant":
			var msg map[string]any
			if err := json.Unmarshal(ev.Message, &msg); err != nil {
				return llm.Result{}, fail("Claude Code sent an unreadable message")
			}
			if e := msg["error"]; (e != nil && e != "") || ev.Error != "" {
				// An error is reported as a message from "<synthetic>": its text is the detail.
				nativeErr, errCode = textOf(msg), ev.Error
			} else {
				assistants = append(assistants, msg)
			}
		case "stream_event":
			switch d := ev.Event.Delta; {
			case ev.Event.Type == "content_block_delta" && d.Type == "text_delta":
				emitted.WriteString(d.Text)
				if onDelta != nil && d.Text != "" {
					onDelta(d.Text)
				}
			case ev.Event.Type == "message_delta" && d.StopReason != "":
				stopReason = d.StopReason
			}
		case "result":
			result = ev
			break read
		}
	}
	if result != nil { // it is the last thing Claude Code says; give it a moment to exit, then end it
		select {
		case <-p.exited:
		case <-time.After(5 * time.Second):
			p.kill()
		}
	}

	if nativeErr != "" || errCode != "" {
		if errCode == "authentication_failed" {
			return llm.Result{}, fmt.Errorf("%w (Claude Code said: %s)", ErrLoggedOut, nativeErr)
		}
		return llm.Result{}, fail(fmt.Sprintf("Claude Code: %s%s", nativeErr, codeNote(errCode)))
	}
	if result == nil {
		return llm.Result{}, fail("Claude Code ended without an answer")
	}
	var text strings.Builder
	var calls []llm.ToolCall
	for _, msg := range assistants {
		blocks, _ := msg["content"].([]any)
		for _, b := range blocks {
			blk, _ := b.(map[string]any)
			switch blk["type"] {
			case "text":
				s, _ := blk["text"].(string)
				text.WriteString(s)
			case "tool_use":
				name, _ := blk["name"].(string)
				xname, ours := n.ours(name)
				if ours {
					blk["name"] = toolPrefix + n.native[xname] // replay it as offered, prefix included
				}
				id, _ := blk["id"].(string)
				input, _ := json.Marshal(blk["input"])
				calls = append(calls, llm.ToolCall{ID: id, Name: xname, Input: input})
			}
		}
	}
	// A turn that stops at its one allowed turn after a tool call is the normal end of a
	// tool-calling turn. Anything else that is not a success is a failure.
	toolTurn := len(calls) > 0 && result.Subtype == "error_max_turns"
	if !toolTurn && (result.IsError || result.Subtype != "success") {
		detail := strings.TrimSpace(result.Result)
		if detail == "" {
			detail = "the turn failed (" + result.Subtype + ")"
		}
		return llm.Result{}, fail("Claude Code: " + detail)
	}
	if len(assistants) == 0 {
		return llm.Result{}, fail("Claude Code ended without an answer")
	}
	if got := text.String(); got != emitted.String() && onDelta != nil && strings.HasPrefix(got, emitted.String()) && len(got) > emitted.Len() {
		onDelta(got[emitted.Len():]) // text that was not streamed
	}
	var u usage
	if len(result.Usage) > 0 {
		_ = json.Unmarshal(result.Usage, &u)
	}
	if stopReason == "" {
		stopReason = result.StopReason
	}
	if stopReason == "" || stopReason == "stop_sequence" {
		stopReason = "end_turn"
		if len(calls) > 0 {
			stopReason = "tool_use"
		}
	}
	raw, err := json.Marshal(assistants)
	if err != nil {
		return llm.Result{}, err
	}
	return llm.Result{
		Text:             text.String(),
		Calls:            calls,
		StopReason:       stopReason,
		Raw:              raw,
		InputTokens:      u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens,
		CacheReadTokens:  u.CacheReadInputTokens,
		CacheWriteTokens: u.CacheCreationInputTokens,
		OutputTokens:     u.OutputTokens,
	}, nil
}

// textOf joins the text blocks of a message.
func textOf(msg map[string]any) string {
	var out []string
	blocks, _ := msg["content"].([]any)
	for _, b := range blocks {
		if blk, _ := b.(map[string]any); blk["type"] == "text" {
			s, _ := blk["text"].(string)
			out = append(out, s)
		}
	}
	return strings.Join(out, "\n")
}

// codeNote names Claude Code's own error code when it adds to the text.
func codeNote(code string) string {
	switch code {
	case "":
		return ""
	case "rate_limit":
		return " (subscription limit reached: wait for it to reset, or choose another model)"
	default:
		return " (" + code + ")"
	}
}
