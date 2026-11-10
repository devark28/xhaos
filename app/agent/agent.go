// Package agent owns agents and subagents: creation, owner messages, and the
// workspace snapshot clients load before following the event log.
package agent

import (
	"cmp"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/devark28/xhaos/app/attachment"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/devark28/xhaos/app/apperr"
	"github.com/devark28/xhaos/app/eventlog"
	"github.com/devark28/xhaos/app/idempotency"
	"github.com/devark28/xhaos/app/llm"
	"github.com/devark28/xhaos/app/policy"
	"github.com/devark28/xhaos/app/project"
	"github.com/devark28/xhaos/app/sqlitedb"
	"github.com/devark28/xhaos/app/tool"
	"github.com/devark28/xhaos/app/workspace"
)

// Statuses.
const (
	StatusIdle       = "idle"
	StatusRunning    = "running"
	StatusAttention  = "attention"
	StatusPaused     = "paused"
	StatusComplete   = "complete"
	StatusTerminated = "terminated"
)

// Event kinds this feature appends.
const (
	EventCreated = "agent.created"
	EventMessage = "agent.message"
	EventStatus  = "agent.status"
	EventTool    = "agent.tool"
	EventTitle   = "agent.title"  // a better title, written by the model
	EventDanger  = "agent.danger" // dangerously allow turned on (data: Danger) or off (data: null)
)

// Message authors.
const (
	FromOwner  = "owner"
	FromAgent  = "agent"
	FromSystem = "system" // harness notices: missing provider, model errors
	FromTool   = "tool"   // results of an agent's tool calls, sent to the model
	// FromCompaction is a summary of the conversation's earlier part; the model
	// sees it instead of those messages (the log keeps them).
	FromCompaction = "compaction"
	// FromChannel is a message another agent sent through a channel; the
	// model sees it as input, like the user's.
	FromChannel = "channel"
)

// Tool call states, recorded as agent.tool events (the latest one per call wins).
const (
	ToolPending = "pending" // waiting for the owner
	ToolAllowed = "allowed" // by the owner or the policy; runs next
	ToolDenied  = "denied"  // by the owner or the policy
	ToolDone    = "done"
	ToolFailed  = "failed" // invalid call, or it ran and failed
)

type Agent struct {
	ID          int64         `json:"id"`
	WorkspaceID string        `json:"workspaceId"`
	ParentID    int64         `json:"parentId,omitempty"`
	ProjectID   int64         `json:"projectId,omitempty"` // the folder its tools work in; 0 for agents made before projects
	Title       string        `json:"title"`
	Role        string        `json:"role,omitempty"`
	Status      string        `json:"status"`
	Provider    string        `json:"provider,omitempty"`
	Model       string        `json:"model,omitempty"`
	Policy      policy.Policy `json:"policy"`
	CreatedAt   time.Time     `json:"createdAt"`
	// Danger: the owner turned on "dangerously allow" (package danger).
	Danger *Danger `json:"danger,omitempty"`
}

// Danger is "dangerously allow": until Until, the agent's calls that would
// ask run without asking (secrets and channels outside its family still
// ask), and git is locked to Branch.
type Danger struct {
	Until  time.Time `json:"until"`
	Branch string    `json:"branch,omitempty"`
	// Calls is the limit on model calls in a row while it is on: 0 keeps the
	// normal limit, -1 means none. It ends with the session.
	Calls int `json:"calls,omitempty"`
}

// NoCallLimit as Danger.Calls lifts the limit on model calls in a row.
const NoCallLimit = -1

// Active reports whether a's danger mode is on at now.
func (a Agent) DangerActive(now time.Time) bool { return a.Danger != nil && now.Before(a.Danger.Until) }

// MessageData is the payload of an agent.message event.
type MessageData struct {
	From string `json:"from"` // FromOwner, FromAgent, or FromSystem
	Body string `json:"body"`
	// Agent replies keep the provider's native message so the next turn can
	// replay it verbatim (thinking blocks included).
	Provider string          `json:"provider,omitempty"`
	Raw      json.RawMessage `json:"raw,omitempty"`
	// Seen is how many conversation messages (owner and agent, in order) the
	// turn that produced this reply had. Owner messages sent during the turn
	// come after it in the log but belong after the reply in the conversation.
	Seen int `json:"seen,omitempty"`
	// Calls are the tool calls an agent reply made; Stop is its stop reason.
	Calls []llm.ToolCall `json:"calls,omitempty"`
	Stop  string         `json:"stop,omitempty"`
	// Prefix fingerprints the system prompt and tools the reply was made
	// under; replies from a different prefix replay without their thinking.
	Prefix string `json:"prefix,omitempty"`
	// Results answer the previous reply's Calls (FromTool messages).
	Results []llm.ToolResult `json:"results,omitempty"`
	// InputTokens is how much context the reply's request used (agent replies).
	InputTokens int64 `json:"inputTokens,omitempty"`
	// Covers is how many conversation messages a compaction summarizes.
	Covers int `json:"covers,omitempty"`
	// Channel and Sender identify a channel message (FromChannel).
	Channel int64 `json:"channel,omitempty"`
	Sender  int64 `json:"sender,omitempty"`
	// Attachments are files sent with an owner message.
	Attachments []attachment.Ref `json:"attachments,omitempty"`
}

// ToolData is the payload of an agent.tool event: one state of one call.
type ToolData struct {
	CallID  string `json:"callId"`
	Name    string `json:"name"`
	Effect  string `json:"effect"`
	Summary string `json:"summary"`
	Preview string `json:"preview,omitempty"`
	Commit  bool   `json:"commit,omitempty"`
	State   string `json:"state"`
	By      string `json:"by,omitempty"`     // "owner", "policy", "danger" or "classifier" for allowed/denied
	Reason  string `json:"reason,omitempty"` // why it asks or was denied
	Output  string `json:"output,omitempty"`
	// ViaModel is set when the local model, not the built-in list, allowed the
	// call: it then runs with no TCP connections.
	ViaModel bool `json:"viaModel,omitempty"`
}

// StatusData is the payload of an agent.status event.
type StatusData struct {
	Status string `json:"status"`
}

// Snapshot is a workspace's current state as of event Seq: its agents and
// the recent end of their transcripts (messages and tool calls). Clients
// render it, then apply events after Seq.
//
// Each agent's transcript is its last TranscriptPage events, plus any older
// tool call still waiting for the owner; a terminated agent's is left out
// until its card is opened. Older, for an agent with more history, is the
// seq to pass to TranscriptBefore for the page before.
type Snapshot struct {
	Seq        int64               `json:"seq"`
	Workspace  workspace.Workspace `json:"workspace"`
	Agents     []Agent             `json:"agents"`
	Transcript []eventlog.Event    `json:"transcript"`
	Older      map[int64]int64     `json:"older,omitempty"`
}

// TranscriptPage is how many transcript events a snapshot carries per agent,
// and the default page size of TranscriptBefore.
const TranscriptPage = 50

// transcriptKinds are the events a transcript shows. Status, title and
// danger come in the Agent entries; other workspace events (channels,
// boards, memories, skills, secrets) reach the pages by their own paths.
var transcriptKinds = []string{EventMessage, EventTool}

// Page is a run of one agent's older transcript events, in seq order. More
// says there are older ones still: ask again with Before.
type Page struct {
	Events []eventlog.Event `json:"events"`
	More   bool             `json:"more"`
	Before int64            `json:"before,omitempty"`
}

type CreateRequest struct {
	IdempotencyKey string         `json:"idempotencyKey"`
	WorkspaceID    string         `json:"workspaceId"`
	ParentID       int64          `json:"parentId,omitempty"`
	ProjectID      int64          `json:"projectId,omitempty"` // 0: the parent's project, else the workspace's first
	Title          string         `json:"title"`               // one line; derived from Prompt when empty
	Prompt         string         `json:"prompt,omitempty"`    // the task, recorded as the first owner message
	Role           string         `json:"role,omitempty"`
	Provider       string         `json:"provider,omitempty"`
	Model          string         `json:"model,omitempty"`
	Policy         *policy.Policy `json:"policy,omitempty"`      // nil: parent's policy, else policy.Default
	Attachments    []string       `json:"attachments,omitempty"` // uploaded attachment IDs, sent with Prompt
}

type DecideRequest struct {
	IdempotencyKey string `json:"idempotencyKey"`
	WorkspaceID    string `json:"workspaceId"`
	AgentID        int64  `json:"agentId"`
	CallID         string `json:"callId"`
	Allow          bool   `json:"allow"`
}

type MessageRequest struct {
	IdempotencyKey string  `json:"idempotencyKey"`
	WorkspaceID    string  `json:"workspaceId"`
	AgentIDs       []int64 `json:"agentIds"`
	Body           string  `json:"body"`
	// Attachments are uploaded attachment IDs sent with Body.
	Attachments []string `json:"attachments,omitempty"`
}

const schema = `
CREATE TABLE IF NOT EXISTS agents (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	workspace_id  TEXT NOT NULL REFERENCES workspaces(id),
	parent_id     INTEGER REFERENCES agents(id),
	title         TEXT NOT NULL,
	role          TEXT NOT NULL DEFAULT '',
	status        TEXT NOT NULL,
	provider      TEXT NOT NULL DEFAULT '',
	model         TEXT NOT NULL DEFAULT '',
	access        TEXT NOT NULL,
	review        TEXT NOT NULL,
	commit_policy TEXT NOT NULL,
	created_at    TEXT NOT NULL,
	project_id    INTEGER REFERENCES projects(id)
);`

// Service is the agent API. Every exported method is callable by clients.
type Service struct {
	db      *sql.DB
	publish eventlog.Publisher
	now     func() time.Time
	// Wake, when set, is called after an owner task, message, or tool decision
	// is committed (not on idempotent replays). The runner uses it to start or
	// continue a turn.
	Wake func(workspaceID string, agentID int64)
	// Halt, when set, cancels the agent's running turn and reports whether
	// one was running. Stop and Terminate use it.
	Halt func(workspaceID string, agentID int64) bool
	// Files resolves attachment IDs sent with tasks and messages.
	Files *attachment.Store
}

// ControlRequest names one agent for Stop or Terminate.
type ControlRequest struct {
	IdempotencyKey string `json:"idempotencyKey"`
	WorkspaceID    string `json:"workspaceId"`
	AgentID        int64  `json:"agentId"`
}

// NewService creates the agents table. Workspace, eventlog, and idempotency
// tables must already exist.
func NewService(db *sql.DB, publish eventlog.Publisher) (*Service, error) {
	if publish == nil {
		publish = func(eventlog.Event) {}
	}
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("migrate agents: %w", err)
	}
	if err := addColumn(db, "agents", "project_id", "INTEGER REFERENCES projects(id)"); err != nil {
		return nil, fmt.Errorf("migrate agents: %w", err)
	}
	for _, col := range [][2]string{{"danger_until", "TEXT NOT NULL DEFAULT ''"}, {"danger_branch", "TEXT NOT NULL DEFAULT ''"}, {"danger_calls", "INTEGER NOT NULL DEFAULT 0"}} {
		if err := addColumn(db, "agents", col[0], col[1]); err != nil {
			return nil, fmt.Errorf("migrate agents: %w", err)
		}
	}
	// 1: the title is the prompt's first line, for the model to replace.
	if err := addColumn(db, "agents", "title_auto", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return nil, fmt.Errorf("migrate agents: %w", err)
	}
	return &Service{db: db, publish: publish, now: time.Now}, nil
}

// Snapshot returns a workspace's agents and the sequence they reflect.
func (s *Service) Snapshot(workspaceID string) (Snapshot, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Snapshot{}, err
	}
	defer tx.Rollback()
	var snap Snapshot
	if snap.Workspace, err = workspace.Get(tx, workspaceID); err != nil {
		return Snapshot{}, err
	}
	if snap.Seq, err = eventlog.LastSeq(tx, workspaceID); err != nil {
		return Snapshot{}, err
	}
	rows, err := tx.Query(selectAgent+` WHERE workspace_id = ? ORDER BY id`, workspaceID)
	if err != nil {
		return Snapshot{}, err
	}
	defer rows.Close()
	snap.Agents = []Agent{}
	for rows.Next() {
		a, err := scan(rows)
		if err != nil {
			return Snapshot{}, err
		}
		snap.Agents = append(snap.Agents, a)
	}
	if err := rows.Err(); err != nil {
		return Snapshot{}, err
	}
	rows.Close()
	// Terminated agents' history waits until their card is opened: from the
	// end (Older = past the last event), through TranscriptBefore.
	var live []int64
	snap.Older = map[int64]int64{}
	for _, a := range snap.Agents {
		if a.Status == StatusTerminated {
			snap.Older[a.ID] = snap.Seq + 1
		} else {
			live = append(live, a.ID)
		}
	}
	events, err := eventlog.Latest(tx, workspaceID, live, transcriptKinds, TranscriptPage)
	if err != nil {
		return Snapshot{}, err
	}
	// Latest sends one event too many for agents with older history: it
	// marks where their older pages begin and is left out.
	count := map[int64]int{}
	for _, e := range events {
		count[e.AgentID]++
	}
	first := map[int64]int64{} // agent -> seq of its first event sent
	seen := map[int64]int{}
	var recent []eventlog.Event
	for _, e := range events {
		seen[e.AgentID]++
		if count[e.AgentID] > TranscriptPage && seen[e.AgentID] == 1 {
			continue
		}
		if first[e.AgentID] == 0 {
			first[e.AgentID] = e.Seq
		}
		recent = append(recent, e)
	}
	for id, n := range count {
		if n > TranscriptPage {
			snap.Older[id] = first[id]
		}
	}
	// A call still waiting for the owner must reach the page even when it
	// is older than the page sent: it is what the owner has to answer. Only
	// agents waiting or paused can have one.
	var waiting []eventlog.Event
	for _, a := range snap.Agents {
		start, cut := snap.Older[a.ID]
		if !cut || (a.Status != StatusAttention && a.Status != StatusPaused) {
			continue
		}
		tools, err := eventlog.ListAgent(tx, workspaceID, a.ID, EventTool)
		if err != nil {
			return Snapshot{}, err
		}
		last := map[string]eventlog.Event{}
		var order []string
		for _, e := range tools {
			var d ToolData
			if json.Unmarshal(e.Data, &d) != nil {
				continue
			}
			if _, ok := last[d.CallID]; !ok {
				order = append(order, d.CallID)
			}
			last[d.CallID] = e
		}
		for _, id := range order {
			e := last[id]
			var d ToolData
			if e.Seq < start && json.Unmarshal(e.Data, &d) == nil && d.State == ToolPending {
				waiting = append(waiting, e)
			}
		}
	}
	snap.Transcript = slim(append(waiting, recent...))
	slices.SortStableFunc(snap.Transcript, func(a, b eventlog.Event) int { return cmp.Compare(a.Seq, b.Seq) })
	if snap.Transcript == nil {
		snap.Transcript = []eventlog.Event{}
	}
	return snap, nil
}

// forReaders slims a transcript event for the pages (and /btw): they show a
// reply's text, not what the model needs to replay it. A tool result message
// is dropped (each tool call shows its own output); an agent reply loses its
// provider-native copy (thinking included) and its calls' arguments, keeping
// their names. The log itself is unchanged; the model still gets it all.
func forReaders(e eventlog.Event) (eventlog.Event, bool) {
	if e.Kind != EventMessage {
		return e, true
	}
	var m MessageData
	if json.Unmarshal(e.Data, &m) != nil {
		return e, true
	}
	if m.From == FromTool {
		return e, false
	}
	if m.Raw == nil && m.Prefix == "" && len(m.Calls) == 0 {
		return e, true
	}
	m.Raw, m.Provider, m.Prefix, m.Seen, m.Stop = nil, "", "", 0, ""
	for i := range m.Calls {
		m.Calls[i].Input = nil
	}
	if data, err := json.Marshal(m); err == nil {
		e.Data = data
	}
	return e, true
}

// slim applies forReaders to a run of events.
func slim(events []eventlog.Event) []eventlog.Event {
	out := events[:0]
	for _, e := range events {
		if e, ok := forReaders(e); ok {
			out = append(out, e)
		}
	}
	return out
}

// TranscriptBefore returns the page of an agent's transcript before seq
// before (a Snapshot's Older, or a Page's first event): up to limit events
// (TranscriptPage when 0, at most 500).
func (s *Service) TranscriptBefore(workspaceID string, agentID, before int64, limit int) (Page, error) {
	if limit <= 0 {
		limit = TranscriptPage
	}
	limit = min(limit, 500)
	events, err := eventlog.Before(s.db, workspaceID, agentID, transcriptKinds, before, limit+1)
	if err != nil {
		return Page{}, err
	}
	page := Page{Events: events, More: len(events) > limit}
	if page.More {
		page.Events = events[1:]
	}
	// Where the next page starts is the first event read, even when slimming
	// drops it (a tool result).
	if len(page.Events) > 0 {
		page.Before = page.Events[0].Seq
	}
	page.Events = slim(page.Events)
	return page, nil
}

// TitleFrom returns the first non-empty line of prompt, cut to 120 runes.
func TitleFrom(prompt string) string {
	for _, line := range strings.Split(prompt, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			if r := []rune(line); len(r) > 120 {
				return string(r[:120])
			}
			return line
		}
	}
	return ""
}

// Create creates an agent, or a subagent when ParentID is set. A subagent
// inherits its parent's policy and can only be restricted further.
func (s *Service) Create(req CreateRequest) (Agent, error) {
	req.Title = strings.TrimSpace(req.Title)
	auto := 0
	if req.Title == "" {
		req.Title, auto = TitleFrom(req.Prompt), 1
	}
	if req.Title == "" || strings.ContainsAny(req.Title, "\r\n") {
		return Agent{}, fmt.Errorf("%w: title must be one non-empty line", apperr.Invalid)
	}
	var created Agent
	var events []eventlog.Event
	_, err := idempotency.Run(s.db, "agent.Create", req.IdempotencyKey, req, &created, func(tx *sql.Tx) error {
		if _, err := workspace.Get(tx, req.WorkspaceID); err != nil {
			return err
		}
		p := policy.Default()
		if req.ParentID != 0 {
			parent, err := get(tx, req.WorkspaceID, req.ParentID)
			if err != nil {
				return err
			}
			if parent.Status == StatusTerminated {
				return fmt.Errorf("%w: parent #%d is terminated", apperr.Conflict, parent.ID)
			}
			if req.Policy != nil && !req.Policy.Within(parent.Policy) {
				return fmt.Errorf("%w: subagent policy exceeds parent #%d", apperr.Invalid, parent.ID)
			}
			p = parent.Policy
			if req.ProjectID != 0 && req.ProjectID != parent.ProjectID {
				return fmt.Errorf("%w: a subagent works in its parent's project", apperr.Invalid)
			}
			req.ProjectID = parent.ProjectID
		} else if req.ProjectID == 0 {
			first, err := project.First(tx, req.WorkspaceID)
			if err != nil {
				return err
			}
			req.ProjectID = first
		}
		if req.ProjectID == 0 {
			return fmt.Errorf("%w: add a project (a folder for the agent to work in) in the Files tab first", apperr.Invalid)
		}
		if _, err := project.Get(tx, req.WorkspaceID, req.ProjectID); err != nil {
			return err
		}
		if req.Policy != nil {
			p = *req.Policy
		}
		if err := p.Validate(); err != nil {
			return err
		}
		at := s.now()
		res, err := tx.Exec(`INSERT INTO agents (workspace_id, parent_id, title, role, status, provider, model, access, review, commit_policy, created_at, project_id, title_auto)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			req.WorkspaceID, sqlitedb.NullID(req.ParentID), req.Title, req.Role, StatusIdle, req.Provider, req.Model,
			p.Access, p.Review, p.Commit, sqlitedb.Stamp(at), sqlitedb.NullID(req.ProjectID), auto)
		if err != nil {
			return err
		}
		id, _ := res.LastInsertId()
		if created, err = get(tx, req.WorkspaceID, id); err != nil {
			return err
		}
		ev, err := eventlog.Append(tx, req.WorkspaceID, id, EventCreated, created, at)
		if err != nil {
			return err
		}
		events = append(events, ev)
		if strings.TrimSpace(req.Prompt) != "" {
			files, err := s.refs(tx, req.WorkspaceID, req.Attachments)
			if err != nil {
				return err
			}
			ev, err := eventlog.Append(tx, req.WorkspaceID, id, EventMessage, MessageData{From: FromOwner, Body: req.Prompt, Attachments: files}, at)
			if err != nil {
				return err
			}
			events = append(events, ev)
		}
		return nil
	})
	if err != nil {
		return Agent{}, err
	}
	s.emit(events)
	if len(events) > 1 {
		s.wake(req.WorkspaceID, created.ID)
	}
	return created, nil
}

func (s *Service) wake(workspaceID string, agentID int64) {
	if s.Wake != nil {
		s.Wake(workspaceID, agentID)
	}
}

// Decide allows or denies a tool call that is waiting for the owner.
func (s *Service) Decide(req DecideRequest) (eventlog.Event, error) {
	var out eventlog.Event
	replayed, err := idempotency.Run(s.db, "agent.Decide", req.IdempotencyKey, req, &out, func(tx *sql.Tx) error {
		a, err := get(tx, req.WorkspaceID, req.AgentID)
		if err != nil {
			return err
		}
		if a.Status == StatusTerminated {
			return fmt.Errorf("%w: agent #%d is terminated", apperr.Conflict, a.ID)
		}
		calls, err := toolStates(tx, req.WorkspaceID, req.AgentID)
		if err != nil {
			return err
		}
		call, ok := calls[req.CallID]
		if !ok {
			return fmt.Errorf("%w: tool call %q of agent #%d", apperr.NotFound, req.CallID, a.ID)
		}
		if call.State != ToolPending {
			return fmt.Errorf("%w: tool call %q is already %s", apperr.Conflict, req.CallID, call.State)
		}
		call.State, call.By, call.Reason = ToolDenied, FromOwner, "the user denied it"
		if call.Name == "ask_user" {
			if req.Allow {
				return fmt.Errorf("%w: answer the questions instead", apperr.Conflict)
			}
			call.Reason = "the user dismissed the questions"
		}
		if req.Allow {
			call.State, call.Reason = ToolAllowed, ""
		}
		call.Output = ""
		out, err = eventlog.Append(tx, req.WorkspaceID, a.ID, EventTool, call, s.now())
		return err
	})
	if err != nil {
		return eventlog.Event{}, err
	}
	if !replayed {
		s.publish(out)
		s.wake(req.WorkspaceID, req.AgentID)
	}
	return out, nil
}

// AnswerRequest answers a waiting ask_user call: option IDs and/or text per
// question ID.
type AnswerRequest struct {
	IdempotencyKey string                 `json:"idempotencyKey"`
	WorkspaceID    string                 `json:"workspaceId"`
	AgentID        int64                  `json:"agentId"`
	CallID         string                 `json:"callId"`
	Answers        map[string]tool.Answer `json:"answers"`
}

// Answer records the user's answers to an ask_user call; they become the
// call's result for the model, and the agent continues.
func (s *Service) Answer(req AnswerRequest) (eventlog.Event, error) {
	var out eventlog.Event
	replayed, err := idempotency.Run(s.db, "agent.Answer", req.IdempotencyKey, req, &out, func(tx *sql.Tx) error {
		a, err := get(tx, req.WorkspaceID, req.AgentID)
		if err != nil {
			return err
		}
		if a.Status == StatusTerminated {
			return fmt.Errorf("%w: agent #%d is terminated", apperr.Conflict, a.ID)
		}
		calls, err := toolStates(tx, req.WorkspaceID, req.AgentID)
		if err != nil {
			return err
		}
		call, ok := calls[req.CallID]
		if !ok || call.Name != "ask_user" {
			return fmt.Errorf("%w: question %q of agent #%d", apperr.NotFound, req.CallID, a.ID)
		}
		if call.State != ToolPending {
			return fmt.Errorf("%w: these questions are already %s", apperr.Conflict, call.State)
		}
		qs, err := tool.ParseQuestions(call.Preview)
		if err != nil {
			return err
		}
		text, err := tool.FormatAnswers(qs, req.Answers)
		if err != nil {
			return fmt.Errorf("%w: %v", apperr.Invalid, err)
		}
		call.State, call.By, call.Reason, call.Output = ToolDone, FromOwner, "", text
		out, err = eventlog.Append(tx, req.WorkspaceID, a.ID, EventTool, call, s.now())
		return err
	})
	if err != nil {
		return eventlog.Event{}, err
	}
	if !replayed {
		s.publish(out)
		s.wake(req.WorkspaceID, req.AgentID)
	}
	return out, nil
}

// Stop cancels the agent's running turn, including a running command. The
// agent is paused; an owner message continues it.
func (s *Service) Stop(req ControlRequest) error {
	a, err := get(s.db, req.WorkspaceID, req.AgentID)
	if err != nil {
		return err
	}
	if a.Status == StatusTerminated {
		return fmt.Errorf("%w: agent #%d is terminated", apperr.Conflict, a.ID)
	}
	if s.Halt == nil || !s.Halt(req.WorkspaceID, req.AgentID) {
		return fmt.Errorf("%w: agent #%d is not running", apperr.Conflict, a.ID)
	}
	return nil
}

// Terminate ends an agent and its subagents for good: running turns stop,
// waiting tool calls are denied, and no further messages are accepted.
func (s *Service) Terminate(req ControlRequest) ([]eventlog.Event, error) {
	var out []eventlog.Event
	replayed, err := idempotency.Run(s.db, "agent.Terminate", req.IdempotencyKey, req, &out, func(tx *sql.Tx) error {
		root, err := get(tx, req.WorkspaceID, req.AgentID)
		if err != nil {
			return err
		}
		if root.Status == StatusTerminated {
			return fmt.Errorf("%w: agent #%d is already terminated", apperr.Conflict, root.ID)
		}
		at := s.now()
		queue := []int64{root.ID}
		for len(queue) > 0 {
			id := queue[0]
			queue = queue[1:]
			a, err := get(tx, req.WorkspaceID, id)
			if err != nil {
				return err
			}
			rows, err := tx.Query(`SELECT id FROM agents WHERE workspace_id = ? AND parent_id = ? ORDER BY id`, req.WorkspaceID, id)
			if err != nil {
				return err
			}
			for rows.Next() {
				var child int64
				if err := rows.Scan(&child); err != nil {
					rows.Close()
					return err
				}
				queue = append(queue, child)
			}
			rows.Close()
			if a.Status == StatusTerminated {
				continue
			}
			calls, err := toolStates(tx, req.WorkspaceID, id)
			if err != nil {
				return err
			}
			for _, c := range sortedCalls(calls) {
				if c.State != ToolPending {
					continue
				}
				c.State, c.By, c.Reason = ToolDenied, FromOwner, "the agent was terminated"
				ev, err := eventlog.Append(tx, req.WorkspaceID, id, EventTool, c, at)
				if err != nil {
					return err
				}
				out = append(out, ev)
			}
			if _, err := tx.Exec(`UPDATE agents SET status = ? WHERE workspace_id = ? AND id = ?`, StatusTerminated, req.WorkspaceID, id); err != nil {
				return err
			}
			ev, err := eventlog.Append(tx, req.WorkspaceID, id, EventStatus, StatusData{Status: StatusTerminated}, at)
			if err != nil {
				return err
			}
			out = append(out, ev)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !replayed {
		s.emit(out)
		if s.Halt != nil {
			for _, e := range out {
				if e.Kind == EventStatus {
					s.Halt(e.WorkspaceID, e.AgentID)
				}
			}
		}
	}
	return out, nil
}

// Restore brings a terminated agent back, idle, with its history: the next
// owner message continues it. Its subagents stay terminated (restore each
// on its own); a subagent comes back only after its parent.
func (s *Service) Restore(req ControlRequest) (eventlog.Event, error) {
	var out eventlog.Event
	replayed, err := idempotency.Run(s.db, "agent.Restore", req.IdempotencyKey, req, &out, func(tx *sql.Tx) error {
		a, err := get(tx, req.WorkspaceID, req.AgentID)
		if err != nil {
			return err
		}
		if a.Status != StatusTerminated {
			return fmt.Errorf("%w: agent #%d is not terminated", apperr.Conflict, a.ID)
		}
		if a.ParentID != 0 {
			parent, err := get(tx, req.WorkspaceID, a.ParentID)
			if err != nil {
				return err
			}
			if parent.Status == StatusTerminated {
				return fmt.Errorf("%w: agent #%d's parent #%d is terminated; restore #%d first", apperr.Conflict, a.ID, parent.ID, parent.ID)
			}
		}
		if _, err := tx.Exec(`UPDATE agents SET status = ? WHERE workspace_id = ? AND id = ?`, StatusIdle, req.WorkspaceID, a.ID); err != nil {
			return err
		}
		out, err = eventlog.Append(tx, req.WorkspaceID, a.ID, EventStatus, StatusData{Status: StatusIdle}, s.now())
		return err
	})
	if err != nil {
		return eventlog.Event{}, err
	}
	if !replayed {
		s.emit([]eventlog.Event{out})
	}
	return out, nil
}

func sortedCalls(calls map[string]ToolData) []ToolData {
	out := make([]ToolData, 0, len(calls))
	for _, c := range calls {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CallID < out[j].CallID })
	return out
}

// toolStates returns the latest state of each of the agent's tool calls.
func toolStates(q sqlitedb.Querier, workspaceID string, id int64) (map[string]ToolData, error) {
	events, err := eventlog.ListAgent(q, workspaceID, id, EventTool)
	if err != nil {
		return nil, err
	}
	out := map[string]ToolData{}
	for _, e := range events {
		var d ToolData
		if err := json.Unmarshal(e.Data, &d); err != nil {
			return nil, err
		}
		out[d.CallID] = d
	}
	return out, nil
}

// Message records an owner message to each recipient agent. Either every
// recipient gets it or none does.
func (s *Service) Message(req MessageRequest) ([]eventlog.Event, error) {
	if strings.TrimSpace(req.Body) == "" {
		return nil, fmt.Errorf("%w: message body is empty", apperr.Invalid)
	}
	if len(req.AgentIDs) == 0 {
		return nil, fmt.Errorf("%w: no recipients", apperr.Invalid)
	}
	var out []eventlog.Event
	replayed, err := idempotency.Run(s.db, "agent.Message", req.IdempotencyKey, req, &out, func(tx *sql.Tx) error {
		at := s.now()
		seen := map[int64]bool{}
		for _, id := range req.AgentIDs {
			if seen[id] {
				continue
			}
			seen[id] = true
			a, err := get(tx, req.WorkspaceID, id)
			if err != nil {
				return err
			}
			if a.Status == StatusTerminated {
				return fmt.Errorf("%w: agent #%d is terminated", apperr.Conflict, id)
			}
			files, err := s.refs(tx, req.WorkspaceID, req.Attachments)
			if err != nil {
				return err
			}
			ev, err := eventlog.Append(tx, req.WorkspaceID, id, EventMessage, MessageData{From: FromOwner, Body: req.Body, Attachments: files}, at)
			if err != nil {
				return err
			}
			out = append(out, ev)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !replayed {
		s.emit(out)
		for _, e := range out {
			s.wake(e.WorkspaceID, e.AgentID)
		}
	}
	return out, nil
}

// refs resolves attachment IDs (none: nil).
func (s *Service) refs(q sqlitedb.Querier, ws string, ids []string) ([]attachment.Ref, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if s.Files == nil {
		return nil, fmt.Errorf("%w: attachments are not available", apperr.Invalid)
	}
	return s.Files.Refs(q, ws, ids)
}

func (s *Service) emit(events []eventlog.Event) {
	for _, e := range events {
		s.publish(e)
	}
}

const selectAgent = `SELECT id, workspace_id, COALESCE(parent_id, 0), title, role, status, provider, model, access, review, commit_policy, created_at, COALESCE(project_id, 0), danger_until, danger_branch, danger_calls FROM agents`

func scan(r interface{ Scan(...any) error }) (Agent, error) {
	var a Agent
	var at, dangerUntil, dangerBranch string
	var dangerCalls int
	err := r.Scan(&a.ID, &a.WorkspaceID, &a.ParentID, &a.Title, &a.Role, &a.Status, &a.Provider, &a.Model,
		&a.Policy.Access, &a.Policy.Review, &a.Policy.Commit, &at, &a.ProjectID, &dangerUntil, &dangerBranch, &dangerCalls)
	a.CreatedAt = sqlitedb.ParseStamp(at)
	if dangerUntil != "" {
		a.Danger = &Danger{Until: sqlitedb.ParseStamp(dangerUntil), Branch: dangerBranch, Calls: dangerCalls}
	}
	return a, err
}

func get(q sqlitedb.Querier, workspaceID string, id int64) (Agent, error) {
	a, err := scan(q.QueryRow(selectAgent+` WHERE workspace_id = ? AND id = ?`, workspaceID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return a, fmt.Errorf("%w: agent #%d in workspace %q", apperr.NotFound, id, workspaceID)
	}
	return a, err
}

// Store is what the server itself does to agents (model runs). It is not
// bound to clients, so they cannot impersonate an agent or set statuses.
type Store struct {
	db      *sql.DB
	publish eventlog.Publisher
	now     func() time.Time
}

func NewStore(db *sql.DB, publish eventlog.Publisher) *Store {
	if publish == nil {
		publish = func(eventlog.Event) {}
	}
	return &Store{db: db, publish: publish, now: time.Now}
}

// Get loads one agent.
func (st *Store) Get(workspaceID string, id int64) (Agent, error) { return get(st.db, workspaceID, id) }

// Messages returns the agent's conversation (agent.message events) in order.
func (st *Store) Messages(workspaceID string, id int64) ([]MessageData, error) {
	events, err := eventlog.ListAgent(st.db, workspaceID, id, EventMessage)
	if err != nil {
		return nil, err
	}
	out := make([]MessageData, 0, len(events))
	for _, e := range events {
		var m MessageData
		if err := json.Unmarshal(e.Data, &m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

// Tools returns the latest state of each of the agent's tool calls.
func (st *Store) Tools(workspaceID string, id int64) (map[string]ToolData, error) {
	return toolStates(st.db, workspaceID, id)
}

// RecordTool appends one state of a tool call. A call the classifier allowed
// with no reason yet keeps the reason SetToolReason gave it since: the runner
// writes its states from copies taken before the reason came.
func (st *Store) RecordTool(workspaceID string, id int64, d ToolData) error {
	return st.write(workspaceID, id, func(tx *sql.Tx, at time.Time) (eventlog.Event, error) {
		if d.By == "classifier" && d.Reason == "" {
			states, err := toolStates(tx, workspaceID, id)
			if err != nil {
				return eventlog.Event{}, err
			}
			if prev, ok := states[d.CallID]; ok && prev.By == "classifier" {
				d.Reason = prev.Reason
			}
		}
		return eventlog.Append(tx, workspaceID, id, EventTool, d, at)
	})
}

// SetToolReason sets the reason of a call the classifier allowed, as its
// latest state with the reason changed. A call with no state is skipped.
func (st *Store) SetToolReason(workspaceID string, id int64, callID, reason string) error {
	return st.write(workspaceID, id, func(tx *sql.Tx, at time.Time) (eventlog.Event, error) {
		states, err := toolStates(tx, workspaceID, id)
		if err != nil {
			return eventlog.Event{}, err
		}
		prev, ok := states[callID]
		if !ok {
			return eventlog.Event{}, errSkip
		}
		prev.Reason = reason
		return eventlog.Append(tx, workspaceID, id, EventTool, prev, at)
	})
}

// Running lists agents whose status is running, for resuming after a restart.
func (st *Store) Running() ([]Agent, error) {
	rows, err := st.db.Query(selectAgent+` WHERE status = ? ORDER BY id`, StatusRunning)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Agent
	for rows.Next() {
		a, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SetStatus updates the agent's status and records an agent.status event.
// A terminated agent stays terminated: a turn that has not yet noticed its
// cancellation cannot bring it back.
func (st *Store) SetStatus(workspaceID string, id int64, status string) error {
	return st.write(workspaceID, id, func(tx *sql.Tx, at time.Time) (eventlog.Event, error) {
		res, err := tx.Exec(`UPDATE agents SET status = ? WHERE workspace_id = ? AND id = ? AND status != ?`, status, workspaceID, id, StatusTerminated)
		if err != nil {
			return eventlog.Event{}, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return eventlog.Event{}, errSkip
		}
		return eventlog.Append(tx, workspaceID, id, EventStatus, StatusData{Status: status}, at)
	})
}

// TitlePending reports whether the agent's title is still its prompt's first
// line, waiting for the model to write a better one.
func (st *Store) TitlePending(workspaceID string, id int64) bool {
	var auto int
	err := st.db.QueryRow(`SELECT title_auto FROM agents WHERE workspace_id = ? AND id = ?`, workspaceID, id).Scan(&auto)
	return err == nil && auto == 1
}

// TitleData is an agent.title event.
type TitleData struct {
	Title string `json:"title"`
}

// SetTitle replaces a pending title (once; a title the user or a parent gave
// is never replaced).
func (st *Store) SetTitle(workspaceID string, id int64, title string) error {
	return st.write(workspaceID, id, func(tx *sql.Tx, at time.Time) (eventlog.Event, error) {
		res, err := tx.Exec(`UPDATE agents SET title = ?, title_auto = 0 WHERE workspace_id = ? AND id = ? AND title_auto = 1`, title, workspaceID, id)
		if err != nil {
			return eventlog.Event{}, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return eventlog.Event{}, errSkip
		}
		return eventlog.Append(tx, workspaceID, id, EventTitle, TitleData{Title: title}, at)
	})
}

// SetDanger turns dangerously allow on (d) or off (nil), recording it in the
// agent's history.
func (st *Store) SetDanger(workspaceID string, id int64, d *Danger) error {
	return st.write(workspaceID, id, func(tx *sql.Tx, at time.Time) (eventlog.Event, error) {
		until, branch, calls := "", "", 0
		if d != nil {
			until, branch, calls = sqlitedb.Stamp(d.Until), d.Branch, d.Calls
		}
		if _, err := tx.Exec(`UPDATE agents SET danger_until = ?, danger_branch = ?, danger_calls = ? WHERE workspace_id = ? AND id = ?`, until, branch, calls, workspaceID, id); err != nil {
			return eventlog.Event{}, err
		}
		return eventlog.Append(tx, workspaceID, id, EventDanger, d, at)
	})
}

// errSkip ends a write without an event or an error.
var errSkip = errors.New("skip")

// Append records a message from the agent or the harness.
func (st *Store) Append(workspaceID string, id int64, m MessageData) error {
	return st.write(workspaceID, id, func(tx *sql.Tx, at time.Time) (eventlog.Event, error) {
		return eventlog.Append(tx, workspaceID, id, EventMessage, m, at)
	})
}

func (st *Store) write(workspaceID string, id int64, fn func(*sql.Tx, time.Time) (eventlog.Event, error)) error {
	tx, err := st.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := get(tx, workspaceID, id); err != nil {
		return err
	}
	ev, err := fn(tx, st.now())
	if errors.Is(err, errSkip) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	st.publish(ev)
	return nil
}

// addColumn adds a column to a table created by an older build.
func addColumn(db *sql.DB, table, column, definition string) error {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	_, err = db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + column + ` ` + definition)
	return err
}
