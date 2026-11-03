// Package agent owns agents and subagents: creation, owner messages, and the
// workspace snapshot clients load before following the event log.
package agent

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"xhaos/app/apperr"
	"xhaos/app/eventlog"
	"xhaos/app/idempotency"
	"xhaos/app/llm"
	"xhaos/app/policy"
	"xhaos/app/sqlitedb"
	"xhaos/app/workspace"
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
)

// Message authors.
const (
	FromOwner  = "owner"
	FromAgent  = "agent"
	FromSystem = "system" // harness notices: missing provider, model errors
	FromTool   = "tool"   // results of an agent's tool calls, sent to the model
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
	Title       string        `json:"title"`
	Role        string        `json:"role,omitempty"`
	Status      string        `json:"status"`
	Provider    string        `json:"provider,omitempty"`
	Model       string        `json:"model,omitempty"`
	Policy      policy.Policy `json:"policy"`
	CreatedAt   time.Time     `json:"createdAt"`
}

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
	By      string `json:"by,omitempty"`     // "owner" or "policy" for allowed/denied
	Reason  string `json:"reason,omitempty"` // why it asks or was denied
	Output  string `json:"output,omitempty"`
}

// StatusData is the payload of an agent.status event.
type StatusData struct {
	Status string `json:"status"`
}

// Snapshot is a workspace's full current state as of event Seq: its agents
// and their transcripts (every event except agent.created, which Agents
// already represents). Clients render it, then apply events after Seq.
type Snapshot struct {
	Seq        int64               `json:"seq"`
	Workspace  workspace.Workspace `json:"workspace"`
	Agents     []Agent             `json:"agents"`
	Transcript []eventlog.Event    `json:"transcript"`
}

type CreateRequest struct {
	IdempotencyKey string         `json:"idempotencyKey"`
	WorkspaceID    string         `json:"workspaceId"`
	ParentID       int64          `json:"parentId,omitempty"`
	Title          string         `json:"title"`            // one line; derived from Prompt when empty
	Prompt         string         `json:"prompt,omitempty"` // the task, recorded as the first owner message
	Role           string         `json:"role,omitempty"`
	Provider       string         `json:"provider,omitempty"`
	Model          string         `json:"model,omitempty"`
	Policy         *policy.Policy `json:"policy,omitempty"` // nil: parent's policy, else policy.Default
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
	created_at    TEXT NOT NULL
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
	// TODO: page long transcripts per agent instead of sending them all.
	events, err := eventlog.List(tx, workspaceID, 0, 0)
	if err != nil {
		return Snapshot{}, err
	}
	snap.Transcript = []eventlog.Event{}
	for _, e := range events {
		if e.Kind != EventCreated {
			snap.Transcript = append(snap.Transcript, e)
		}
	}
	return snap, nil
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
	if req.Title == "" {
		req.Title = TitleFrom(req.Prompt)
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
		}
		if req.Policy != nil {
			p = *req.Policy
		}
		if err := p.Validate(); err != nil {
			return err
		}
		at := s.now()
		res, err := tx.Exec(`INSERT INTO agents (workspace_id, parent_id, title, role, status, provider, model, access, review, commit_policy, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			req.WorkspaceID, sqlitedb.NullID(req.ParentID), req.Title, req.Role, StatusIdle, req.Provider, req.Model,
			p.Access, p.Review, p.Commit, sqlitedb.Stamp(at))
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
			ev, err := eventlog.Append(tx, req.WorkspaceID, id, EventMessage, MessageData{From: FromOwner, Body: req.Prompt}, at)
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
		call.State, call.By, call.Reason = ToolDenied, FromOwner, "the owner denied it"
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
			ev, err := eventlog.Append(tx, req.WorkspaceID, id, EventMessage, MessageData{From: FromOwner, Body: req.Body}, at)
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

func (s *Service) emit(events []eventlog.Event) {
	for _, e := range events {
		s.publish(e)
	}
}

const selectAgent = `SELECT id, workspace_id, COALESCE(parent_id, 0), title, role, status, provider, model, access, review, commit_policy, created_at FROM agents`

func scan(r interface{ Scan(...any) error }) (Agent, error) {
	var a Agent
	var at string
	err := r.Scan(&a.ID, &a.WorkspaceID, &a.ParentID, &a.Title, &a.Role, &a.Status, &a.Provider, &a.Model,
		&a.Policy.Access, &a.Policy.Review, &a.Policy.Commit, &at)
	a.CreatedAt = sqlitedb.ParseStamp(at)
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

// RecordTool appends one state of a tool call.
func (st *Store) RecordTool(workspaceID string, id int64, d ToolData) error {
	return st.write(workspaceID, id, func(tx *sql.Tx, at time.Time) (eventlog.Event, error) {
		return eventlog.Append(tx, workspaceID, id, EventTool, d, at)
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
