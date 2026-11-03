// Package eventlog is the ordered, per-workspace record of everything that
// happens: transcripts, state changes, and later approvals and tool calls.
// Every feature appends here; clients use it to catch up after a disconnect.
package eventlog

import (
	"database/sql"
	"encoding/json"
	"time"

	"xhaos/app/sqlitedb"
)

// Event is one log entry. Seq increases by exactly one per workspace, so a
// client can detect gaps.
type Event struct {
	WorkspaceID string          `json:"workspaceId"`
	Seq         int64           `json:"seq"`
	At          time.Time       `json:"at"`
	AgentID     int64           `json:"agentId,omitempty"`
	Kind        string          `json:"kind"`
	Data        json.RawMessage `json:"data"`
}

// Publisher delivers committed events to live clients. It must not block.
type Publisher func(Event)

const schema = `
CREATE TABLE IF NOT EXISTS events (
	workspace_id TEXT NOT NULL REFERENCES workspaces(id),
	seq          INTEGER NOT NULL,
	at           TEXT NOT NULL,
	agent_id     INTEGER,
	kind         TEXT NOT NULL,
	data         TEXT NOT NULL,
	PRIMARY KEY (workspace_id, seq)
);`

// Migrate creates the events table. Run it after the workspace migration.
func Migrate(db *sql.DB) error { _, err := db.Exec(schema); return err }

// LastSeq returns the workspace's latest sequence number, or 0.
func LastSeq(q sqlitedb.Querier, workspaceID string) (int64, error) {
	var seq int64
	err := q.QueryRow(`SELECT COALESCE(MAX(seq), 0) FROM events WHERE workspace_id = ?`, workspaceID).Scan(&seq)
	return seq, err
}

// Append records an event inside the caller's transaction, so the event and
// the state change it describes commit together. Publish it only after commit.
func Append(tx *sql.Tx, workspaceID string, agentID int64, kind string, data any, at time.Time) (Event, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return Event{}, err
	}
	seq, err := LastSeq(tx, workspaceID)
	if err != nil {
		return Event{}, err
	}
	seq++
	stamp := sqlitedb.Stamp(at)
	if _, err := tx.Exec(`INSERT INTO events (workspace_id, seq, at, agent_id, kind, data) VALUES (?, ?, ?, ?, ?, ?)`,
		workspaceID, seq, stamp, sqlitedb.NullID(agentID), kind, string(raw)); err != nil {
		return Event{}, err
	}
	return Event{WorkspaceID: workspaceID, Seq: seq, At: sqlitedb.ParseStamp(stamp), AgentID: agentID, Kind: kind, Data: raw}, nil
}

// Service exposes the log to clients.
type Service struct{ db *sql.DB }

func NewService(db *sql.DB) *Service { return &Service{db: db} }

// EventsAfter returns up to limit events with seq > after, in order.
// limit <= 0 means 500; it is capped at 1000.
func (s *Service) EventsAfter(workspaceID string, after int64, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 500
	}
	return List(s.db, workspaceID, after, min(limit, 1000))
}

// List returns events with seq > after in order; limit <= 0 means no limit.
// Features use it inside their own transactions (for example, snapshots).
func List(q sqlitedb.Querier, workspaceID string, after int64, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = -1 // SQLite: no limit
	}
	rows, err := q.Query(`SELECT workspace_id, seq, at, COALESCE(agent_id, 0), kind, data FROM events
		WHERE workspace_id = ? AND seq > ? ORDER BY seq LIMIT ?`, workspaceID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		var at, data string
		if err := rows.Scan(&e.WorkspaceID, &e.Seq, &at, &e.AgentID, &e.Kind, &data); err != nil {
			return nil, err
		}
		e.At, e.Data = sqlitedb.ParseStamp(at), json.RawMessage(data)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ListAgent returns one agent's events of the given kind, in order.
func ListAgent(q sqlitedb.Querier, workspaceID string, agentID int64, kind string) ([]Event, error) {
	rows, err := q.Query(`SELECT workspace_id, seq, at, COALESCE(agent_id, 0), kind, data FROM events
		WHERE workspace_id = ? AND agent_id = ? AND kind = ? ORDER BY seq`, workspaceID, agentID, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		var at, data string
		if err := rows.Scan(&e.WorkspaceID, &e.Seq, &at, &e.AgentID, &e.Kind, &data); err != nil {
			return nil, err
		}
		e.At, e.Data = sqlitedb.ParseStamp(at), json.RawMessage(data)
		out = append(out, e)
	}
	return out, rows.Err()
}
