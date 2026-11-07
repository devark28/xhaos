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
	"github.com/devark28/xhaos/app/attachment"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/devark28/xhaos/app/agent"
	"github.com/devark28/xhaos/app/danger"
	"github.com/devark28/xhaos/app/egress"
	"github.com/devark28/xhaos/app/llm"
	"github.com/devark28/xhaos/app/policy"
	"github.com/devark28/xhaos/app/provider"
	"github.com/devark28/xhaos/app/sandbox"
	"github.com/devark28/xhaos/app/secret"
	"github.com/devark28/xhaos/app/skill"
	"github.com/devark28/xhaos/app/tool"
	"github.com/devark28/xhaos/app/usage"
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
	// Memories, when set, returns the workspace's memory index for the system
	// prompt (rebuilt every turn, so it survives compaction).
	Memories func(workspaceID string) string
	// Files reads attachments sent with owner messages.
	Files *attachment.Store
	// Extras add tools beyond the built-in ones (MCP servers, channels and
	// spawn_agent). Their calls pass the same gate as the built-in tools.
	Extras []ExternalTools
	// OnFinal, when set, receives an agent's final answer when a turn ends
	// with one (channels use it to report a subagent's result to its parent).
	OnFinal func(a agent.Agent, answer string)
	// Sandbox confines agents' shell commands (package sandbox).
	Sandbox sandbox.Config
	// Skills lists skills in the system prompt and gives bash calls their
	// folders (package skill).
	Skills *skill.Service
	// Secrets and Egress give bash calls secrets, in a sealed environment
	// whose network goes through the proxy (package secret, egress).
	Secrets *secret.Sealer
	Egress  *egress.Proxy
	// Usage, when set, counts the tokens of every model call.
	Usage   *usage.Recorder
	titling map[key]bool // titles asked for since start (at most once each)
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	mu      sync.Mutex
	active  map[key]bool // value: another turn requested while running
	stops   map[key]context.CancelFunc
}

// New creates a runner whose tools work inside root.
func New(agents *agent.Store, providers *provider.Store, notify Notifier) *Runner {
	if notify == nil {
		notify = func(Delta) {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Runner{agents: agents, providers: providers, notify: notify, ctx: ctx, cancel: cancel, active: map[key]bool{}, stops: map[key]context.CancelFunc{}}
}

// ExternalTools is a source of extra tools (MCP servers, channels). Calls
// receive the calling agent.
type ExternalTools interface {
	Specs(workspaceID string) []llm.Tool
	Owns(name string) bool
	Prepare(workspaceID string, a agent.Agent, c llm.ToolCall) (tool.Call, error)
	Execute(ctx context.Context, workspaceID string, a agent.Agent, call tool.Call) (string, error)
}

// specs lists every tool an agent in the workspace can call.
func (r *Runner) specs(ws string) []llm.Tool {
	specs := append(tool.Specs(), attachTools{r}.Specs(ws)...)
	for _, x := range r.Extras {
		specs = append(specs, x.Specs(ws)...)
	}
	return specs
}

func (r *Runner) extra(name string) ExternalTools {
	if (attachTools{r}).Owns(name) {
		return attachTools{r}
	}
	for _, x := range r.Extras {
		if x.Owns(name) {
			return x
		}
	}
	return nil
}

func (r *Runner) prepare(root, ws string, a agent.Agent, c llm.ToolCall) (tool.Call, error) {
	if x := r.extra(c.Name); x != nil {
		return x.Prepare(ws, a, c)
	}
	call, err := tool.Prepare(root, c)
	if err != nil || len(call.Secrets) == 0 {
		return call, err
	}
	if r.Secrets == nil || r.Egress == nil {
		return call, errors.New("secrets are not available on this server")
	}
	use, err := r.Secrets.Check(ws, a, call.Secrets)
	if err != nil {
		return call, err
	}
	// Each use asks, unless every secret is trusted to this agent.
	if !use.Trusted {
		call.Effect = policy.EffectSecret
	}
	call.Preview = use.Preview
	return call, nil
}

// skillDirs gives a bash call the folders of the skills it names, read-only,
// as $SKILL_<NAME>.
func (r *Runner) skillDirs(ws string, a agent.Agent, call tool.Call) (sandbox.Extra, error) {
	if len(call.Skills) == 0 {
		return sandbox.Extra{}, nil
	}
	if r.Skills == nil {
		return sandbox.Extra{}, errors.New("skills are not available on this server")
	}
	dirs, err := r.Skills.Dirs(ws, a.ProjectID, call.Skills)
	if err != nil {
		return sandbox.Extra{}, err
	}
	var x sandbox.Extra
	for name, dir := range dirs {
		x.Read = append(x.Read, dir)
		x.Env = append(x.Env, "SKILL_"+skill.EnvName(name)+"="+dir)
	}
	return x, nil
}

// lockBreak says why a call breaks a dangerously allowed agent's git lock.
func lockBreak(a agent.Agent, call tool.Call) string {
	if call.Name != "bash" || a.Danger == nil {
		return ""
	}
	return danger.CheckCommand(call.Summary, a.Danger.Branch)
}

// sealed runs a bash call that holds secrets: values as environment
// variables, network only to their hosts through the proxy, values
// scrubbed from the output.
func (r *Runner) sealed(ctx context.Context, root, ws string, a agent.Agent, call tool.Call) (string, error) {
	values, use, err := r.Secrets.Open(ws, a, call.Secrets)
	if err != nil {
		return "", err
	}
	proxyURL, release := r.Egress.Lease(use.Rules)
	defer release()
	env := []string{"GIT_TERMINAL_PROMPT=0", "NO_PROXY=", "no_proxy=", "XHAOS_PROXY=" + proxyURL}
	for _, k := range []string{"HTTPS_PROXY", "HTTP_PROXY", "ALL_PROXY", "https_proxy", "http_proxy", "all_proxy"} {
		env = append(env, k+"="+proxyURL)
	}
	var keys []string
	for name, v := range values {
		if use.Kinds[name] == secret.KindSSH {
			keys = append(keys, name)
			continue
		}
		env = append(env, name+"="+v)
	}
	sort.Strings(keys)
	extra, err := r.skillDirs(ws, a, call)
	if err != nil {
		return "", err
	}
	sealed := sandbox.Sealed{Env: env, ProxyPort: r.Egress.Port(), Extra: extra}
	if len(keys) > 0 {
		sealed.Setup = func(scratch string) ([]string, error) {
			return sshSetup(scratch, keys, values, use, r.Secrets.KnownHosts(ws, keys[0]))
		}
		sealed.Done = func(scratch string) {
			if b, err := os.ReadFile(filepath.Join(scratch, "home", ".ssh", "known_hosts")); err == nil {
				for _, k := range keys {
					r.Secrets.RememberHosts(ws, k, string(b))
				}
			}
		}
	}
	scrub := func(s string) string { return secret.Scrub(strings.ReplaceAll(s, proxyURL, "[proxy]"), values) }
	return tool.ExecuteSealed(ctx, root, r.Sandbox, call, sealed, scrub)
}

// sshSetup gives a sealed command its SSH keys: each key in a file ($NAME is
// its path), an ssh config that uses it for its hosts and reaches them only
// through the proxy (the server's binary as ProxyCommand), the host keys
// accepted before, and ssh/scp/sftp wrappers on PATH that use that config
// (ssh reads ~/.ssh from the real home otherwise, which the sandbox hides).
// git uses it through GIT_SSH_COMMAND.
func sshSetup(scratch string, keys []string, values map[string]string, use secret.Use, known string) ([]string, error) {
	dir := filepath.Join(scratch, "home", ".ssh")
	bin := filepath.Join(scratch, "bin")
	for _, d := range []string{dir, bin} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	config := filepath.Join(dir, "config")
	var cfg strings.Builder
	var env []string
	for _, name := range keys {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(values[name]), 0o600); err != nil {
			return nil, err
		}
		env = append(env, name+"="+path)
		for _, h := range use.Hosts[name] {
			user, host, port := "", h, "22"
			if u, rest, ok := strings.Cut(h, "@"); ok {
				user, host = u, rest
			}
			if hh, pp, err := net.SplitHostPort(host); err == nil {
				host, port = hh, pp
			}
			fmt.Fprintf(&cfg, "Host %s\n  HostName %s\n  Port %s\n  IdentityFile %s\n  IdentitiesOnly yes\n", host, host, port, path)
			if user != "" {
				fmt.Fprintf(&cfg, "  User %s\n", user)
			}
		}
	}
	fmt.Fprintf(&cfg, "Host *\n  ProxyCommand %s\n  UserKnownHostsFile %s\n  GlobalKnownHostsFile /dev/null\n  StrictHostKeyChecking accept-new\n  BatchMode yes\n  ForwardAgent no\n",
		egress.ConnectCommand(self), filepath.Join(dir, "known_hosts"))
	if err := os.WriteFile(config, []byte(cfg.String()), 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "known_hosts"), []byte(known), 0o600); err != nil {
		return nil, err
	}
	for _, prog := range []string{"ssh", "scp", "sftp"} {
		real, err := exec.LookPath(prog)
		if err != nil {
			continue
		}
		script := fmt.Sprintf("#!/bin/sh\nexec %q -F %q \"$@\"\n", real, config)
		if err := os.WriteFile(filepath.Join(bin, prog), []byte(script), 0o700); err != nil {
			return nil, err
		}
	}
	env = append(env, "PATH="+bin+":"+os.Getenv("PATH"), fmt.Sprintf("GIT_SSH_COMMAND=%s -F %q", filepath.Join(bin, "ssh"), config))
	return env, nil
}

func (r *Runner) execute(ctx context.Context, root, ws string, a agent.Agent, call tool.Call) (string, error) {
	if x := r.extra(call.Name); x != nil {
		return x.Execute(ctx, ws, a, call)
	}
	if len(call.Secrets) > 0 {
		return r.sealed(ctx, root, ws, a, call)
	}
	extra, err := r.skillDirs(ws, a, call)
	if err != nil {
		return "", err
	}
	return tool.ExecuteWith(ctx, root, r.Sandbox, extra, call)
}

// Catalog lists the runner's own tools beyond tool.Specs (read_attachment),
// for the Tools panel.
func (r *Runner) Catalog() []tool.Info { return attachTools{r}.Catalog() }

// rootFor is the folder an agent's tools work in.
func (r *Runner) rootFor(a agent.Agent) (string, error) {
	if a.ProjectID == 0 || r.ProjectRoot == nil {
		return "", errors.New("this agent has no project; its tools have no folder to work in")
	}
	return r.ProjectRoot(a.WorkspaceID, a.ProjectID)
}

// Resume continues agents left running by a previous server process.
// Resume continues a workspace's agents left running by a previous server
// process (once its key is available: they need its provider keys).
func (r *Runner) Resume(ws string) {
	agents, err := r.agents.Running()
	if err != nil {
		slog.Error("run: resume", "err", err)
		return
	}
	for _, a := range agents {
		if a.WorkspaceID == ws {
			r.Kick(a.WorkspaceID, a.ID)
		}
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
// A dangerously-allowed session may raise or lift it (callLimit).
const maxSteps = 40

// callLimit is the limit on model calls in a row for a at now: the
// session's while dangerously allowed, else maxSteps. 0 means none.
func callLimit(a agent.Agent, now time.Time) int {
	if a.DangerActive(now) && a.Danger.Calls != 0 {
		if a.Danger.Calls == agent.NoCallLimit {
			return 0
		}
		return a.Danger.Calls
	}
	return maxSteps
}

// SystemPrompt is fixed per agent so every turn of a conversation sends the
// same system prompt (a requirement for replaying thinking blocks and for
// the prompt cache). It leaves out the title, which the model may rewrite
// after the first turn; the task is in the first message anyway.
func SystemPrompt(a agent.Agent, root string) string {
	return fmt.Sprintf("You are agent #%d in Xhaos, an agent workspace.\n"+
		"The workspace is the directory %s. Use the tools to read, search, and change files there and to run commands. "+
		"Paths are relative to the workspace. Depending on your policy (access %s, review %s, commits %s), "+
		"changes and commands may wait for the user to allow or deny them; a denied call is the user's answer, so do not retry it unchanged. "+
		"When you need a decision or information only the user has, ask with ask_user instead of guessing. "+
		"Other agents work in this workspace, some in other projects; you cannot see their files, so coordinate through channels (agents_list, channel_create, channel_send). "+
		"Messages from other agents arrive marked [channel #N]. Use spawn_agent to hand a self-contained task to a subagent. "+
		"Read before you edit, keep changes small, and reply to the user in concise Markdown.",
		a.ID, root, a.Policy.Access, a.Policy.Review, a.Policy.Commit)
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
		adapter  llm.Adapter
		creds    llm.Credentials
		kind     string
		budget   int64
		overflow bool // the model rejected the request as too long; compact once
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
		if limit := callLimit(a, time.Now()); limit > 0 && step >= limit {
			fail(fmt.Sprintf("Stopped after %d model calls in a row. Send a message to let the agent continue.", limit))
			return
		}
		if adapter == nil {
			if a.Provider == "" || a.Model == "" {
				fail("No model is selected for this agent. Add a provider in Settings → Providers, then create the agent with a model.")
				return
			}
			p, c, err := r.providers.Credentials(ws, a.Provider)
			if err != nil {
				fail(fmt.Sprintf("Cannot use provider %q: %v. Check Settings → Providers.", a.Provider, err))
				return
			}
			if adapter = r.providers.Adapter(p.Kind); adapter == nil {
				fail(fmt.Sprintf("Provider kind %q has no adapter.", p.Kind))
				return
			}
			creds, kind, budget = c, p.Kind, p.Budget()
			r.maybeTitle(ws, a, adapter, creds, kind, conv)
		}

		// Keep the request within the model's context: summarize the older
		// part of the conversation when it nears the budget.
		last := latestCompaction(history)
		if overflow || estimate(conv, last) > int64(float64(budget)*compactAt) {
			forced := overflow
			overflow = false
			switch err := r.compact(ctx, ws, a, adapter, creds, kind, conv, last); {
			case err == nil:
				continue
			case ctx.Err() != nil:
				r.stopped(ws, id)
				return
			case forced:
				fail(fmt.Sprintf("The conversation is too long for %s and could not be compacted: %v", a.Model, err))
				return
			default:
				slog.Warn("run: compaction skipped", "agent", id, "err", err)
			}
		}

		root, err := r.rootFor(a)
		if err != nil {
			fail(fmt.Sprintf("Cannot use the agent's project: %v.", err))
			return
		}
		system := SystemPrompt(a, root)
		if r.Memories != nil {
			system += "\n\n" + r.Memories(ws)
		}
		if r.Skills != nil {
			if index := r.Skills.Index(ws, a.ProjectID); index != "" {
				system += "\n\n" + index
			}
		}
		turn := llm.Turn{Model: a.Model, System: system, Tools: r.specs(ws)}
		prefix := Prefix(turn)
		start, madeAt := 0, 0
		if last != nil {
			// The model sees the summary, then the messages after it.
			start, madeAt = last.Covers, last.Seen
			turn.Messages = append(turn.Messages, llm.Message{Role: "user", Text: summaryIntro + last.Body})
		}
		for i, m := range conv[start:] {
			// Replies made before the latest compaction were made on a longer
			// history; their thinking does not replay on the shorter one.
			stale := start+i < madeAt
			switch m.From {
			case agent.FromOwner, agent.FromChannel:
				turn.Messages = append(turn.Messages, r.userMessage(ws, m))
			case agent.FromTool:
				turn.Messages = append(turn.Messages, llm.Message{Role: "user", Results: m.Results})
			default:
				turn.Messages = append(turn.Messages, llm.Message{Role: "assistant", Text: m.Body, RawKind: m.Provider, Raw: m.Raw, Calls: m.Calls, StripThinking: m.Prefix != prefix || stale})
			}
		}

		_ = r.agents.SetStatus(ws, id, agent.StatusRunning)
		res, err := adapter.Stream(ctx, creds, turn, func(text string) { r.notify(Delta{WorkspaceID: ws, AgentID: id, Text: text}) })
		r.count(ws, a, kind, usage.Turn, res)
		if err != nil {
			if ctx.Err() != nil {
				r.stopped(ws, id)
				return
			}
			if tooLong(err) && last == nil || tooLong(err) && last.Seen < len(conv) {
				overflow = true // compact what is there, then try again
				continue
			}
			fail(fmt.Sprintf("Model call failed (%s %s): %v", kind, a.Model, err))
			return
		}
		reply := agent.MessageData{From: agent.FromAgent, Body: res.Text, Provider: kind, Raw: res.Raw, Seen: len(conv), Calls: res.Calls, Stop: res.StopReason, Prefix: prefix, InputTokens: res.InputTokens}
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
			if r.OnFinal != nil && res.Text != "" {
				r.OnFinal(a, res.Text)
			}
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
		call, err := r.prepare(root, ws, a, c)
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
			by := "policy"
			// Dangerously allow: what would ask runs, except secrets and
			// channels outside the family; git stays on its locked branch.
			if a.DangerActive(time.Now()) {
				if why := lockBreak(a, call); why != "" {
					verdict, reason = policy.Deny, why
				} else if verdict == policy.Ask && call.Effect != policy.EffectSecret && call.Effect != policy.EffectContact {
					verdict, reason, by = policy.Allow, "", "danger"
				}
			}
			d.Reason = reason
			switch verdict {
			case policy.Allow:
				d.State, d.By = agent.ToolAllowed, by
			case policy.Deny:
				d.State, d.By = agent.ToolDenied, by
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
				call, err := r.prepare(root, ws, a, c)
				out := ""
				if err == nil {
					out, err = r.execute(ctx, root, ws, a, call)
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
		case agent.FromOwner, agent.FromChannel:
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

// ---- context management ------------------------------------------------------

// compactAt is the share of the context budget that triggers a compaction.
const compactAt = 0.8

const summaryIntro = "Summary of the earlier conversation (older messages were compacted to save context; Xhaos keeps the full history):\n\n"

const summarizer = `You compact a conversation between a user and a coding agent so the agent can continue without the older messages.
Write a summary that keeps: the user's goals and requirements, decisions and their reasons, what was done (files changed, commands run and their outcomes), what was found, open problems, and the next steps.
Be complete but concise, in Markdown bullet points. Quote exact names, paths, and values. Do not invent anything.`

// latestCompaction returns the newest compaction in the log, or nil.
func latestCompaction(history []agent.MessageData) *agent.MessageData {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].From == agent.FromCompaction {
			return &history[i]
		}
	}
	return nil
}

// estimate approximates the next request's input tokens: the usage reported
// for the latest reply made on the current (possibly compacted) history, plus
// about four characters per token for what came after it.
func estimate(conv []agent.MessageData, last *agent.MessageData) int64 {
	var tokens int64
	start, madeAt, chars := 0, 0, 0
	if last != nil {
		start, madeAt, chars = last.Covers, last.Seen, len(last.Body)
	}
	from := start
	for i := len(conv) - 1; i >= start && i >= madeAt; i-- {
		if conv[i].From == agent.FromAgent && conv[i].InputTokens > 0 {
			tokens, from, chars = conv[i].InputTokens, i, 0
			break
		}
	}
	for _, m := range conv[from:] {
		chars += len(m.Body)
		for _, f := range m.Attachments {
			chars += attachmentChars(f)
		}
		for _, r := range m.Results {
			chars += len(r.Content)
		}
		for _, c := range m.Calls {
			chars += len(c.Input)
		}
	}
	return tokens + int64(chars/4)
}

// cutPoint picks where a compaction ends: the latest point after `after`
// that leaves at least the last message, and where nothing is split: before
// a user message, or before a reply that follows a finished tool round.
func cutPoint(conv []agent.MessageData, after int) int {
	for i := len(conv) - 1; i > after; i-- {
		switch {
		case conv[i].From == agent.FromOwner || conv[i].From == agent.FromChannel:
			return i
		case conv[i].From == agent.FromAgent && conv[i-1].From == agent.FromTool:
			return i
		}
	}
	return -1
}

// compact summarizes the conversation up to a cut point with the agent's own
// model and records the summary; nothing is removed from the log.
func (r *Runner) compact(ctx context.Context, ws string, a agent.Agent, adapter llm.Adapter, creds llm.Credentials, kind string, conv []agent.MessageData, last *agent.MessageData) error {
	from, previous := 0, ""
	if last != nil {
		from, previous = last.Covers, last.Body
	}
	cut := cutPoint(conv, from)
	if cut < 0 {
		return errors.New("no point to compact at yet")
	}
	var b strings.Builder
	if previous != "" {
		b.WriteString("Summary so far:\n" + previous + "\n\nWhat happened next:\n")
	}
	for _, m := range conv[from:cut] {
		switch m.From {
		case agent.FromOwner, agent.FromChannel:
			fmt.Fprintf(&b, "\n[user]\n%s\n", clipText(m.Body, 4000))
			for _, f := range m.Attachments {
				fmt.Fprintf(&b, "(attached %s, %s)\n", f.Name, f.Type)
			}
		case agent.FromAgent:
			if m.Body != "" {
				fmt.Fprintf(&b, "\n[agent]\n%s\n", clipText(m.Body, 4000))
			}
			for _, c := range m.Calls {
				fmt.Fprintf(&b, "[agent calls %s %s]\n", c.Name, clipText(string(c.Input), 600))
			}
		case agent.FromTool:
			for _, res := range m.Results {
				state := "result"
				if res.IsError {
					state = "error"
				}
				fmt.Fprintf(&b, "[tool %s]\n%s\n", state, clipText(res.Content, 1500))
			}
		}
	}
	turn := llm.Turn{Model: a.Model, System: summarizer, Messages: []llm.Message{{Role: "user", Text: b.String()}}}
	res, err := adapter.Stream(ctx, creds, turn, func(string) {})
	r.count(ws, a, kind, usage.Compaction, res)
	if err != nil {
		return err
	}
	if strings.TrimSpace(res.Text) == "" {
		return errors.New("the model returned an empty summary")
	}
	return r.agents.Append(ws, a.ID, agent.MessageData{From: agent.FromCompaction, Body: strings.TrimSpace(res.Text), Covers: cut, Seen: len(conv)})
}

// count records a call's tokens (a failed count is logged, never fatal).
func (r *Runner) count(ws string, a agent.Agent, kind, purpose string, res llm.Result) {
	if err := r.Usage.Record(ws, a.ID, a.Provider, kind, a.Model, purpose, res); err != nil {
		slog.Warn("run: record usage", "agent", a.ID, "err", err)
	}
}

const titler = "You name tasks for a list of agents. Reply with the title only."

// maybeTitle asks the agent's model, once, for a title that says what the
// task is about, replacing the prompt's first line. It runs beside the
// turn and costs one small call.
func (r *Runner) maybeTitle(ws string, a agent.Agent, adapter llm.Adapter, creds llm.Credentials, kind string, conv []agent.MessageData) {
	k := key{ws, a.ID}
	r.mu.Lock()
	if r.titling == nil {
		r.titling = map[key]bool{}
	}
	if r.titling[k] || !r.agents.TitlePending(ws, a.ID) {
		r.mu.Unlock()
		return
	}
	r.titling[k] = true
	r.mu.Unlock()
	task := ""
	for _, m := range conv {
		if m.From == agent.FromOwner && strings.TrimSpace(m.Body) != "" {
			task = m.Body
			break
		}
	}
	if task == "" {
		return
	}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		ctx, cancel := context.WithTimeout(r.ctx, time.Minute)
		defer cancel()
		turn := llm.Turn{Model: a.Model, System: titler, Small: true, Purpose: "title", Messages: []llm.Message{{Role: "user", Text: "Write a title for this task in 6 to 12 words: plain words saying what will be done, no quotes, no trailing period.\n\nTask:\n" + clipText(task, 3000)}}}
		res, err := adapter.Stream(ctx, creds, turn, func(string) {})
		r.count(ws, a, kind, usage.Title, res)
		if err != nil {
			slog.Info("run: title", "agent", a.ID, "err", err)
			return
		}
		if t := CleanTitle(res.Text); t != "" {
			if err := r.agents.SetTitle(ws, a.ID, t); err != nil {
				slog.Warn("run: set title", "agent", a.ID, "err", err)
			}
		}
	}()
}

// CleanTitle keeps a model's title to one plain line of 3 to 16 words.
func CleanTitle(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(s, "Title:"), "title:"))
	s = strings.Trim(s, "\"'`*#. ")
	words := strings.Fields(s)
	if len(words) < 3 {
		return ""
	}
	if len(words) > 16 {
		words = words[:16]
	}
	return strings.Join(words, " ")
}

func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + " …[cut]"
}

// tooLong reports a provider error that says the request exceeded the
// model's context window.
func tooLong(err error) bool {
	msg := strings.ToLower(err.Error())
	for _, s := range []string{"prompt is too long", "context_length_exceeded", "maximum context length", "context length", "too many tokens", "context window"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// userMessage builds a user message: text attachments are inlined after the
// body, images and PDFs go as files, anything else becomes a note.
func (r *Runner) userMessage(ws string, m agent.MessageData) llm.Message {
	msg := llm.Message{Role: "user", Text: m.Body}
	if len(m.Attachments) == 0 {
		return msg
	}
	var b strings.Builder
	b.WriteString(m.Body)
	for _, ref := range m.Attachments {
		if r.Files == nil {
			fmt.Fprintf(&b, "\n\n(Attached %s could not be read: attachments are not available.)", ref.Name)
			continue
		}
		f, err := r.Files.Load(ws, ref.ID)
		if err != nil {
			fmt.Fprintf(&b, "\n\n(Attached %s could not be read: %v)", ref.Name, err)
			continue
		}
		switch attachment.Kind(f) {
		case "image", "pdf":
			fmt.Fprintf(&b, "\n\n(Attached %s follows.)", f.Name)
			msg.Files = append(msg.Files, llm.File{Name: f.Name, Type: f.Type, Data: f.Data})
		case "text":
			b.WriteString("\n\n" + inlineText(f))
		default:
			fmt.Fprintf(&b, "\n\n(Attached %s, %s, %d bytes: a binary file the model cannot read.)", f.Name, f.Type, f.Size)
		}
	}
	msg.Text = b.String()
	return msg
}

// attachmentChars estimates an attachment's size in characters (four per
// token): images and PDF pages cost about 1,600 tokens.
func attachmentChars(f attachment.Ref) int {
	switch {
	case strings.HasPrefix(f.Type, "image/"):
		return 1600 * 4
	case f.Type == "application/pdf":
		return int(f.Size/50_000+1) * 2400 * 4
	}
	if f.Size > maxInlineText {
		return previewText
	}
	return int(f.Size)
}
