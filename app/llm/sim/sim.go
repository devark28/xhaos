// Package sim is a simulated model: a scripted adapter that drives the real
// harness (tools, approvals, channels, subagents) without calling any
// provider or spending tokens. It is deterministic and stateless: each turn
// it reads the conversation and picks the next step.
//
// What it does depends on words in the task:
//   - always: list the project, then read one file;
//   - "split", "spawn" or "subagent": start two subagents and summarize their
//     final answers when both arrive;
//   - "contact #N" or "talk to #N": open a channel with agent #N (another
//     top-level agent's channel waits for your approval);
//   - "ask": ask you a question with ask_user;
//   - "write" or "note": write sim-notes.md (gated like any write);
//   - "run": run `git status` (a read-only command);
//   - "board": list the boards (creating one if there are none), add a note
//     and a small diagram to the first, plus the image at "image <path>"
//     when the task names one, then read the board back.
//
// A message from another agent gets one reply on its channel; replies are
// marked so two simulated agents never answer each other forever. A
// subagent reads a file and reports back.
package sim

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/devark28/xhaos/app/llm"
)

const Kind = "sim"

// Models: "scripted" streams at reading speed; "scripted-fast" without
// delays (tests).
var Models = []string{"scripted", "scripted-fast"}

type Adapter struct{}

func (Adapter) Kind() string      { return Kind }
func (Adapter) KeyOptional() bool { return true }
func (Adapter) Models(context.Context, llm.Credentials) ([]string, error) {
	return append([]string(nil), Models...), nil
}

// replyMark starts every channel reply, so a simulated agent never answers one.
const replyMark = "(simulated reply) "

var (
	contactRE = regexp.MustCompile(`(?i)\b(?:contact|talk to|message)\s+#(\d+)`)
	imageRE   = regexp.MustCompile(`(?i)\bimage\s+(\S+\.(?:png|jpe?g|gif|webp))\b`)
	boardRE   = regexp.MustCompile(`#(\d+)`)
	channelRE = regexp.MustCompile(`^\[channel #(\d+) .*?\] message from agent #(\d+) \(([^)]*)\):\n`)
)

func (a Adapter) Stream(ctx context.Context, _ llm.Credentials, turn llm.Turn, onDelta func(string)) (llm.Result, error) {
	fast := turn.Model == "scripted-fast"
	pause := func(d time.Duration) error {
		if fast {
			return ctx.Err()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d):
			return nil
		}
	}
	var res llm.Result
	if turn.Purpose == "title" {
		res = say(simTitle(turn))
	} else {
		res = next(turn)
	}
	if err := pause(400 * time.Millisecond); err != nil {
		return llm.Result{}, err
	}
	for i, w := range strings.SplitAfter(res.Text, " ") {
		if i > 0 {
			if err := pause(25 * time.Millisecond); err != nil {
				return llm.Result{}, err
			}
		}
		onDelta(w)
	}
	// Rough counts, so token and context views have something to show.
	in := len(turn.System)
	for _, m := range turn.Messages {
		in += len(m.Text)
		for _, r := range m.Results {
			in += len(r.Content)
		}
	}
	res.InputTokens, res.OutputTokens = int64(in/4), int64(len(res.Text)/4+20*len(res.Calls))
	return res, nil
}

// episode is the conversation since the last message that was not a tool
// result: what started this piece of work, and the calls made since.
type episode struct {
	trigger string         // the user or channel message that started it
	calls   []llm.ToolCall // calls made since, in order
	results map[string]llm.ToolResult
	history []llm.Message
}

func current(turn llm.Turn) episode {
	e := episode{results: map[string]llm.ToolResult{}, history: turn.Messages}
	start := 0
	for i, m := range turn.Messages {
		if m.Role == "user" && len(m.Results) == 0 && strings.TrimSpace(m.Text) != "" {
			start = i
		}
	}
	if len(turn.Messages) > 0 {
		e.trigger = turn.Messages[start].Text
	}
	for _, m := range turn.Messages[start:] {
		e.calls = append(e.calls, m.Calls...)
		for _, r := range m.Results {
			e.results[r.CallID] = r
		}
		// Text sent alongside results (an owner message while calls waited)
		// does not restart the episode.
	}
	return e
}

func (e episode) called(name string) []llm.ToolCall {
	var out []llm.ToolCall
	for _, c := range e.calls {
		if c.Name == name {
			out = append(out, c)
		}
	}
	return out
}

func (e episode) result(name string) (llm.ToolResult, bool) {
	cs := e.called(name)
	if len(cs) == 0 {
		return llm.ToolResult{}, false
	}
	r, ok := e.results[cs[len(cs)-1].ID]
	return r, ok
}

func has(turn llm.Turn, name string) bool {
	for _, t := range turn.Tools {
		if t.Name == name {
			return true
		}
	}
	return false
}

func call(id, name string, input map[string]any) llm.ToolCall {
	raw, _ := json.Marshal(input)
	return llm.ToolCall{ID: id, Name: name, Input: raw}
}

func say(text string) llm.Result { return llm.Result{Text: text, StopReason: "end_turn"} }

func use(text string, calls ...llm.ToolCall) llm.Result {
	return llm.Result{Text: text, Calls: calls, StopReason: "tool_use"}
}

// next picks the reply for this turn.
func next(turn llm.Turn) llm.Result {
	e := current(turn)
	n := len(turn.Messages)
	id := func(s string) string { return fmt.Sprintf("sim-%d-%s", n, s) }

	// A message from another agent.
	if m := channelRE.FindStringSubmatch(e.trigger); m != nil && !strings.Contains(e.trigger, "(You are a subagent") {
		body := e.trigger[len(m[0]):]
		switch {
		case strings.HasPrefix(body, "Final answer:"):
			return parentCollects(turn)
		case strings.HasPrefix(body, replyMark):
			return say(fmt.Sprintf("Agent #%s answered on channel #%s; nothing more to send.", m[2], m[1]))
		case len(e.called("channel_send")) == 0 && has(turn, "channel_send"):
			return use(fmt.Sprintf("Agent #%s (%s) wrote on channel #%s. Replying once.", m[2], m[3], m[1]),
				call(id("reply"), "channel_send", map[string]any{"channel": atoi(m[1]), "message": replyMark + "Thanks, noted: " + firstLine(body)}))
		}
		return say(fmt.Sprintf("Replied to agent #%s on channel #%s.", m[2], m[1]))
	}

	task := e.trigger
	low := strings.ToLower(task)
	subagent := strings.Contains(task, "(You are a subagent")

	// Look around first: list, then read one file.
	if has(turn, "list_dir") && len(e.called("list_dir")) == 0 {
		return use("Let me look at the project first.", call(id("ls"), "list_dir", map[string]any{}))
	}
	listing, _ := e.result("list_dir")
	file := pickFile(listing.Content)
	if file != "" && has(turn, "read_file") && len(e.called("read_file")) == 0 {
		return use("Reading "+file+".", call(id("read"), "read_file", map[string]any{"path": file, "limit": 40}))
	}
	read, _ := e.result("read_file")
	lines := strings.Count(read.Content, "\n")
	seen := "an empty project"
	if file != "" {
		seen = fmt.Sprintf("%s (%d lines shown)", file, lines)
	}

	if subagent {
		brief := firstLine(task)
		return say(fmt.Sprintf("Done with %q: I looked at %s. Nothing in it blocks the task; it is ready for the next step.", brief, seen))
	}

	if wants(low, "split", "spawn", "subagent") && has(turn, "spawn_agent") && len(e.called("spawn_agent")) == 0 {
		subject := firstLine(task)
		return use("This splits cleanly into two parts; starting a subagent for each.",
			call(id("sub1"), "spawn_agent", map[string]any{"title": "Part 1", "task": "Part 1 of: " + subject + ". Survey the project and report what matters."}),
			call(id("sub2"), "spawn_agent", map[string]any{"title": "Part 2", "task": "Part 2 of: " + subject + ". Check the details and report risks."}))
	}
	if wants(low, "board") && has(turn, "board_list") {
		if r, ok := boardTurn(e, id, task); ok {
			return r
		}
	}
	if m := contactRE.FindStringSubmatch(task); m != nil && has(turn, "channel_create") && len(e.called("channel_create")) == 0 {
		return use("Opening a channel with agent #"+m[1]+".",
			call(id("contact"), "channel_create", map[string]any{"subject": "coordination: " + firstLine(task), "members": []int{atoi(m[1])},
				"message": "Hi, I'm working on: " + firstLine(task) + ". Anything I should know before I start?"}))
	}
	if wants(low, "ask") && has(turn, "ask_user") && len(e.called("ask_user")) == 0 {
		return use("I need a decision before going on.", call(id("ask"), "ask_user", map[string]any{
			"title": "Before I continue",
			"questions": []map[string]any{{"prompt": "Which approach should I take?", "options": []map[string]any{
				{"label": "The small change", "description": "touch as little as possible"},
				{"label": "The thorough one", "description": "refactor while I am there"},
			}}},
		}))
	}
	if wants(low, "write", "note") && has(turn, "write_file") && len(e.called("write_file")) == 0 {
		return use("Writing my notes to sim-notes.md.", call(id("write"), "write_file", map[string]any{
			"path": "sim-notes.md", "content": "# Notes (simulated agent)\n\nTask: " + firstLine(task) + "\n\nLooked at " + seen + ".\n"}))
	}
	if wants(low, "run") && has(turn, "bash") && len(e.called("bash")) == 0 {
		return use("Checking the repository state.", call(id("run"), "bash", map[string]any{"command": "git status --short"}))
	}

	var b strings.Builder
	fmt.Fprintf(&b, "**Simulated agent.** I looked at %s.", seen)
	if subs := e.called("spawn_agent"); len(subs) > 0 {
		fmt.Fprintf(&b, " I started %d subagents; I'll summarize when their answers arrive.", len(subs))
	}
	// What it did, as a Markdown table (it also shows tables rendering).
	rows := ""
	for _, name := range []string{"list_dir", "read_file", "channel_create", "ask_user", "write_file", "bash"} {
		if r, ok := e.result(name); ok {
			state := "done"
			if r.IsError {
				state = "refused: " + firstLine(r.Content)
			}
			rows += fmt.Sprintf("| `%s` | %s |\n", name, strings.ReplaceAll(state, "|", "\\|"))
		}
	}
	if rows != "" {
		b.WriteString("\n\n| Step | Result |\n|---|---|\n" + rows)
	}
	return say(strings.TrimSpace(b.String()))
}

// boardTurn works on a board: list, create one if there is none, add a note
// and a diagram (and an image the task names), then read it back. ok is
// false once it is done, so the summary follows.
func boardTurn(e episode, id func(string) string, task string) (llm.Result, bool) {
	if len(e.called("board_list")) == 0 {
		return use("Looking at the boards.", call(id("boards"), "board_list", map[string]any{})), true
	}
	listed, ok := e.result("board_list")
	if !ok {
		return llm.Result{}, false
	}
	m := boardRE.FindStringSubmatch(listed.Content)
	if m == nil {
		if len(e.called("board_create")) == 0 {
			return use("There is no board yet; creating one.", call(id("create"), "board_create", map[string]any{"name": "Sim board"})), true
		}
		created, _ := e.result("board_create")
		if m = boardRE.FindStringSubmatch(created.Content); m == nil {
			return llm.Result{}, false
		}
	}
	board := atoi(m[1])
	if len(e.called("board_add_note")) == 0 {
		calls := []llm.ToolCall{
			call(id("note"), "board_add_note", map[string]any{"board": board, "text": "Simulated agent: " + firstLine(task)}),
			call(id("shapes"), "board_add_shapes", map[string]any{"board": board, "shapes": []map[string]any{
				{"type": "rectangle", "x": 0, "y": 0, "width": 160, "height": 60, "text": "Camera", "fill": "#a5d8ff"},
				{"type": "arrow", "x": 160, "y": 30, "x2": 260, "y2": 30},
				{"type": "ellipse", "x": 260, "y": 0, "width": 140, "height": 60, "text": "Recorder"},
			}}),
		}
		if im := imageRE.FindStringSubmatch(task); im != nil {
			calls = append(calls, call(id("image"), "board_add_image", map[string]any{"board": board, "path": im[1], "caption": "From the simulated agent"}))
		}
		return use(fmt.Sprintf("Adding a note and a diagram to board #%d.", board), calls...), true
	}
	if len(e.called("board_read")) == 0 {
		return use("Reading the board back.", call(id("read"), "board_read", map[string]any{"board": board})), true
	}
	var b strings.Builder
	fmt.Fprintf(&b, "**Simulated agent.** Worked on board #%d.\n\n| Step | Result |\n|---|---|\n", board)
	for _, name := range []string{"board_create", "board_add_note", "board_add_shapes", "board_add_image", "board_read"} {
		if r, ok := e.result(name); ok {
			state := "done"
			if r.IsError {
				state = "refused: " + firstLine(r.Content)
			}
			fmt.Fprintf(&b, "| `%s` | %s |\n", name, strings.ReplaceAll(state, "|", "\\|"))
		}
	}
	return say(strings.TrimSpace(b.String())), true
}

// parentCollects summarizes subagents' final answers once all have arrived.
func parentCollects(turn llm.Turn) llm.Result {
	spawned, finals := 0, 0
	for _, m := range turn.Messages {
		for _, c := range m.Calls {
			if c.Name == "spawn_agent" {
				spawned++
			}
		}
		if mm := channelRE.FindStringSubmatch(m.Text); mm != nil && strings.HasPrefix(m.Text[len(mm[0]):], "Final answer:") {
			finals++
		}
	}
	if finals < spawned {
		return say(fmt.Sprintf("Got %d of %d subagent answers; waiting for the rest.", finals, spawned))
	}
	return say(fmt.Sprintf("All %d subagents reported back. Summary: each part is checked and nothing blocks the task.", finals))
}

func wants(low string, words ...string) bool {
	for _, w := range words {
		if regexp.MustCompile(`\b` + w).MatchString(low) {
			return true
		}
	}
	return false
}

// pickFile chooses a file from a list_dir result: prefer a README, then the
// first visible non-directory entry.
func pickFile(listing string) string {
	var first string
	for _, line := range strings.Split(listing, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		name := fields[0]
		if strings.HasSuffix(name, "/") || strings.HasPrefix(name, ".") {
			continue
		}
		if strings.HasPrefix(strings.ToLower(name), "readme") {
			return name
		}
		if first == "" {
			first = name
		}
	}
	return first
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if r := []rune(s); len(r) > 100 {
		s = string(r[:100]) + "…"
	}
	return s
}

func atoi(s string) int {
	n := 0
	fmt.Sscanf(s, "%d", &n)
	return n
}

// simTitle names a task from its first words, as a model would summarize.
func simTitle(turn llm.Turn) string {
	text := ""
	if len(turn.Messages) > 0 {
		text = turn.Messages[len(turn.Messages)-1].Text
	}
	if i := strings.Index(text, "Task:\n"); i >= 0 {
		text = text[i+len("Task:\n"):]
	}
	words := strings.Fields(firstLine(text))
	if len(words) > 10 {
		words = words[:10]
	}
	return "Simulated: " + strings.Join(words, " ")
}
