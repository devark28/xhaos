// Package run executes model turns for agents. The server owns execution:
// turns continue when no browser is connected. One turn runs per agent at a
// time; owner messages that arrive meanwhile queue exactly one more turn.
// A turn loops model call → tool calls → results until the model stops
// calling tools, or a call waits for the owner (the agent needs attention;
// the owner's decision wakes it again).
package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"xhaos/app/agent"
	"xhaos/app/llm"
	"xhaos/app/policy"
	"xhaos/app/provider"
	"xhaos/app/tool"
)

// Delta is a live, unpersisted text chunk of an agent's reply in progress.
type Delta struct {
	WorkspaceID string `json:"workspaceId"`
	AgentID     int64  `json:"agentId"`
	Text        string `json:"text"`
}

// Notifier delivers live-only notifications (not part of the event log).
type Notifier func(Delta)

type key struct {
	workspace string
	agent     int64
}

type Runner struct {
	agents    *agent.Store
	providers *provider.Store
	notify    Notifier
	// ProjectRoot, when set, maps an agent's project to the folder its tools
	// work in. Agents in different projects cannot see each other's files.
	ProjectRoot func(workspaceID string, projectID int64) (string, error)
	// External, when set, adds tools served outside this process (MCP). Their
	// calls pass the same gate as the built-in tools.
	External ExternalTools
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	mu       sync.Mutex
	active   map[key]bool // value: another turn requested while running
	stops    map[key]context.CancelFunc
}

// New creates a runner whose tools work inside root.
func New(agents *agent.Store, providers *provider.Store, notify Notifier) *Runner {
	if notify == nil {
		notify = func(Delta) {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Runner{agents: agents, providers: providers, notify: notify, ctx: ctx, cancel: cancel, active: map[key]bool{}, stops: map[key]context.CancelFunc{}}
}

// ExternalTools is a source of extra tools (the MCP manager).
type ExternalTools interface {
	Specs(workspaceID string) []llm.Tool
	Owns(name string) bool
	Prepare(workspaceID string, c llm.ToolCall) (tool.Call, error)
	Execute(ctx context.Context, workspaceID string, call tool.Call) (string, error)
}

// specs lists every tool an agent in the workspace can call.
func (r *Runner) specs(ws string) []llm.Tool {
	specs := tool.Specs()
	if r.External != nil {
		specs = append(specs, r.External.Specs(ws)...)
	}
	return specs
}

func (r *Runner) prepare(root, ws string, c llm.ToolCall) (tool.Call, error) {
	if r.External != nil && r.External.Owns(c.Name) {
		return r.External.Prepare(ws, c)
	}
	return tool.Prepare(root, c)
}

func (r *Runner) execute(ctx context.Context, root, ws string, call tool.Call) (string, error) {
	if r.External != nil && r.External.Owns(call.Name) {
		return r.External.Execute(ctx, ws, call)
	}
	return tool.Execute(ctx, root, call)
}

// rootFor is the folder an agent's tools work in.
func (r *Runner) rootFor(a agent.Agent) (string, error) {
	if a.ProjectID == 0 || r.ProjectRoot == nil {
		return "", errors.New("this agent has no project; its tools have no folder to work in")
	}
	return r.ProjectRoot(a.WorkspaceID, a.ProjectID)
}

// Resume continues agents left running by a previous server process.
func (r *Runner) Resume() {
	agents, err := r.agents.Running()
	if err != nil {
		slog.Error("run: resume", "err", err)
		return
	}
	for _, a := range agents {
		r.Kick(a.WorkspaceID, a.ID)
	}
}

// Kick schedules a turn for the agent. It never blocks.
func (r *Runner) Kick(workspaceID string, agentID int64) {
	k := key{workspaceID, agentID}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ctx.Err() != nil {
		return
	}
	if _, running := r.active[k]; running {
		r.active[k] = true
		return
	}
	r.active[k] = false
	r.wg.Add(1)
	go r.loop(k)
}

// Close cancels running turns and waits for them to stop.
func (r *Runner) Close() {
	r.cancel()
	r.wg.Wait()
}

// Stop cancels the agent's running turn and drops a queued one. It reports
// whether a turn was running.
func (r *Runner) Stop(workspaceID string, agentID int64) bool {
	k := key{workspaceID, agentID}
	r.mu.Lock()
	defer r.mu.Unlock()
	stop, running := r.stops[k]
	if running {
		stop()
		r.active[k] = false
	}
	return running
}

func (r *Runner) loop(k key) {
	defer r.wg.Done()
	for {
		r.mu.Lock()
		ctx, stop := context.WithCancel(r.ctx)
		r.stops[k] = stop
		r.mu.Unlock()
		r.turn(ctx, k.workspace, k.agent)
		stop()
		r.mu.Lock()
		delete(r.stops, k)
		if again := r.active[k]; again && r.ctx.Err() == nil {
			r.active[k] = false
			r.mu.Unlock()
			continue
		}
		delete(r.active, k)
		r.mu.Unlock()
		return
	}
}

// maxSteps bounds model calls per wake-up, so a looping agent stops and asks.
const maxSteps = 40

// SystemPrompt is fixed per agent so every turn of a conversation sends the
// same system prompt (a requirement for replaying thinking blocks).
func SystemPrompt(a agent.Agent, root string) string {
	return fmt.Sprintf("You are agent #%d in Xhaos, an agent workspace, working on: %s\n"+
		"The workspace is the directory %s. Use the tools to read, search, and change files there and to run commands. "+
		"Paths are relative to the workspace. Depending on your policy (access %s, review %s, commits %s), "+
		"changes and commands may wait for the user to allow or deny them; a denied call is the user's answer, so do not retry it unchanged. "+
		"When you need a decision or information only the user has, ask with ask_user instead of guessing. "+
		"Read before you edit, keep changes small, and reply to the user in concise Markdown.",
		a.ID, a.Title, root, a.Policy.Access, a.Policy.Review, a.Policy.Commit)
}

// stopped marks the agent paused after its turn was cancelled by the owner.
// On server shutdown it stays running, so Resume continues it next start.
func (r *Runner) stopped(ws string, id int64) {
	if r.ctx.Err() == nil {
		_ = r.agents.SetStatus(ws, id, agent.StatusPaused)
	}
}

func (r *Runner) turn(ctx context.Context, ws string, id int64) {
	a, err := r.agents.Get(ws, id)
	if err != nil || a.Status == agent.StatusTerminated {
		return
	}
	fail := func(msg string) {
		_ = r.agents.Append(ws, id, agent.MessageData{From: agent.FromSystem, Body: msg})
		_ = r.agents.SetStatus(ws, id, agent.StatusAttention)
	}
	var (
		adapter llm.Adapter
		creds   llm.Credentials
		kind    string
	)
	for step := 0; ; step++ {
		if ctx.Err() != nil {
			r.stopped(ws, id)
			return
		}
		history, err := r.agents.Messages(ws, id)
		if err != nil {
			slog.Error("run: load transcript", "agent", id, "err", err)
			return
		}
		conv := Conversation(history)
		if len(conv) == 0 {
			return
		}
		// The latest reply's tool calls are settled before anything else, even
		// when the owner wrote meanwhile: results must follow their calls.
		if i := lastReply(conv); i >= 0 && len(conv[i].Calls) > 0 && (i+1 == len(conv) || conv[i+1].From != agent.FromTool) {
			if !r.settle(ctx, ws, a, conv[i]) {
				return
			}
			continue
		}
		if conv[len(conv)-1].From == agent.FromAgent {
			return // answered; a queued wake-up found nothing new
		}
		if step >= maxSteps {
			fail(fmt.Sprintf("Stopped after %d model calls in a row. Send a message to let the agent continue.", maxSteps))
			return
		}
		if adapter == nil {
			if a.Provider == "" || a.Model == "" {
				fail("No model is selected for this agent. Add a provider in Settings → Providers, then create the agent with a model.")
				return
			}
			p, c, err := r.providers.Credentials(a.Provider)
			if err != nil {
				fail(fmt.Sprintf("Cannot use provider %q: %v. Check Settings → Providers.", a.Provider, err))
				return
			}
			if adapter = r.providers.Adapter(p.Kind); adapter == nil {
				fail(fmt.Sprintf("Provider kind %q has no adapter.", p.Kind))
				return
			}
			creds, kind = c, p.Kind
		}

		root, err := r.rootFor(a)
		if err != nil {
			fail(fmt.Sprintf("Cannot use the agent's project: %v.", err))
			return
		}
		turn := llm.Turn{Model: a.Model, System: SystemPrompt(a, root), Tools: r.specs(ws)}
		prefix := Prefix(turn)
		for _, m := range conv {
			switch m.From {
			case agent.FromOwner:
				turn.Messages = append(turn.Messages, llm.Message{Role: "user", Text: m.Body})
			case agent.FromTool:
				turn.Messages = append(turn.Messages, llm.Message{Role: "user", Results: m.Results})
			default:
				turn.Messages = append(turn.Messages, llm.Message{Role: "assistant", Text: m.Body, RawKind: m.Provider, Raw: m.Raw, Calls: m.Calls, StripThinking: m.Prefix != prefix})
			}
		}

		_ = r.agents.SetStatus(ws, id, agent.StatusRunning)
		res, err := adapter.Stream(ctx, creds, turn, func(text string) { r.notify(Delta{WorkspaceID: ws, AgentID: id, Text: text}) })
		if err != nil {
			if ctx.Err() != nil {
				r.stopped(ws, id)
				return
			}
			fail(fmt.Sprintf("Model call failed (%s %s): %v", kind, a.Model, err))
			return
		}
		reply := agent.MessageData{From: agent.FromAgent, Body: res.Text, Provider: kind, Raw: res.Raw, Seen: len(conv), Calls: res.Calls, Stop: res.StopReason, Prefix: prefix}
		if res.Text != "" || len(res.Raw) > 0 || len(res.Calls) > 0 {
			if err := r.agents.Append(ws, id, reply); err != nil {
				slog.Error("run: save reply", "agent", id, "err", err)
				return
			}
		}
		if len(res.Calls) > 0 {
			continue // settle them next
		}
		switch res.StopReason {
		case "refusal":
			fail("The model declined this request. Rephrase it, or choose another model for a new agent.")
		case "max_tokens":
			fail("The reply hit the output limit and may be cut off. Ask the agent to continue.")
		default:
			_ = r.agents.SetStatus(ws, id, agent.StatusComplete)
		}
		return
	}
}

// Prefix fingerprints a turn's system prompt and tools. Replies made under
// another prefix (an older build's prompt, for example) cannot replay their
// thinking: the provider rejects it as bound to a different conversation.
// Comparing recorded fingerprints keeps the strip deterministic, so every
// later request sends the same history.
func Prefix(t llm.Turn) string {
	tools, _ := json.Marshal(t.Tools)
	sum := sha256.Sum256(append([]byte(t.System+"\x00"), tools...))
	return hex.EncodeToString(sum[:8])
}

func lastReply(conv []agent.MessageData) int {
	for i := len(conv) - 1; i >= 0; i-- {
		if conv[i].From == agent.FromAgent {
			return i
		}
	}
	return -1
}

// settle moves a reply's tool calls through the policy gate. Calls the gate
// allows or denies are decided at once; the rest wait for the owner. When
// every call is decided it runs the allowed ones in order, records their
// results for the model, and returns true.
func (r *Runner) settle(ctx context.Context, ws string, a agent.Agent, reply agent.MessageData) bool {
	root, err := r.rootFor(a)
	if err != nil {
		slog.Error("run: project root", "agent", a.ID, "err", err)
		return false
	}
	states, err := r.agents.Tools(ws, a.ID)
	if err != nil {
		slog.Error("run: load tool states", "agent", a.ID, "err", err)
		return false
	}
	record := func(d agent.ToolData) {
		if err := r.agents.RecordTool(ws, a.ID, d); err != nil {
			slog.Error("run: record tool state", "agent", a.ID, "err", err)
		}
		states[d.CallID] = d
	}
	waiting := false
	for _, c := range reply.Calls {
		if st, ok := states[c.ID]; ok {
			waiting = waiting || st.State == agent.ToolPending
			continue
		}
		call, err := r.prepare(root, ws, c)
		d := agent.ToolData{CallID: c.ID, Name: c.Name, Effect: call.Effect, Summary: call.Summary, Preview: call.Preview, Commit: call.Commit}
		switch {
		case reply.Stop == "max_tokens":
			// The input may be cut off yet still parse; never run it.
			d.State, d.Output = agent.ToolFailed, "Not run: the reply hit the output limit, so this call may be incomplete. Make it again, in smaller pieces if it is large."
		case err != nil:
			d.State, d.Output = agent.ToolFailed, err.Error()
		case call.Effect == tool.Ask:
			// Asking needs no permission; the call waits for the answers.
			d.State, d.Reason = agent.ToolPending, "waiting for your answers"
			waiting = true
		default:
			verdict, reason := a.Policy.Gate(call.Effect, call.Commit)
			d.Reason = reason
			switch verdict {
			case policy.Allow:
				d.State, d.By = agent.ToolAllowed, "policy"
			case policy.Deny:
				d.State, d.By = agent.ToolDenied, "policy"
			default:
				d.State = agent.ToolPending
				waiting = true
			}
		}
		record(d)
	}
	if waiting {
		_ = r.agents.SetStatus(ws, a.ID, agent.StatusAttention)
		return false
	}

	_ = r.agents.SetStatus(ws, a.ID, agent.StatusRunning)
	results := make([]llm.ToolResult, 0, len(reply.Calls))
	for _, c := range reply.Calls {
		d := states[c.ID]
		if d.State == agent.ToolAllowed {
			d.By, d.Reason = "", ""
			if ctx.Err() != nil {
				d.State, d.Output = agent.ToolFailed, "Not run: the agent was stopped."
			} else {
				// Prepare again: files may have changed while the call waited.
				call, err := r.prepare(root, ws, c)
				out := ""
				if err == nil {
					out, err = r.execute(ctx, root, ws, call)
				}
				d.State, d.Output = agent.ToolDone, out
				switch {
				case ctx.Err() != nil:
					d.State, d.Output = agent.ToolFailed, "Stopped before it finished.\n"+out
				case err != nil:
					d.State, d.Output = agent.ToolFailed, err.Error()
				}
			}
			record(d)
		}
		res := llm.ToolResult{CallID: c.ID, Content: d.Output}
		switch d.State {
		case agent.ToolDenied:
			res.IsError, res.Content = true, "Denied: "+d.Reason+"."
		case agent.ToolFailed:
			res.IsError = true
		}
		if res.Content == "" {
			res.Content = "(no output)"
		}
		results = append(results, res)
	}
	if err := r.agents.Append(ws, a.ID, agent.MessageData{From: agent.FromTool, Results: results}); err != nil {
		slog.Error("run: save tool results", "agent", a.ID, "err", err)
		return false
	}
	return true
}

// Conversation orders owner, agent, and tool-result messages as the model saw
// them. The log is in arrival order, so an owner message sent while a turn was
// running sits before that turn's reply; the reply belongs right after the
// messages it saw (its Seen count). Tool results belong right after the reply
// whose calls they answer, ahead of owner messages sent while tools waited.
// System notes are for the owner, not the model.
func Conversation(history []agent.MessageData) []agent.MessageData {
	conv := make([]agent.MessageData, 0, len(history))
	insert := func(at int, m agent.MessageData) {
		conv = append(conv[:at], append([]agent.MessageData{m}, conv[at:]...)...)
	}
	for _, m := range history {
		switch m.From {
		case agent.FromOwner:
			conv = append(conv, m)
		case agent.FromTool:
			at := len(conv)
			for i := len(conv) - 1; i >= 0; i-- {
				if conv[i].From == agent.FromAgent {
					at = i + 1
					break
				}
			}
			insert(at, m)
		case agent.FromAgent:
			at := len(conv)
			if m.Seen > 0 && m.Seen < at {
				at = m.Seen
			}
			insert(at, m)
		}
	}
	return conv
}
