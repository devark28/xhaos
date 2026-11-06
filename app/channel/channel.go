// Package channel lets agents talk to each other. A channel has a subject and
// members; a message sent to it is delivered into every other member's
// transcript and wakes them. Who may join whom:
//   - a top-level agent: other top-level agents (any project) and its own subagents;
//   - a subagent: its parent, its siblings (other subagents of the same
//     parent), and its own subagents.
//
// So a subagent never reaches outside its family on its own; its parent can
// still bring it into one of the parent's channels.
//
// spawn_agent creates a subagent with a channel to its parent; the subagent's
// final answers are posted there automatically.
package channel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/devark28/xhaos/app/agent"
	"github.com/devark28/xhaos/app/apperr"
	"github.com/devark28/xhaos/app/eventlog"
	"github.com/devark28/xhaos/app/llm"
	"github.com/devark28/xhaos/app/sqlitedb"
	"github.com/devark28/xhaos/app/tool"
)

const (
	EventCreated = "channel.created"
	EventMessage = "channel.message"
	EventClosed  = "channel.closed"
	// EventLink: the channel was published, given a new passphrase, or
	// unpublished (the data is the channel; never the passphrase).
	EventLink = "channel.link"

	KindSpawn = "spawn" // a parent and the subagent it spawned

	// A channel takes at most burstLimit messages per burstWindow, so two
	// agents cannot answer each other forever at the user's expense.
	burstLimit  = 30
	burstWindow = 10 * time.Minute

	// spawn_agent limits: how deep subagents nest (a top-level agent is
	// level 1) and how many unfinished subagents one agent may have.
	maxDepth    = 3
	maxChildren = 8
)

type Channel struct {
	ID          int64     `json:"id"`
	WorkspaceID string    `json:"workspaceId"`
	Subject     string    `json:"subject"`
	Kind        string    `json:"kind,omitempty"`
	CreatorID   int64     `json:"creatorId"`
	Members     []int64   `json:"members"`
	CreatedAt   time.Time `json:"createdAt"`
	// Closed: the user closed it; nothing more can be sent.
	Closed bool `json:"closed,omitempty"`
	// Published: the user put it on a link, where anyone with its
	// passphrase can read it and send messages from outside Xhaos.
	Published bool `json:"published,omitempty"`
}

type Message struct {
	ID        int64 `json:"id"`
	ChannelID int64 `json:"channelId"`
	SenderID  int64 `json:"senderId"` // 0: sent from outside, through the channel's link
	// SenderName names an outside sender (as it introduced itself).
	SenderName string    `json:"senderName,omitempty"`
	Body       string    `json:"body"`
	CreatedAt  time.Time `json:"createdAt"`
}

const schema = `
CREATE TABLE IF NOT EXISTS channels (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	workspace_id TEXT NOT NULL REFERENCES workspaces(id),
	subject      TEXT NOT NULL,
	kind         TEXT NOT NULL DEFAULT '',
	creator_id   INTEGER NOT NULL REFERENCES agents(id),
	created_at   TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS channel_members (
	channel_id INTEGER NOT NULL REFERENCES channels(id),
	agent_id   INTEGER NOT NULL REFERENCES agents(id),
	PRIMARY KEY (channel_id, agent_id)
);
CREATE TABLE IF NOT EXISTS channel_messages (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	channel_id INTEGER NOT NULL REFERENCES channels(id),
	sender_id  INTEGER REFERENCES agents(id), -- NULL: from outside, through the link
	sender_name TEXT NOT NULL DEFAULT '',
	body       TEXT NOT NULL,
	created_at TEXT NOT NULL
);`

func Migrate(db *sql.DB) error {
	if _, err := db.Exec(schema); err != nil {
		return err
	}
	_, err := db.Exec(`ALTER TABLE channels ADD COLUMN closed_at TEXT NOT NULL DEFAULT ''`)
	if err != nil && !strings.Contains(err.Error(), "duplicate column") {
		return err
	}
	return migrateLinks(db)
}

// Service holds channels. Its exported methods are the client-facing API
// (read-only: agents create channels and send messages, the user watches).
type Service struct {
	db      *sql.DB
	agents  *agent.Service
	store   *agent.Store
	publish eventlog.Publisher
	// Wake starts or continues an agent's turn after a delivery.
	Wake func(workspaceID string, agentID int64)
	// Models lists the models configured for a provider (its key), and
	// Price prices one (USD per million input and output tokens; ok false
	// when unknown). spawn_agent may choose among them.
	Models func(workspaceID, providerID string) ([]string, error)
	Price  func(model string) (input, output float64, ok bool)
	now    func() time.Time
}

func NewService(db *sql.DB, agents *agent.Service, store *agent.Store, publish eventlog.Publisher) *Service {
	if publish == nil {
		publish = func(eventlog.Event) {}
	}
	return &Service{db: db, agents: agents, store: store, publish: publish, now: time.Now}
}

// ---- membership rules ----------------------------------------------------------

// descendant returns agent id when it is one of a's subagents (at any
// depth) and still running; anyone else is refused.
func (s *Service) descendant(a agent.Agent, id int64) (agent.Agent, error) {
	snap, err := s.agents.Snapshot(a.WorkspaceID)
	if err != nil {
		return agent.Agent{}, err
	}
	byID := map[int64]agent.Agent{}
	for _, x := range snap.Agents {
		byID[x.ID] = x
	}
	target, ok := byID[id]
	if !ok {
		return agent.Agent{}, fmt.Errorf("there is no agent #%d", id)
	}
	for p := target.ParentID; p != 0; p = byID[p].ParentID {
		if p == a.ID {
			if target.Status == agent.StatusTerminated {
				return agent.Agent{}, fmt.Errorf("subagent #%d is already terminated", id)
			}
			return target, nil
		}
	}
	return agent.Agent{}, fmt.Errorf("agent #%d is not one of your subagents; you can only terminate your own", id)
}

// mayInclude reports whether creator may put member in a channel.
func mayInclude(creator, member agent.Agent) bool {
	if member.Status == agent.StatusTerminated {
		return false
	}
	if member.ParentID == creator.ID { // its own subagents
		return true
	}
	if creator.ParentID == 0 {
		return member.ParentID == 0
	}
	return member.ID == creator.ParentID || member.ParentID == creator.ParentID
}

// contacts lists the agents a may put in a channel, by ID.
func (s *Service) contacts(a agent.Agent) ([]agent.Agent, error) {
	snap, err := s.agents.Snapshot(a.WorkspaceID)
	if err != nil {
		return nil, err
	}
	var out []agent.Agent
	for _, m := range snap.Agents {
		if m.ID != a.ID && mayInclude(a, m) {
			out = append(out, m)
		}
	}
	return out, nil
}

// family reports whether member is in a's own family: its subagents, its
// parent, or its siblings. Channels within a family need no approval (the
// spawn that made the family was approved); any other channel does.
func family(a, member agent.Agent) bool {
	return member.ParentID == a.ID || (a.ParentID != 0 && (member.ID == a.ParentID || member.ParentID == a.ParentID))
}

// task is the latest instruction the user gave an agent (first line), so an
// approval shows what each member is working on.
func (s *Service) task(ws string, id int64) string {
	events, err := eventlog.ListAgent(s.db, ws, id, agent.EventMessage)
	if err != nil {
		return ""
	}
	for i := len(events) - 1; i >= 0; i-- {
		var m agent.MessageData
		if json.Unmarshal(events[i].Data, &m) == nil && m.From == agent.FromOwner && strings.TrimSpace(m.Body) != "" {
			return firstLine(m.Body)
		}
	}
	return ""
}

// outsiders describes the members outside creator's family, one line each;
// empty when the channel stays within the family.
func (s *Service) outsiders(creator agent.Agent, members []int64) (string, error) {
	snap, err := s.agents.Snapshot(creator.WorkspaceID)
	if err != nil {
		return "", err
	}
	byID := map[int64]agent.Agent{}
	for _, a := range snap.Agents {
		byID[a.ID] = a
	}
	var b strings.Builder
	for _, id := range members {
		m, ok := byID[id]
		if !ok || id == creator.ID || family(creator, m) {
			continue
		}
		fmt.Fprintf(&b, "#%d %s · project %d · %s", m.ID, m.Title, m.ProjectID, m.Status)
		if m.ParentID != 0 {
			fmt.Fprintf(&b, " · subagent of #%d", m.ParentID)
		}
		if t := s.task(creator.WorkspaceID, m.ID); t != "" && t != m.Title {
			fmt.Fprintf(&b, "\n    working on: %s", t)
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String()), nil
}

// Close ends a channel: members are told, and nothing more can be sent.
func (s *Service) Close(workspaceID string, channelID int64) (Channel, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Channel{}, err
	}
	defer tx.Rollback()
	c, err := s.load(tx, workspaceID, channelID)
	if err != nil {
		return Channel{}, err
	}
	if c.Closed {
		return c, nil
	}
	at := s.now()
	if _, err := tx.Exec(`UPDATE channels SET closed_at = ? WHERE id = ?`, sqlitedb.Stamp(at), c.ID); err != nil {
		return Channel{}, err
	}
	c.Closed = true
	events := []eventlog.Event{}
	ev, err := eventlog.Append(tx, workspaceID, 0, EventClosed, c, at)
	if err != nil {
		return Channel{}, err
	}
	events = append(events, ev)
	note := fmt.Sprintf("[channel #%d %q] closed by the user. Messages can no longer be sent there.", c.ID, c.Subject)
	for _, m := range c.Members {
		// Told, not woken: the note reaches them with their next turn.
		ev, err := eventlog.Append(tx, workspaceID, m, agent.EventMessage, agent.MessageData{From: agent.FromChannel, Body: note, Channel: c.ID}, at)
		if err != nil {
			return Channel{}, err
		}
		events = append(events, ev)
	}
	if err := tx.Commit(); err != nil {
		return Channel{}, err
	}
	for _, e := range events {
		s.publish(e)
	}
	return c, nil
}

// ---- storage -------------------------------------------------------------------

func (s *Service) load(q sqlitedb.Querier, ws string, id int64) (Channel, error) {
	var c Channel
	var at, closed string
	err := q.QueryRow(`SELECT id, workspace_id, subject, kind, creator_id, created_at, closed_at,
		EXISTS (SELECT 1 FROM channel_links l WHERE l.channel_id = channels.id) FROM channels WHERE workspace_id = ? AND id = ?`, ws, id).
		Scan(&c.ID, &c.WorkspaceID, &c.Subject, &c.Kind, &c.CreatorID, &at, &closed, &c.Published)
	c.Closed = closed != ""
	if errors.Is(err, sql.ErrNoRows) {
		return c, fmt.Errorf("%w: channel #%d", apperr.NotFound, id)
	}
	if err != nil {
		return c, err
	}
	c.CreatedAt = sqlitedb.ParseStamp(at)
	rows, err := q.Query(`SELECT agent_id FROM channel_members WHERE channel_id = ? ORDER BY agent_id`, id)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		var m int64
		if err := rows.Scan(&m); err != nil {
			return c, err
		}
		c.Members = append(c.Members, m)
	}
	return c, rows.Err()
}

func isMember(c Channel, id int64) bool {
	for _, m := range c.Members {
		if m == id {
			return true
		}
	}
	return false
}

// Create makes a channel with the creator and the given members.
func (s *Service) Create(creator agent.Agent, subject string, members []int64, kind string) (Channel, error) {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return Channel{}, errors.New("a channel needs a subject")
	}
	contacts, err := s.contacts(creator)
	if err != nil {
		return Channel{}, err
	}
	allowed := map[int64]bool{}
	for _, c := range contacts {
		allowed[c.ID] = true
	}
	set := map[int64]bool{creator.ID: true}
	for _, m := range members {
		if m == creator.ID {
			continue
		}
		if !allowed[m] {
			return Channel{}, fmt.Errorf("agent #%d cannot be added: %s", m, ruleText(creator))
		}
		set[m] = true
	}
	if len(set) < 2 {
		return Channel{}, errors.New("a channel needs at least one other member")
	}
	ids := make([]int64, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	tx, err := s.db.Begin()
	if err != nil {
		return Channel{}, err
	}
	defer tx.Rollback()
	at := s.now()
	res, err := tx.Exec(`INSERT INTO channels (workspace_id, subject, kind, creator_id, created_at) VALUES (?, ?, ?, ?, ?)`, creator.WorkspaceID, subject, kind, creator.ID, sqlitedb.Stamp(at))
	if err != nil {
		return Channel{}, err
	}
	id, _ := res.LastInsertId()
	for _, m := range ids {
		if _, err := tx.Exec(`INSERT INTO channel_members (channel_id, agent_id) VALUES (?, ?)`, id, m); err != nil {
			return Channel{}, err
		}
	}
	c := Channel{ID: id, WorkspaceID: creator.WorkspaceID, Subject: subject, Kind: kind, CreatorID: creator.ID, Members: ids, CreatedAt: at}
	ev, err := eventlog.Append(tx, creator.WorkspaceID, creator.ID, EventCreated, c, at)
	if err != nil {
		return Channel{}, err
	}
	if err := tx.Commit(); err != nil {
		return Channel{}, err
	}
	s.publish(ev)
	return c, nil
}

func ruleText(a agent.Agent) string {
	if a.ParentID == 0 {
		return "a top-level agent can include other top-level agents and its own subagents"
	}
	return "a subagent can include only its parent, its sibling subagents, and its own subagents"
}

// Send posts a message and delivers it to every other member's transcript.
func (s *Service) Send(sender agent.Agent, channelID int64, body string) (Message, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return Message{}, errors.New("a message needs content")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Message{}, err
	}
	defer tx.Rollback()
	c, err := s.load(tx, sender.WorkspaceID, channelID)
	if err != nil {
		return Message{}, err
	}
	if !isMember(c, sender.ID) {
		return Message{}, fmt.Errorf("you are not a member of channel #%d", channelID)
	}
	header := fmt.Sprintf("[channel #%d %q] message from agent #%d (%s):", c.ID, c.Subject, sender.ID, sender.Title)
	return s.deliver(tx, c, sender.ID, "", header, body)
}

// deliver posts body to channel c (in tx, which it commits) and puts it, after
// header, in every other live member's transcript, waking those not paused.
// senderID 0 is a message from outside, named senderName.
func (s *Service) deliver(tx *sql.Tx, c Channel, senderID int64, senderName, header, body string) (Message, error) {
	ws, channelID := c.WorkspaceID, c.ID
	if c.Closed {
		return Message{}, fmt.Errorf("channel #%d was closed by the user; nothing more can be sent there", channelID)
	}
	at := s.now()
	var recent int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM channel_messages WHERE channel_id = ? AND created_at > ?`, channelID, sqlitedb.Stamp(at.Add(-burstWindow))).Scan(&recent); err != nil {
		return Message{}, err
	}
	if recent >= burstLimit {
		return Message{}, fmt.Errorf("channel #%d is busy (%d messages in %s); stop and ask the user before continuing the exchange", channelID, recent, burstWindow)
	}
	var sid any // NULL for an outside sender
	if senderID != 0 {
		sid = senderID
	}
	res, err := tx.Exec(`INSERT INTO channel_messages (channel_id, sender_id, sender_name, body, created_at) VALUES (?, ?, ?, ?, ?)`, channelID, sid, senderName, body, sqlitedb.Stamp(at))
	if err != nil {
		return Message{}, err
	}
	id, _ := res.LastInsertId()
	msg := Message{ID: id, ChannelID: channelID, SenderID: senderID, SenderName: senderName, Body: body, CreatedAt: at}
	events := []eventlog.Event{}
	ev, err := eventlog.Append(tx, ws, senderID, EventMessage, msg, at)
	if err != nil {
		return Message{}, err
	}
	events = append(events, ev)
	text := header + "\n" + body
	var woken []int64
	for _, m := range c.Members {
		if m == senderID {
			continue
		}
		var status string
		if err := tx.QueryRow(`SELECT status FROM agents WHERE workspace_id = ? AND id = ?`, ws, m).Scan(&status); err != nil || status == agent.StatusTerminated {
			continue
		}
		ev, err := eventlog.Append(tx, ws, m, agent.EventMessage, agent.MessageData{From: agent.FromChannel, Body: text, Channel: c.ID, Sender: senderID}, at)
		if err != nil {
			return Message{}, err
		}
		events = append(events, ev)
		// A paused agent was stopped by the user: it gets the message but
		// stays paused until the user resumes it.
		if status != agent.StatusPaused {
			woken = append(woken, m)
		}
	}
	if err := tx.Commit(); err != nil {
		return Message{}, err
	}
	for _, e := range events {
		s.publish(e)
	}
	if s.Wake != nil {
		for _, m := range woken {
			s.Wake(ws, m)
		}
	}
	return msg, nil
}

// From names a message's sender for agents and digests.
func (m Message) From() string {
	if m.SenderID == 0 {
		return fmt.Sprintf("outside (%s)", m.SenderName)
	}
	return fmt.Sprintf("#%d", m.SenderID)
}

func (s *Service) messages(ws string, channelID int64, limit int) ([]Message, error) {
	return s.messagesAfter(ws, channelID, 0, limit)
}

// messagesAfter lists the latest limit messages with ids above after, oldest first.
func (s *Service) messagesAfter(ws string, channelID, after int64, limit int) ([]Message, error) {
	rows, err := s.db.Query(`SELECT m.id, m.channel_id, COALESCE(m.sender_id, 0), m.sender_name, m.body, m.created_at FROM channel_messages m
		JOIN channels c ON c.id = m.channel_id WHERE c.workspace_id = ? AND m.channel_id = ? AND m.id > ? ORDER BY m.id DESC LIMIT ?`, ws, channelID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		var at string
		if err := rows.Scan(&m.ID, &m.ChannelID, &m.SenderID, &m.SenderName, &m.Body, &at); err != nil {
			return nil, err
		}
		m.CreatedAt = sqlitedb.ParseStamp(at)
		out = append([]Message{m}, out...)
	}
	return out, rows.Err()
}

func (s *Service) channelsOf(ws string, agentID int64) ([]Channel, error) {
	q := `SELECT id FROM channels WHERE workspace_id = ? ORDER BY id`
	args := []any{ws}
	if agentID != 0 {
		q = `SELECT c.id FROM channels c JOIN channel_members m ON m.channel_id = c.id WHERE c.workspace_id = ? AND m.agent_id = ? ORDER BY c.id`
		args = append(args, agentID)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	out := []Channel{}
	for _, id := range ids {
		c, err := s.load(s.db, ws, id)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// View is a channel with its messages, for the client.
type View struct {
	Channel
	Messages []Message `json:"messages"`
}

// Channels lists a workspace's channels with their messages (the latest 200
// each), for the user to read along.
func (s *Service) Channels(workspaceID string) ([]View, error) {
	chans, err := s.channelsOf(workspaceID, 0)
	if err != nil {
		return nil, err
	}
	out := []View{}
	for _, c := range chans {
		msgs, err := s.messages(workspaceID, c.ID, 200)
		if err != nil {
			return nil, err
		}
		if msgs == nil {
			msgs = []Message{}
		}
		out = append(out, View{Channel: c, Messages: msgs})
	}
	return out, nil
}

// Owner is the client-facing view of channels: read-only. Creating channels
// and sending messages take the acting agent, so only agent tools reach
// them; binding Service itself would let a client post as any agent.
type Owner struct{ s *Service }

func (s *Service) Owner() *Owner { return &Owner{s: s} }

// Channels lists the workspace's channels with their messages.
func (o *Owner) Channels(workspaceID string) ([]View, error) { return o.s.Channels(workspaceID) }

type CloseRequest struct {
	WorkspaceID string `json:"workspaceId"`
	ChannelID   int64  `json:"channelId"`
}

// Close closes a channel: its members are told, and nothing more can be sent.
func (o *Owner) Close(req CloseRequest) (Channel, error) {
	return o.s.Close(req.WorkspaceID, req.ChannelID)
}

// Digest renders the workspace's channels for /btw: subject, members, and
// the last few messages of each (cut short).
func (s *Service) Digest(workspaceID string) string {
	views, err := s.Channels(workspaceID)
	if err != nil || len(views) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Channels\n")
	for _, v := range views {
		fmt.Fprintf(&b, "Channel #%d %q, members %s, %d messages\n", v.ID, v.Subject, ids(v.Members), len(v.Messages))
		msgs := v.Messages
		if len(msgs) > 4 {
			msgs = msgs[len(msgs)-4:]
		}
		for _, m := range msgs {
			body := strings.Join(strings.Fields(m.Body), " ")
			if r := []rune(body); len(r) > 300 {
				body = string(r[:300]) + "…"
			}
			fmt.Fprintf(&b, "- %s: %s\n", m.From(), body)
		}
	}
	return strings.TrimSpace(b.String())
}

// spawnChannel finds the channel between a spawned subagent and its parent.
func (s *Service) spawnChannel(child agent.Agent) (int64, bool) {
	var id int64
	err := s.db.QueryRow(`SELECT c.id FROM channels c JOIN channel_members m ON m.channel_id = c.id
		WHERE c.workspace_id = ? AND c.kind = ? AND m.agent_id = ? ORDER BY c.id LIMIT 1`, child.WorkspaceID, KindSpawn, child.ID).Scan(&id)
	return id, err == nil
}

// OnFinal posts a subagent's final answer to its spawn channel, waking the
// parent. The runner calls it when an agent's turn ends with an answer.
func (s *Service) OnFinal(a agent.Agent, answer string) {
	if a.ParentID == 0 || strings.TrimSpace(answer) == "" {
		return
	}
	if id, ok := s.spawnChannel(a); ok {
		_, _ = s.Send(a, id, "Final answer:\n"+answer)
	}
}

// ---- tools for agents ----------------------------------------------------------

var str = map[string]any{"type": "string"}
var integer = map[string]any{"type": "integer"}

// Tools are the channel tools and spawn_agent, for the runner.
type Tools struct{ s *Service }

func (s *Service) Tools() *Tools { return &Tools{s: s} }

var effects = map[string]string{
	"agents_list": tool.Read, "channel_list": tool.Read, "channel_read": tool.Read, "models_list": tool.Read,
	// Talking within the family changes no files and runs nothing. Prepare
	// raises channel_create to tool.Contact when it reaches outside the
	// family, so the user approves who starts talking to whom.
	"channel_create": tool.Read, "channel_send": tool.Read,
	// A new agent spends tokens and can act: gated like a file write.
	"spawn_agent": tool.Write,
	// Ending its own subagents (and theirs) only stops work it started.
	"subagent_terminate": tool.Read,
}

func (t *Tools) Owns(name string) bool { _, ok := effects[name]; return ok }

// Catalog describes these tools for the Tools panel.
func (t *Tools) Catalog() []tool.Info {
	var out []tool.Info
	for _, spec := range t.Specs("") {
		out = append(out, tool.Info{Name: spec.Name, Description: spec.Description, Effect: effects[spec.Name], Source: "channels"})
	}
	return out
}

func (t *Tools) Specs(string) []llm.Tool {
	return []llm.Tool{
		{Name: "agents_list", Description: "List the agents you can talk to through channels: their ID, title, project, and status.",
			Schema: llm.ToolSchema{Properties: map[string]any{}}},
		{Name: "channel_create", Description: "Open a channel with other agents (see agents_list for who you may include). Messages sent there reach every member and wake them. A channel with agents outside your own family (your parent, siblings, and subagents) waits for the user's approval, so say in message who you are and why you are reaching out.",
			Schema: llm.ToolSchema{Properties: map[string]any{"subject": str, "members": map[string]any{"type": "array", "items": integer, "description": "Agent IDs to include besides you."}, "message": map[string]any{"type": "string", "description": "Optional first message, sent when the channel opens."}}, Required: []string{"subject", "members"}}},
		{Name: "channel_send", Description: "Send a message to a channel you are in. Every other member receives it and wakes up; their replies come back to you the same way.",
			Schema: llm.ToolSchema{Properties: map[string]any{"channel": integer, "message": str}, Required: []string{"channel", "message"}}},
		{Name: "channel_list", Description: "List the channels you are in, with their subjects and members.",
			Schema: llm.ToolSchema{Properties: map[string]any{}}},
		{Name: "channel_read", Description: "Read a channel's recent messages (default the last 30).",
			Schema: llm.ToolSchema{Properties: map[string]any{"channel": integer, "limit": integer}, Required: []string{"channel"}}},
		{Name: "models_list", Description: "List the models a subagent can use: those configured for your provider, cheapest first, with prices.",
			Schema: llm.ToolSchema{Properties: map[string]any{}}},
		{Name: "subagent_terminate", Description: "End one of your own subagents (and its subagents) that is no longer needed, e.g. when its task is done or went wrong. Only your subagents, never other agents.",
			Schema: llm.ToolSchema{Properties: map[string]any{"agent": map[string]any{"type": "integer", "description": "The subagent's ID."}}, Required: []string{"agent"}}},
		{Name: "spawn_agent", Description: "Start a subagent on a focused task. It works in your project with at most your permissions, and gets a channel with you; its final answer arrives in that channel. Use it to split independent work; keep the task self-contained. Choose its model with cost in mind: prefer the cheapest model (see models_list) that can do the task well, and keep larger models for hard reasoning.",
			Schema: llm.ToolSchema{Properties: map[string]any{"task": str, "title": map[string]any{"type": "string", "description": "A short title for its card."}, "model": map[string]any{"type": "string", "description": "A model from models_list. Default: your own model."}}, Required: []string{"task"}}},
	}
}

type input struct {
	Subject string  `json:"subject"`
	Members []int64 `json:"members"`
	Channel int64   `json:"channel"`
	Message string  `json:"message"`
	Limit   int     `json:"limit"`
	Task    string  `json:"task"`
	Title   string  `json:"title"`
	Model   string  `json:"model"`
	Agent   int64   `json:"agent"`
}

func (t *Tools) Prepare(ws string, a agent.Agent, c llm.ToolCall) (tool.Call, error) {
	call := tool.Call{ID: c.ID, Name: c.Name, Effect: effects[c.Name], Args: c.Input}
	var in input
	dec := json.NewDecoder(strings.NewReader(string(c.Input)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return call, fmt.Errorf("invalid input for %s: %v", c.Name, err)
	}
	switch c.Name {
	case "channel_create":
		call.Summary = fmt.Sprintf("%q with %s", in.Subject, ids(in.Members))
		out, err := t.s.outsiders(a, in.Members)
		if err != nil {
			return call, err
		}
		if out != "" {
			call.Effect = tool.Contact
			call.Preview = "Opens a channel with agents outside #" + fmt.Sprint(a.ID) + "'s family:\n" + out
			if m := strings.TrimSpace(in.Message); m != "" {
				call.Preview += "\n\nFirst message:\n" + m
			}
		}
	case "channel_send":
		call.Summary = fmt.Sprintf("channel #%d: %s", in.Channel, firstLine(in.Message))
	case "channel_read":
		call.Summary = fmt.Sprintf("channel #%d", in.Channel)
	case "subagent_terminate":
		target, err := t.s.descendant(a, in.Agent)
		if err != nil {
			return call, err
		}
		call.Summary = fmt.Sprintf("#%d %s", target.ID, target.Title)
	case "spawn_agent":
		if strings.TrimSpace(in.Task) == "" {
			return call, errors.New("spawn_agent needs a task")
		}
		model := strings.TrimSpace(in.Model)
		if model == "" {
			model = a.Model
		}
		if err := t.s.allowedModel(a, model); err != nil {
			return call, err
		}
		call.Summary = firstLine(in.Title + " " + in.Task)
		call.Preview = "Model: " + model + "\n\n" + in.Task
	default:
		call.Summary = c.Name
	}
	return call, nil
}

func (t *Tools) Execute(_ context.Context, ws string, a agent.Agent, call tool.Call) (string, error) {
	var in input
	_ = json.Unmarshal(call.Args, &in)
	s := t.s
	switch call.Name {
	case "agents_list":
		contacts, err := s.contacts(a)
		if err != nil {
			return "", err
		}
		if len(contacts) == 0 {
			return "No other agents you can talk to. (" + ruleText(a) + ".)", nil
		}
		var b strings.Builder
		for _, c := range contacts {
			rel := ""
			switch {
			case c.ParentID == a.ID:
				rel = " (your subagent)"
			case c.ID == a.ParentID:
				rel = " (your parent)"
			case a.ParentID != 0 && c.ParentID == a.ParentID:
				rel = " (sibling)"
			}
			fmt.Fprintf(&b, "#%d %s%s · project %d · %s\n", c.ID, c.Title, rel, c.ProjectID, c.Status)
		}
		return strings.TrimSpace(b.String()), nil
	case "channel_create":
		c, err := s.Create(a, in.Subject, in.Members, "")
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(in.Message) != "" {
			if _, err := s.Send(a, c.ID, in.Message); err != nil {
				return fmt.Sprintf("Opened channel #%d %q with %s, but the first message failed: %v", c.ID, c.Subject, ids(c.Members), err), nil
			}
			return fmt.Sprintf("Opened channel #%d %q with %s and sent your message.", c.ID, c.Subject, ids(c.Members)), nil
		}
		return fmt.Sprintf("Opened channel #%d %q with %s.", c.ID, c.Subject, ids(c.Members)), nil
	case "channel_send":
		if _, err := s.Send(a, in.Channel, in.Message); err != nil {
			return "", err
		}
		return fmt.Sprintf("Sent to channel #%d.", in.Channel), nil
	case "channel_list":
		chans, err := s.channelsOf(ws, a.ID)
		if err != nil {
			return "", err
		}
		if len(chans) == 0 {
			return "You are in no channels.", nil
		}
		var b strings.Builder
		for _, c := range chans {
			closed := ""
			if c.Closed {
				closed = " · closed"
			}
			fmt.Fprintf(&b, "#%d %q · members %s%s\n", c.ID, c.Subject, ids(c.Members), closed)
		}
		return strings.TrimSpace(b.String()), nil
	case "channel_read":
		c, err := s.load(s.db, ws, in.Channel)
		if err != nil {
			return "", err
		}
		if !isMember(c, a.ID) {
			return "", fmt.Errorf("you are not a member of channel #%d", in.Channel)
		}
		limit := in.Limit
		if limit <= 0 || limit > 200 {
			limit = 30
		}
		msgs, err := s.messages(ws, in.Channel, limit)
		if err != nil {
			return "", err
		}
		if len(msgs) == 0 {
			return "No messages yet.", nil
		}
		var b strings.Builder
		for _, m := range msgs {
			fmt.Fprintf(&b, "%s at %s:\n%s\n\n", m.From(), m.CreatedAt.Format("15:04:05"), m.Body)
		}
		return tool.Clip(strings.TrimSpace(b.String())), nil
	case "spawn_agent":
		return s.spawn(a, in.Task, in.Title, in.Model)
	case "subagent_terminate":
		target, err := s.descendant(a, in.Agent)
		if err != nil {
			return "", err
		}
		if _, err := s.agents.Terminate(agent.ControlRequest{IdempotencyKey: fmt.Sprintf("subagent-terminate-%d-%d-%d", a.ID, target.ID, s.now().UnixNano()), WorkspaceID: a.WorkspaceID, AgentID: target.ID}); err != nil {
			return "", err
		}
		return fmt.Sprintf("Terminated subagent #%d (%s) and its subagents.", target.ID, target.Title), nil
	case "models_list":
		return s.modelsList(a)
	}
	return "", fmt.Errorf("unknown tool %s", call.Name)
}

// spawn creates a subagent of a and a channel between them, then starts it.
// choices lists the models parent's provider offers, cheapest first (priced
// by input plus output; unpriced last).
func (s *Service) choices(parent agent.Agent) ([]string, error) {
	if s.Models == nil {
		return []string{parent.Model}, nil
	}
	models, err := s.Models(parent.WorkspaceID, parent.Provider)
	if err != nil {
		return nil, err
	}
	cost := func(m string) float64 {
		if s.Price != nil {
			if in, out, ok := s.Price(m); ok {
				return in + out
			}
		}
		return 1e18
	}
	sort.SliceStable(models, func(i, j int) bool { return cost(models[i]) < cost(models[j]) })
	return models, nil
}

// allowedModel accepts the parent's own model or one configured for its provider.
func (s *Service) allowedModel(parent agent.Agent, model string) error {
	if model == parent.Model {
		return nil
	}
	models, err := s.choices(parent)
	if err != nil {
		return err
	}
	for _, m := range models {
		if m == model {
			return nil
		}
	}
	return fmt.Errorf("model %q is not configured for your provider; choose one of: %s", model, strings.Join(models, ", "))
}

func (s *Service) modelsList(a agent.Agent) (string, error) {
	models, err := s.choices(a)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, m := range models {
		price := "price unknown"
		if s.Price != nil {
			if in, out, ok := s.Price(m); ok {
				price = fmt.Sprintf("$%g in / $%g out per million tokens", in, out)
			}
		}
		mark := ""
		if m == a.Model {
			mark = " (yours)"
		}
		fmt.Fprintf(&b, "%s%s · %s\n", m, mark, price)
	}
	return strings.TrimSpace(b.String()), nil
}

func (s *Service) spawn(parent agent.Agent, task, title, model string) (string, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		model = parent.Model
	}
	if err := s.allowedModel(parent, model); err != nil {
		return "", err
	}
	snap, err := s.agents.Snapshot(parent.WorkspaceID)
	if err != nil {
		return "", err
	}
	byID := map[int64]agent.Agent{}
	for _, a := range snap.Agents {
		byID[a.ID] = a
	}
	depth := 1
	for p := parent; p.ParentID != 0; p = byID[p.ParentID] {
		depth++
	}
	if depth >= maxDepth {
		return "", fmt.Errorf("subagents nest at most %d levels; do this task yourself or report back to your parent", maxDepth)
	}
	open := 0
	for _, a := range snap.Agents {
		if a.ParentID == parent.ID && a.Status != agent.StatusComplete && a.Status != agent.StatusTerminated {
			open++
		}
	}
	if open >= maxChildren {
		return "", fmt.Errorf("you already have %d unfinished subagents; wait for their answers before starting more", open)
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = agent.TitleFrom(task)
	}
	child, err := s.agents.Create(agent.CreateRequest{
		IdempotencyKey: fmt.Sprintf("spawn-%d-%d", parent.ID, s.now().UnixNano()),
		WorkspaceID:    parent.WorkspaceID, ParentID: parent.ID, Title: title, Role: "Subagent",
		Provider: parent.Provider, Model: model,
	})
	if err != nil {
		return "", err
	}
	c, err := s.Create(parent, fmt.Sprintf("#%d %s", child.ID, title), []int64{child.ID}, KindSpawn)
	if err != nil {
		return "", err
	}
	brief := fmt.Sprintf("%s\n\n(You are a subagent of agent #%d. Channel #%d connects you with it: use channel_send there for questions or progress. Your final answer is posted there automatically when you finish.)", task, parent.ID, c.ID)
	// The task arrives like a channel message from the parent, and starts it.
	if err := s.store.Append(parent.WorkspaceID, child.ID, agent.MessageData{From: agent.FromChannel, Body: brief, Channel: c.ID, Sender: parent.ID}); err != nil {
		return "", err
	}
	if s.Wake != nil {
		s.Wake(parent.WorkspaceID, child.ID)
	}
	return fmt.Sprintf("Started subagent #%d (%q, %s) in channel #%d. Its final answer will arrive in that channel.", child.ID, title, model, c.ID), nil
}

func ids(list []int64) string {
	parts := make([]string, len(list))
	for i, id := range list {
		parts[i] = fmt.Sprintf("#%d", id)
	}
	return strings.Join(parts, ", ")
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}
