package mcpserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/devark28/xhaos/app/agent"
	"github.com/devark28/xhaos/app/auth"
	"github.com/devark28/xhaos/app/channel"
	"github.com/devark28/xhaos/app/eventlog"
	"github.com/devark28/xhaos/app/policy"
	"github.com/devark28/xhaos/app/tool"
)

// ErrLocked is what every tool says while the token's workspace is locked.
var ErrLocked = errors.New("server locked: the owner must sign in on the Xhaos page (the server restarted or the workspace was locked); agents wait until then")

// toolDef is one tool and the scopes that reach it (any one of them).
type toolDef struct {
	name   string
	scopes []string
	add    func(*sdk.Server)
}

func (t toolDef) allowed(id identity) bool {
	return slices.ContainsFunc(t.scopes, id.has)
}

// def declares a tool: fn runs with the caller's identity once its scope
// and the workspace's lock are checked; its result goes back as JSON text.
func def[In any](s *Server, name, desc string, scopes []string, fn func(ctx context.Context, id identity, in In) (any, error)) toolDef {
	return toolDef{name: name, scopes: scopes, add: func(srv *sdk.Server) {
		sdk.AddTool(srv, &sdk.Tool{Name: name, Description: desc}, func(ctx context.Context, req *sdk.CallToolRequest, in In) (*sdk.CallToolResult, any, error) {
			var id identity
			if req.Extra != nil && req.Extra.TokenInfo != nil {
				id, _ = req.Extra.TokenInfo.Extra["identity"].(identity)
			}
			if id.TokenID == 0 {
				return nil, nil, errToken
			}
			if !slices.ContainsFunc(scopes, id.has) {
				return nil, nil, fmt.Errorf("this token lacks the %s scope", strings.Join(scopes, " or "))
			}
			if err := s.unlocked(id); err != nil {
				return nil, nil, err
			}
			out, err := fn(ctx, id, in)
			if err != nil {
				return nil, nil, err
			}
			text, err := json.MarshalIndent(out, "", "  ")
			if err != nil {
				return nil, nil, err
			}
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: string(text)}}}, nil, nil
		})
	}}
}

func (s *Server) unlocked(id identity) error {
	if s.d.State != nil && s.d.State(id.Workspace) != auth.StateUnlocked {
		return ErrLocked
	}
	return nil
}

var (
	read    = []string{ScopeRead}
	drive   = []string{ScopeDrive}
	answers = []string{ScopeAnswer, ScopeApprove}
)

// tools lists every tool, in the order clients see them.
func (s *Server) tools() []toolDef {
	return []toolDef{
		def(s, "list_agents", "List the agents: number (#N), title, status (idle, running, attention = waiting for the owner, paused, complete, terminated), parent, project, model, and how many questions or approvals each is waiting on.", read, s.listAgents),
		def(s, "get_agent", "One agent in detail: its status, its pending questions and approvals (with every option), its channels, and its recent messages and tool calls, oldest first.", read, s.getAgent),
		def(s, "list_questions", "Every pending question (ask_user), approval, and rename an agent proposed for itself (kind rename: proposed_title and reason; the agent does not wait for it) across all agents, each with its agent's number and title, its id for answer_question, and whether this token may answer it.", read, s.listQuestions),
		def(s, "wait_for_events", "Wait (up to timeout_seconds) until something happens: an agent replies, changes status, asks a question or waits for an approval, or a channel gets a message. Call it first without since to get the current seq, then pass the seq it returns each time. Returns at once when there is something new.", read, s.waitForEvents),
		def(s, "list_projects", "The workspace's projects (the folders agents work in), for create_agent.", read, s.listProjects),
		def(s, "list_models", "The model providers set up in the workspace and their models, for create_agent, and the default.", read, s.listModels),
		def(s, "list_channels", "The workspace's channels: id, subject, members (agent numbers and titles), message count and last message id.", read, s.listChannels),
		def(s, "read_channel", "Read a channel's messages after a message id (0: the latest ones). wait_seconds holds the call until a new message arrives.", read, s.readChannel),
		def(s, "create_agent", "Start a new top-level agent on a task. Choose a project and a model (list_projects, list_models) or take the defaults. Its first message (the task) is shown as coming from this token.", drive, s.createAgent),
		def(s, "send_message", "Send a message (a new task, an answer in prose, an instruction) to an agent, as the owner does; it wakes the agent unless it is paused.", drive, s.sendMessage),
		def(s, "start_agent", "Continue an agent: bring back a terminated one, then send it a message (default: continue where it left off). This also resumes a paused agent.", drive, s.startAgent),
		def(s, "stop_agent", "Stop an agent's running turn (it pauses; send_message or start_agent continues it). terminate: end it and its subagents for good (start_agent can bring it back).", drive, s.stopAgent),
		def(s, "post_channel", "Post a message into a channel under this token's name; its agents get it and wake.", drive, s.postChannel),
		def(s, "open_channel", "Open a channel with one agent, for an exchange you both read (it replies there with channel_send), and optionally post the first message.", drive, s.openChannel),
		def(s, "answer_question", "Answer a pending question by its id (from list_questions). For ask_user questions (scope answer): give answers per question (choices by option label or id, and/or text), or choice/text when there is one question, or dismiss. For a git commit or push (scope approve-feature-branch): decision allow or deny; allow only works for plain git commands on branches that are not protected (main, integration, release…, tags stay the owner's). For a rename an agent proposed for itself (kind rename, scope answer): decision rename or keep. Other approvals are the owner's.", answers, s.answerQuestion),
	}
}

func newKey() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "mcp-" + hex.EncodeToString(b)
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// ---- agents ----------------------------------------------------------------

type agentView struct {
	Number   int64  `json:"number"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Parent   int64  `json:"parent,omitempty"`
	Project  string `json:"project,omitempty"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Waiting  int    `json:"waiting"`
}

func (s *Server) projectNames(ws string) map[int64]string {
	out := map[int64]string{}
	if list, err := s.d.Projects.Projects(ws); err == nil {
		for _, p := range list {
			out[p.ID] = p.Name
		}
	}
	return out
}

func (s *Server) view(a agent.Agent, names map[int64]string, waiting int) agentView {
	return agentView{Number: a.ID, Title: a.Title, Status: a.Status, Parent: a.ParentID, Project: names[a.ProjectID], Provider: a.Provider, Model: a.Model, Waiting: waiting}
}

type listAgentsIn struct {
	IncludeTerminated bool `json:"include_terminated,omitempty" jsonschema:"also list terminated agents"`
}

func (s *Server) listAgents(_ context.Context, id identity, in listAgentsIn) (any, error) {
	snap, err := s.d.Agents.Snapshot(id.Workspace)
	if err != nil {
		return nil, err
	}
	pend, err := s.pending(id, snap.Agents)
	if err != nil {
		return nil, err
	}
	count := map[int64]int{}
	for _, p := range pend {
		count[p.Agent]++
	}
	names := s.projectNames(id.Workspace)
	out := []agentView{}
	for _, a := range snap.Agents {
		if a.Status == agent.StatusTerminated && !in.IncludeTerminated {
			continue
		}
		out = append(out, s.view(a, names, count[a.ID]))
	}
	return map[string]any{"agents": out, "seq": snap.Seq}, nil
}

type agentIn struct {
	Agent    int64 `json:"agent" jsonschema:"the agent's number (#N)"`
	Messages int   `json:"messages,omitempty" jsonschema:"how many recent transcript entries to return (default 20, at most 100)"`
}

type entryView struct {
	Seq    int64     `json:"seq"`
	At     time.Time `json:"at"`
	From   string    `json:"from"` // owner, agent, system, channel, compaction, or tool
	Via    string    `json:"via,omitempty"`
	Text   string    `json:"text,omitempty"`
	Tool   string    `json:"tool,omitempty"`
	State  string    `json:"state,omitempty"`
	Output string    `json:"output,omitempty"`
}

func (s *Server) getAgent(_ context.Context, id identity, in agentIn) (any, error) {
	a, err := s.d.Store.Get(id.Workspace, in.Agent)
	if err != nil {
		return nil, err
	}
	n := in.Messages
	if n <= 0 {
		n = 20
	}
	n = min(n, 100)
	// Tool calls take several events each (their states): read more and
	// keep each call's latest.
	page, err := s.d.Agents.TranscriptBefore(id.Workspace, a.ID, 1<<62, min(n*3, 500))
	if err != nil {
		return nil, err
	}
	var entries []entryView
	at := map[string]int{}
	for _, e := range page.Events {
		switch e.Kind {
		case agent.EventMessage:
			var m agent.MessageData
			if json.Unmarshal(e.Data, &m) != nil || (m.From == agent.FromAgent && m.Body == "") {
				continue
			}
			entries = append(entries, entryView{Seq: e.Seq, At: e.At, From: m.From, Via: m.Via, Text: clip(m.Body, 4000)})
		case agent.EventTool:
			var d agent.ToolData
			if json.Unmarshal(e.Data, &d) != nil {
				continue
			}
			v := entryView{Seq: e.Seq, At: e.At, From: "tool", Via: d.Via, Tool: d.Name, Text: clip(d.Summary, 300), State: d.State, Output: clip(d.Output, 800)}
			if i, ok := at[d.CallID]; ok {
				entries[i] = v
				continue
			}
			at[d.CallID] = len(entries)
			entries = append(entries, v)
		}
	}
	if len(entries) > n {
		entries = entries[len(entries)-n:]
	}
	pend, err := s.pending(id, []agent.Agent{a})
	if err != nil {
		return nil, err
	}
	var chans []map[string]any
	if list, err := s.d.Channels.List(id.Workspace); err == nil {
		for _, c := range list {
			if slices.Contains(c.Members, a.ID) {
				chans = append(chans, map[string]any{"id": c.ID, "subject": c.Subject, "closed": c.Closed})
			}
		}
	}
	return map[string]any{
		"agent":     s.view(a, s.projectNames(id.Workspace), len(pend)),
		"policy":    a.Policy,
		"pending":   pend,
		"channels":  chans,
		"recent":    entries,
		"more":      page.More || len(entries) == n,
		"createdAt": a.CreatedAt,
	}, nil
}

// ---- questions and approvals ----------------------------------------------------

// Kinds of pending items.
const (
	kindQuestion = "question" // ask_user
	kindCommit   = "commit"   // a git commit or push waiting for review
	kindApproval = "approval" // anything else waiting for the owner
	kindRename   = "rename"   // a title the agent proposed for itself (it does not wait)
)

// renameCall is the call-id part of a rename proposal's question id.
const renameCall = "rename"

type pendingItem struct {
	ID         string          `json:"id"`
	Agent      int64           `json:"agent"`
	AgentTitle string          `json:"agent_title"`
	Kind       string          `json:"kind"`
	Title      string          `json:"title,omitempty"`
	Questions  []tool.Question `json:"questions,omitempty"`
	Tool       string          `json:"tool,omitempty"`
	Command    string          `json:"command,omitempty"`
	Preview    string          `json:"preview,omitempty"`
	Reason     string          `json:"reason,omitempty"`
	// Intent is what allowing a commit or push does: Xhaos runs its own
	// command for it (WillRun), never the agent's.
	Intent  string `json:"intent,omitempty"`
	WillRun string `json:"will_run,omitempty"`
	// ProposedTitle is a rename's new title (the current one is AgentTitle).
	ProposedTitle string `json:"proposed_title,omitempty"`
	CanAnswer     bool   `json:"you_can_answer"`
	WhyNot        string `json:"why_not,omitempty"`
}

func questionID(agentID int64, callID string) string {
	return strconv.FormatInt(agentID, 10) + "/" + callID
}

func parseQuestionID(s string) (int64, string, error) {
	a, call, ok := strings.Cut(strings.TrimPrefix(strings.TrimSpace(s), "#"), "/")
	n, err := strconv.ParseInt(a, 10, 64)
	if !ok || err != nil || call == "" {
		return 0, "", fmt.Errorf("question id %q: expected <agent>/<call id>, as list_questions gives it", s)
	}
	return n, call, nil
}

// pending lists the calls of agents (only those that can be waiting) that
// wait for the owner, oldest call id first per agent, and the agents' title
// proposals.
func (s *Server) pending(id identity, agents []agent.Agent) ([]pendingItem, error) {
	out := []pendingItem{}
	for _, a := range agents {
		if a.TitleProposal != nil && a.Status != agent.StatusTerminated {
			out = append(out, renameItem(id, a))
		}
		if a.Status != agent.StatusAttention && a.Status != agent.StatusPaused {
			continue
		}
		states, err := s.d.Store.Tools(id.Workspace, a.ID)
		if err != nil {
			return nil, err
		}
		var calls []agent.ToolData
		for _, d := range states {
			if d.State == agent.ToolPending {
				calls = append(calls, d)
			}
		}
		slices.SortFunc(calls, func(x, y agent.ToolData) int { return strings.Compare(x.CallID, y.CallID) })
		for _, d := range calls {
			out = append(out, s.item(id, a, d))
		}
	}
	return out, nil
}

// renameItem describes an agent's title proposal.
func renameItem(id identity, a agent.Agent) pendingItem {
	p := pendingItem{ID: questionID(a.ID, renameCall), Agent: a.ID, AgentTitle: a.Title, Kind: kindRename,
		Title: "Rename #" + strconv.FormatInt(a.ID, 10) + "?", ProposedTitle: a.TitleProposal.Title, Reason: a.TitleProposal.Reason}
	p.CanAnswer = id.has(ScopeAnswer)
	if !p.CanAnswer {
		p.WhyNot = "answering questions needs the answer scope"
	}
	return p
}

// item describes one waiting call, and whether id may answer it.
func (s *Server) item(id identity, a agent.Agent, d agent.ToolData) pendingItem {
	p := pendingItem{ID: questionID(a.ID, d.CallID), Agent: a.ID, AgentTitle: a.Title, Tool: d.Name, Reason: d.Reason}
	switch {
	case d.Name == "ask_user":
		p.Kind, p.Tool, p.Reason = kindQuestion, "", ""
		var parsed struct {
			Title     string          `json:"title"`
			Questions []tool.Question `json:"questions"`
		}
		_ = json.Unmarshal([]byte(d.Preview), &parsed)
		p.Title, p.Questions = parsed.Title, parsed.Questions
		if p.Title == "" {
			p.Title = d.Summary
		}
		p.CanAnswer = id.has(ScopeAnswer)
		if !p.CanAnswer {
			p.WhyNot = "answering questions needs the answer scope"
		}
	case d.Commit && d.Name == "bash":
		p.Kind, p.Command = kindCommit, d.Summary
		in, err := s.featureBranches(a, d)
		if err == nil {
			p.Intent, p.WillRun = in.String(), in.Run
		}
		switch {
		case !id.has(ScopeApprove):
			p.WhyNot = "approving commits and pushes needs the approve-feature-branch scope"
		case err != nil:
			p.WhyNot = "only the owner can allow this: " + err.Error()
		default:
			p.CanAnswer = true
		}
	default:
		p.Kind, p.Command, p.Preview = kindApproval, clip(d.Summary, 2000), clip(d.Preview, 4000)
		p.WhyNot = "only the owner allows commands, file changes, web access, secrets and new channels; open the Xhaos page"
	}
	return p
}

// featureBranches reads a waiting commit or push as the intent
// approve-feature-branch may allow (see branch.go).
func (s *Server) featureBranches(a agent.Agent, d agent.ToolData) (intent, error) {
	if d.Effect == policy.EffectSecret {
		return intent{}, errors.New("the command uses secrets the owner has not trusted to this agent")
	}
	if a.ProjectID == 0 || s.d.ProjectRoot == nil {
		return intent{}, errors.New("the agent has no project")
	}
	root, err := s.d.ProjectRoot(a.WorkspaceID, a.ProjectID)
	if err != nil {
		return intent{}, err
	}
	return planner{root: root, local: s.LocalRemotes}.plan(d.Summary)
}

func (s *Server) listQuestions(_ context.Context, id identity, _ struct{}) (any, error) {
	snap, err := s.d.Agents.Snapshot(id.Workspace)
	if err != nil {
		return nil, err
	}
	pend, err := s.pending(id, snap.Agents)
	if err != nil {
		return nil, err
	}
	return map[string]any{"questions": pend, "seq": snap.Seq}, nil
}

type answerItem struct {
	Question string   `json:"question" jsonschema:"the question's id (q1, q2…), its number (1, 2…) or its prompt"`
	Choices  []string `json:"choices,omitempty" jsonschema:"chosen options, by label or id (o1, o2…)"`
	Text     string   `json:"text,omitempty" jsonschema:"a written answer, alone or beside choices"`
}

type answerIn struct {
	QuestionID string       `json:"question_id" jsonschema:"the id from list_questions (<agent>/<call id>)"`
	Answers    []answerItem `json:"answers,omitempty" jsonschema:"for ask_user: one entry per question answered"`
	Choice     string       `json:"choice,omitempty" jsonschema:"for an ask_user with one question: the chosen option's label or id"`
	Text       string       `json:"text,omitempty" jsonschema:"for an ask_user with one question: a written answer"`
	Dismiss    bool         `json:"dismiss,omitempty" jsonschema:"for ask_user: dismiss the questions without answering"`
	Decision   string       `json:"decision,omitempty" jsonschema:"for a git commit or push: allow or deny; for a rename: rename or keep"`
}

func (s *Server) answerQuestion(_ context.Context, id identity, in answerIn) (any, error) {
	agentID, callID, err := parseQuestionID(in.QuestionID)
	if err != nil {
		return nil, err
	}
	a, err := s.d.Store.Get(id.Workspace, agentID)
	if err != nil {
		return nil, err
	}
	if callID == renameCall {
		return s.answerRename(id, a, in)
	}
	states, err := s.d.Store.Tools(id.Workspace, a.ID)
	if err != nil {
		return nil, err
	}
	d, ok := states[callID]
	if !ok {
		return nil, fmt.Errorf("agent #%d has no question %q", a.ID, callID)
	}
	if d.State != agent.ToolPending {
		return nil, fmt.Errorf("question %s is already %s", in.QuestionID, d.State)
	}
	it := s.item(id, a, d)
	done := func(state string) (any, error) {
		return map[string]any{"ok": true, "agent": a.ID, "agent_title": a.Title, "question_id": it.ID, "state": state}, nil
	}
	switch it.Kind {
	case kindQuestion:
		if !id.has(ScopeAnswer) {
			return nil, errors.New(it.WhyNot)
		}
		if in.Decision != "" {
			return nil, errors.New("this is an ask_user question: give answers, choice/text, or dismiss, not a decision")
		}
		if in.Dismiss {
			if _, err := s.d.Agents.Decide(agent.DecideRequest{IdempotencyKey: newKey(), WorkspaceID: id.Workspace, AgentID: a.ID, CallID: callID, Allow: false, Via: id.Name}); err != nil {
				return nil, err
			}
			return done("dismissed")
		}
		ans, err := buildAnswers(it.Questions, in)
		if err != nil {
			return nil, err
		}
		if _, err := s.d.Agents.Answer(agent.AnswerRequest{IdempotencyKey: newKey(), WorkspaceID: id.Workspace, AgentID: a.ID, CallID: callID, Answers: ans, Via: id.Name}); err != nil {
			return nil, err
		}
		return done("answered")
	case kindCommit:
		if !id.has(ScopeApprove) {
			return nil, errors.New(it.WhyNot)
		}
		var allow bool
		var run string
		switch strings.ToLower(strings.TrimSpace(in.Decision)) {
		case "allow":
			allow = true
			if !it.CanAnswer {
				return nil, errors.New(it.WhyNot)
			}
			// Planned again here: what runs is what was checked just now.
			plan, err := s.featureBranches(a, d)
			if err != nil {
				return nil, fmt.Errorf("only the owner can allow this: %v", err)
			}
			run = plan.Run
		case "deny":
		default:
			return nil, errors.New("a commit or push takes decision allow or deny")
		}
		if _, err := s.d.Agents.Decide(agent.DecideRequest{IdempotencyKey: newKey(), WorkspaceID: id.Workspace, AgentID: a.ID, CallID: callID, Allow: allow, Via: id.Name, Run: run}); err != nil {
			return nil, err
		}
		if allow {
			out, _ := done("allowed")
			res := out.(map[string]any)
			res["intent"], res["xhaos_runs"] = it.Intent, run
			return res, nil
		}
		return done("denied")
	}
	return nil, errors.New(it.WhyNot)
}

// answerRename renames an agent to its proposal (decision rename) or keeps
// its title (decision keep).
func (s *Server) answerRename(id identity, a agent.Agent, in answerIn) (any, error) {
	if a.TitleProposal == nil || a.Status == agent.StatusTerminated {
		return nil, fmt.Errorf("agent #%d has no title proposal waiting", a.ID)
	}
	it := renameItem(id, a)
	if !it.CanAnswer {
		return nil, errors.New(it.WhyNot)
	}
	var rename bool
	switch strings.ToLower(strings.TrimSpace(in.Decision)) {
	case "rename":
		rename = true
	case "keep":
	default:
		return nil, errors.New("a rename takes decision rename or keep")
	}
	if _, err := s.d.Agents.DecideTitle(agent.TitleDecisionRequest{IdempotencyKey: newKey(), WorkspaceID: id.Workspace, AgentID: a.ID,
		Rename: rename, Title: a.TitleProposal.Title, Via: id.Name}); err != nil {
		return nil, err
	}
	res := map[string]any{"ok": true, "agent": a.ID, "question_id": it.ID, "state": "kept", "agent_title": a.Title}
	if rename {
		res["state"], res["agent_title"], res["renamed_from"] = "renamed", a.TitleProposal.Title, a.Title
	}
	return res, nil
}

// buildAnswers turns the tool's answers into option IDs and text per
// question ID, as the page sends them.
func buildAnswers(qs []tool.Question, in answerIn) (map[string]tool.Answer, error) {
	items := in.Answers
	if len(items) == 0 {
		if in.Choice == "" && in.Text == "" {
			return nil, errors.New("give answers (or choice/text for a single question, or dismiss)")
		}
		if len(qs) != 1 {
			return nil, fmt.Errorf("these are %d questions: answer each in answers", len(qs))
		}
		it := answerItem{Question: qs[0].ID, Text: in.Text}
		if in.Choice != "" {
			it.Choices = []string{in.Choice}
		}
		items = []answerItem{it}
	} else if in.Choice != "" || in.Text != "" {
		return nil, errors.New("give answers or choice/text, not both")
	}
	out := map[string]tool.Answer{}
	for _, it := range items {
		q, err := findQuestion(qs, it.Question)
		if err != nil {
			return nil, err
		}
		var a tool.Answer
		for _, c := range it.Choices {
			o, err := findOption(q, c)
			if err != nil {
				return nil, err
			}
			a.Choices = append(a.Choices, o)
		}
		a.Text = it.Text
		out[q.ID] = a
	}
	return out, nil
}

func findQuestion(qs []tool.Question, ref string) (tool.Question, error) {
	ref = strings.TrimSpace(ref)
	for i, q := range qs {
		if strings.EqualFold(ref, q.ID) || ref == strconv.Itoa(i+1) || strings.EqualFold(ref, strings.TrimSpace(q.Prompt)) {
			return q, nil
		}
	}
	if ref == "" && len(qs) == 1 {
		return qs[0], nil
	}
	return tool.Question{}, fmt.Errorf("no question %q (ids: %s)", ref, questionIDs(qs))
}

func questionIDs(qs []tool.Question) string {
	var ids []string
	for _, q := range qs {
		ids = append(ids, q.ID)
	}
	return strings.Join(ids, ", ")
}

func findOption(q tool.Question, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	for _, o := range q.Options {
		if strings.EqualFold(ref, o.Label) || ref == o.ID {
			return o.ID, nil
		}
	}
	var labels []string
	for _, o := range q.Options {
		labels = append(labels, strconv.Quote(o.Label))
	}
	if len(labels) == 0 {
		return "", fmt.Errorf("%q has no options: answer it with text", q.Prompt)
	}
	return "", fmt.Errorf("%q is not an option of %q (options: %s)", ref, q.Prompt, strings.Join(labels, ", "))
}

// ---- driving agents -------------------------------------------------------------

type createIn struct {
	Task     string `json:"task" jsonschema:"the task, sent as the agent's first message"`
	Title    string `json:"title,omitempty" jsonschema:"one-line title (default: the model writes one from the task)"`
	Project  string `json:"project,omitempty" jsonschema:"project id, name or path (default: the workspace's first project)"`
	Provider string `json:"provider,omitempty" jsonschema:"provider id from list_models (default: the one with the model, else the first)"`
	Model    string `json:"model,omitempty" jsonschema:"model name from list_models (default: the provider's first)"`
	Access   string `json:"access,omitempty" jsonschema:"rw (default) or ro"`
	Review   string `json:"review,omitempty" jsonschema:"manual (default: file changes wait for approval) or auto (file changes run; other commands still ask)"`
	Commit   string `json:"commit,omitempty" jsonschema:"review (default: commits and pushes wait for approval) or blocked; allowed is the owner's choice only"`
}

func (s *Server) createAgent(_ context.Context, id identity, in createIn) (any, error) {
	if strings.TrimSpace(in.Task) == "" {
		return nil, errors.New("give the agent a task")
	}
	projectID, err := s.resolveProject(id.Workspace, in.Project)
	if err != nil {
		return nil, err
	}
	prov, model, err := s.resolveModel(id.Workspace, in.Provider, in.Model)
	if err != nil {
		return nil, err
	}
	p := policy.Default()
	if in.Access != "" {
		p.Access = in.Access
	}
	if in.Review != "" {
		p.Review = in.Review
	}
	if in.Commit != "" {
		p.Commit = in.Commit
	}
	if p.Commit == "allowed" {
		return nil, errors.New("commit allowed (no review) is the owner's choice; use review or blocked")
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	a, err := s.d.Agents.Create(agent.CreateRequest{IdempotencyKey: newKey(), WorkspaceID: id.Workspace, ProjectID: projectID, Title: in.Title, Prompt: in.Task,
		Provider: prov, Model: model, Policy: &p, Via: id.Name})
	if err != nil {
		return nil, err
	}
	return map[string]any{"agent": s.view(a, s.projectNames(id.Workspace), 0), "policy": a.Policy}, nil
}

func (s *Server) resolveProject(ws, ref string) (int64, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return 0, nil
	}
	list, err := s.d.Projects.Projects(ws)
	if err != nil {
		return 0, err
	}
	for _, p := range list {
		if strconv.FormatInt(p.ID, 10) == ref || strings.EqualFold(p.Name, ref) || p.Path == ref {
			return p.ID, nil
		}
	}
	var names []string
	for _, p := range list {
		names = append(names, fmt.Sprintf("%d %s", p.ID, p.Name))
	}
	return 0, fmt.Errorf("no project %q (projects: %s)", ref, strings.Join(names, "; "))
}

func (s *Server) resolveModel(ws, provID, model string) (string, string, error) {
	list, err := s.d.Providers(ws)
	if err != nil {
		return "", "", err
	}
	if len(list) == 0 {
		return "", "", errors.New("no model provider is set up: the owner adds one in Settings → Models")
	}
	for _, p := range list {
		if provID != "" && p.ID != provID {
			continue
		}
		if model == "" {
			if len(p.Models) == 0 {
				return "", "", fmt.Errorf("provider %s lists no models: name one", p.ID)
			}
			return p.ID, p.Models[0], nil
		}
		if slices.Contains(p.Models, model) || (provID != "" && len(p.Models) == 0) {
			return p.ID, model, nil
		}
	}
	if provID != "" {
		return "", "", fmt.Errorf("provider %q not found, or it has no model %q (see list_models)", provID, model)
	}
	return "", "", fmt.Errorf("no provider offers model %q (see list_models)", model)
}

type messageIn struct {
	Agent int64  `json:"agent" jsonschema:"the agent's number"`
	Body  string `json:"body" jsonschema:"the message (Markdown)"`
}

func (s *Server) sendMessage(_ context.Context, id identity, in messageIn) (any, error) {
	a, err := s.d.Store.Get(id.Workspace, in.Agent)
	if err != nil {
		return nil, err
	}
	if a.Status == agent.StatusTerminated {
		return nil, fmt.Errorf("agent #%d is terminated: start_agent brings it back", a.ID)
	}
	if _, err := s.d.Agents.Message(agent.MessageRequest{IdempotencyKey: newKey(), WorkspaceID: id.Workspace, AgentIDs: []int64{a.ID}, Body: in.Body, Via: id.Name}); err != nil {
		return nil, err
	}
	note := "sent; the agent continues"
	if a.Status == agent.StatusPaused {
		note = "sent; the agent is paused and continues when started (start_agent)"
	}
	return map[string]any{"ok": true, "agent": a.ID, "agent_title": a.Title, "note": note}, nil
}

type startIn struct {
	Agent   int64  `json:"agent" jsonschema:"the agent's number"`
	Message string `json:"message,omitempty" jsonschema:"what to tell it (default: continue where you left off)"`
}

func (s *Server) startAgent(_ context.Context, id identity, in startIn) (any, error) {
	a, err := s.d.Store.Get(id.Workspace, in.Agent)
	if err != nil {
		return nil, err
	}
	if a.Status == agent.StatusTerminated {
		if _, err := s.d.Agents.Restore(agent.ControlRequest{IdempotencyKey: newKey(), WorkspaceID: id.Workspace, AgentID: a.ID}); err != nil {
			return nil, err
		}
		s.note(id, a.ID, "Brought back")
	}
	body := strings.TrimSpace(in.Message)
	if body == "" {
		body = "Continue where you left off."
	}
	if _, err := s.d.Agents.Message(agent.MessageRequest{IdempotencyKey: newKey(), WorkspaceID: id.Workspace, AgentIDs: []int64{a.ID}, Body: body, Via: id.Name}); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "agent": a.ID, "agent_title": a.Title}, nil
}

type stopIn struct {
	Agent     int64 `json:"agent" jsonschema:"the agent's number"`
	Terminate bool  `json:"terminate,omitempty" jsonschema:"end the agent and its subagents for good (denies their waiting calls)"`
}

func (s *Server) stopAgent(_ context.Context, id identity, in stopIn) (any, error) {
	a, err := s.d.Store.Get(id.Workspace, in.Agent)
	if err != nil {
		return nil, err
	}
	if in.Terminate {
		if _, err := s.d.Agents.Terminate(agent.ControlRequest{IdempotencyKey: newKey(), WorkspaceID: id.Workspace, AgentID: a.ID}); err != nil {
			return nil, err
		}
		s.note(id, a.ID, "Terminated")
		return map[string]any{"ok": true, "agent": a.ID, "agent_title": a.Title, "status": agent.StatusTerminated}, nil
	}
	if err := s.d.Agents.Stop(agent.ControlRequest{IdempotencyKey: newKey(), WorkspaceID: id.Workspace, AgentID: a.ID}); err != nil {
		return nil, err
	}
	s.note(id, a.ID, "Stopped")
	return map[string]any{"ok": true, "agent": a.ID, "agent_title": a.Title, "status": agent.StatusPaused}, nil
}

// note records, for the owner, what the token did to an agent (a system
// note: the model does not see it).
func (s *Server) note(id identity, agentID int64, what string) {
	_ = s.d.Store.Append(id.Workspace, agentID, agent.MessageData{From: agent.FromSystem, Body: fmt.Sprintf("%s through MCP by %q.", what, id.Name), Via: id.Name})
}

// ---- projects and models ---------------------------------------------------------

func (s *Server) listProjects(_ context.Context, id identity, _ struct{}) (any, error) {
	list, err := s.d.Projects.Projects(id.Workspace)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, p := range list {
		out = append(out, map[string]any{"id": p.ID, "name": p.Name, "path": p.Path})
	}
	return map[string]any{"projects": out}, nil
}

func (s *Server) listModels(_ context.Context, id identity, _ struct{}) (any, error) {
	list, err := s.d.Providers(id.Workspace)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, p := range list {
		out = append(out, map[string]any{"id": p.ID, "name": p.Name, "kind": p.Kind, "models": p.Models})
	}
	res := map[string]any{"providers": out}
	if prov, model, err := s.resolveModel(id.Workspace, "", ""); err == nil {
		res["default"] = map[string]string{"provider": prov, "model": model}
	}
	return res, nil
}

// ---- channels --------------------------------------------------------------------

type memberView struct {
	Number int64  `json:"number"`
	Title  string `json:"title"`
}

type channelView struct {
	ID        int64        `json:"id"`
	Subject   string       `json:"subject"`
	Kind      string       `json:"kind,omitempty"`
	Members   []memberView `json:"members"`
	Published bool         `json:"published,omitempty"`
	Closed    bool         `json:"closed,omitempty"`
	Messages  int          `json:"messages"`
	LastID    int64        `json:"last_id"`
}

type messageView struct {
	ID    int64     `json:"id"`
	At    time.Time `json:"at"`
	From  string    `json:"from"` // "#N title", "mcp: name" or "outside: name"
	Agent int64     `json:"agent,omitempty"`
	Text  string    `json:"text"`
}

// titles caches agent titles for one call.
type titles struct {
	s     *Server
	ws    string
	known map[int64]string
}

func (t *titles) of(n int64) string {
	if t.known == nil {
		t.known = map[int64]string{}
	}
	if v, ok := t.known[n]; ok {
		return v
	}
	a, _ := t.s.d.Store.Get(t.ws, n)
	t.known[n] = a.Title
	return a.Title
}

func (s *Server) channelView(ws string, c channel.Channel, t *titles) channelView {
	v := channelView{ID: c.ID, Subject: c.Subject, Kind: c.Kind, Published: c.Published, Closed: c.Closed, Members: []memberView{}}
	for _, m := range c.Members {
		v.Members = append(v.Members, memberView{Number: m, Title: t.of(m)})
	}
	v.Messages, v.LastID, _ = s.d.Channels.Count(ws, c.ID)
	return v
}

func messageOf(m channel.Message, t *titles) messageView {
	v := messageView{ID: m.ID, At: m.CreatedAt, Text: m.Body}
	switch {
	case m.SenderID != 0:
		v.Agent, v.From = m.SenderID, fmt.Sprintf("#%d %s", m.SenderID, t.of(m.SenderID))
	case m.Via == channel.ViaMCP:
		v.From = "mcp: " + m.SenderName
	default:
		v.From = "outside: " + m.SenderName
	}
	return v
}

func (s *Server) listChannels(_ context.Context, id identity, _ struct{}) (any, error) {
	list, err := s.d.Channels.List(id.Workspace)
	if err != nil {
		return nil, err
	}
	t := &titles{s: s, ws: id.Workspace}
	out := []channelView{}
	for _, c := range list {
		out = append(out, s.channelView(id.Workspace, c, t))
	}
	return map[string]any{"channels": out}, nil
}

type readChannelIn struct {
	Channel     int64 `json:"channel" jsonschema:"the channel id"`
	After       int64 `json:"after,omitempty" jsonschema:"return messages after this message id (0: the latest)"`
	Limit       int   `json:"limit,omitempty" jsonschema:"at most this many messages (default 50, at most 200)"`
	WaitSeconds int   `json:"wait_seconds,omitempty" jsonschema:"wait up to this long (at most 50) for a message after 'after'"`
}

func (s *Server) readChannel(ctx context.Context, id identity, in readChannelIn) (any, error) {
	c, err := s.d.Channels.Get(id.Workspace, in.Channel)
	if err != nil {
		return nil, err
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 200)
	deadline := s.now().Add(min(time.Duration(max(in.WaitSeconds, 0))*time.Second, s.MaxWait))
	if in.WaitSeconds > 0 {
		done, err := s.startWait(id)
		if err != nil {
			return nil, err
		}
		defer done()
	}
	for {
		msgs, err := s.d.Channels.MessagesAfter(id.Workspace, c.ID, in.After, limit)
		if err != nil {
			return nil, err
		}
		if len(msgs) > 0 || !s.now().Before(deadline) {
			t := &titles{s: s, ws: id.Workspace}
			out := []messageView{}
			next := in.After
			for _, m := range msgs {
				out = append(out, messageOf(m, t))
				next = max(next, m.ID)
			}
			return map[string]any{"channel": s.channelView(id.Workspace, c, t), "messages": out, "next_after": next}, nil
		}
		if err := s.sleep(ctx, id); err != nil {
			return nil, err
		}
	}
}

// sleep waits one poll interval; it fails when the call is cancelled, the
// token is revoked or the workspace locks meanwhile.
func (s *Server) sleep(ctx context.Context, id identity) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(s.Poll):
	}
	if !s.tokens.live(id.TokenID) {
		return errToken
	}
	return s.unlocked(id)
}

type postIn struct {
	Channel int64  `json:"channel" jsonschema:"the channel id"`
	Body    string `json:"body" jsonschema:"the message (Markdown)"`
}

func (s *Server) postChannel(_ context.Context, id identity, in postIn) (any, error) {
	m, err := s.d.Channels.PostFromClient(id.Workspace, in.Channel, id.Name, in.Body)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "message": messageOf(m, &titles{s: s, ws: id.Workspace})}, nil
}

type openIn struct {
	Agent   int64  `json:"agent" jsonschema:"the agent's number"`
	Subject string `json:"subject" jsonschema:"what the channel is about"`
	Message string `json:"message,omitempty" jsonschema:"a first message to post"`
}

func (s *Server) openChannel(_ context.Context, id identity, in openIn) (any, error) {
	a, err := s.d.Store.Get(id.Workspace, in.Agent)
	if err != nil {
		return nil, err
	}
	c, err := s.d.Channels.OpenForClient(a, in.Subject)
	if err != nil {
		return nil, err
	}
	t := &titles{s: s, ws: id.Workspace}
	out := map[string]any{"channel": s.channelView(id.Workspace, c, t)}
	if strings.TrimSpace(in.Message) != "" {
		m, err := s.d.Channels.PostFromClient(id.Workspace, c.ID, id.Name, in.Message)
		if err != nil {
			return nil, err
		}
		out["message"] = messageOf(m, t)
	}
	return out, nil
}

// ---- waiting for events ---------------------------------------------------------

type waitIn struct {
	Since          *int64 `json:"since,omitempty" jsonschema:"the seq from the last call (omit it to get the current seq at once)"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" jsonschema:"how long to wait for something new (default 30, at most 50)"`
}

type eventView struct {
	Seq        int64     `json:"seq"`
	At         time.Time `json:"at"`
	Kind       string    `json:"kind"`
	Agent      int64     `json:"agent,omitempty"`
	AgentTitle string    `json:"agent_title,omitempty"`
	Status     string    `json:"status,omitempty"`
	Text       string    `json:"text,omitempty"`
	QuestionID string    `json:"question_id,omitempty"`
	Channel    int64     `json:"channel,omitempty"`
	MessageID  int64     `json:"message_id,omitempty"`
	From       string    `json:"from,omitempty"`
	Via        string    `json:"via,omitempty"`
}

func (s *Server) waitForEvents(ctx context.Context, id identity, in waitIn) (any, error) {
	if in.Since == nil {
		snap, err := s.d.Agents.Snapshot(id.Workspace)
		if err != nil {
			return nil, err
		}
		pend, err := s.pending(id, snap.Agents)
		if err != nil {
			return nil, err
		}
		return map[string]any{"seq": snap.Seq, "events": []eventView{}, "pending_questions": len(pend)}, nil
	}
	timeout := time.Duration(in.TimeoutSeconds) * time.Second
	if in.TimeoutSeconds <= 0 {
		timeout = 30 * time.Second
	}
	deadline := s.now().Add(min(timeout, s.MaxWait))
	done, err := s.startWait(id)
	if err != nil {
		return nil, err
	}
	defer done()
	since := max(*in.Since, 0)
	t := &titles{s: s, ws: id.Workspace}
	for {
		events, err := s.d.Events.EventsAfter(id.Workspace, since, 500)
		if err != nil {
			return nil, err
		}
		out := []eventView{}
		for _, e := range events {
			since = max(since, e.Seq)
			if v, ok := summarize(e, t); ok {
				out = append(out, v)
			}
		}
		if len(out) > 0 || !s.now().Before(deadline) {
			return map[string]any{"seq": since, "events": out, "more": len(events) == 500}, nil
		}
		if len(events) == 500 {
			continue
		}
		if err := s.sleep(ctx, id); err != nil {
			return nil, err
		}
	}
}

// summarize turns an event into what a client follows, or false for the
// ones it does not need (tool results, delivered channel copies…).
func summarize(e eventlog.Event, t *titles) (eventView, bool) {
	v := eventView{Seq: e.Seq, At: e.At, Agent: e.AgentID}
	if e.AgentID != 0 {
		v.AgentTitle = t.of(e.AgentID)
	}
	switch e.Kind {
	case agent.EventCreated:
		v.Kind = "agent_created"
	case agent.EventStatus:
		var d agent.StatusData
		_ = json.Unmarshal(e.Data, &d)
		v.Kind, v.Status = "status", d.Status
	case agent.EventTitle:
		var d agent.TitleData
		_ = json.Unmarshal(e.Data, &d)
		v.Kind, v.Text, v.AgentTitle = "title", d.Title, d.Title
	case agent.EventTitleProposal:
		var p *agent.TitleProposal
		if json.Unmarshal(e.Data, &p) != nil || p == nil {
			return v, false
		}
		v.Kind, v.Text, v.QuestionID = "rename_proposal", p.Title, questionID(e.AgentID, renameCall)
	case agent.EventMessage:
		var m agent.MessageData
		if json.Unmarshal(e.Data, &m) != nil {
			return v, false
		}
		switch m.From {
		case agent.FromAgent:
			if m.Body == "" {
				return v, false
			}
			v.Kind = "reply"
		case agent.FromSystem:
			v.Kind = "note"
		case agent.FromOwner:
			v.Kind = "owner_message"
		default:
			return v, false
		}
		v.Text, v.Via = clip(m.Body, 2000), m.Via
	case agent.EventTool:
		var d agent.ToolData
		if json.Unmarshal(e.Data, &d) != nil {
			return v, false
		}
		v.QuestionID, v.Via = questionID(e.AgentID, d.CallID), d.Via
		switch {
		case d.State == agent.ToolPending && d.Name == "ask_user":
			v.Kind, v.Text = "question", d.Summary
		case d.State == agent.ToolPending:
			v.Kind, v.Text = "approval", clip(d.Summary, 500)
		case d.Name == "ask_user" && d.State == agent.ToolDone:
			v.Kind = "answered"
		case d.Name == "ask_user" && d.State == agent.ToolDenied:
			v.Kind = "dismissed"
		case d.By == agent.FromOwner && (d.State == agent.ToolAllowed || d.State == agent.ToolDenied):
			v.Kind, v.Text = d.State, clip(d.Summary, 500)
		default:
			return v, false
		}
	case channel.EventMessage:
		var m channel.Message
		if json.Unmarshal(e.Data, &m) != nil {
			return v, false
		}
		mv := messageOf(m, t)
		v.Kind, v.Channel, v.MessageID, v.From, v.Text = "channel_message", m.ChannelID, m.ID, mv.From, clip(m.Body, 2000)
		v.Agent, v.AgentTitle = 0, ""
	case channel.EventCreated, channel.EventClosed:
		var c channel.Channel
		if json.Unmarshal(e.Data, &c) != nil {
			return v, false
		}
		v.Kind, v.Channel, v.Text = strings.ReplaceAll(e.Kind, ".", "_"), c.ID, c.Subject
	default:
		return v, false
	}
	return v, true
}
