// Package eventlog is the ordered, per-workspace record of everything that
// happens: transcripts, state changes, approvals and tool calls. Every
// feature appends here; clients use it to catch up after a disconnect.
//
// SQLite is the index: each event's row holds its sequence number, agent,
// and kind. An agent's event bodies (messages, tool output) live in that
// agent's JSON Lines file, transcripts/<workspace>/<agent>.jsonl beside the
// database, one self-describing line per event; the row records where its
// line is. Workspace-level events, and rows written before files existed,
// keep their body in the row. The row is what counts: a line whose
// transaction rolled back stays in the file but nothing points at it.
package eventlog

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/devark28/xhaos/app/sqlitedb"
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
func Migrate(db *sql.DB) error {
	if _, err := db.Exec(schema); err != nil {
		return err
	}
	// Added with transcript files: where the event's line is (NULL: inline).
	for _, col := range []string{"file_off", "file_len"} {
		if _, err := db.Exec(`ALTER TABLE events ADD COLUMN ` + col + ` INTEGER`); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return err
		}
	}
	// One agent's events by seq: transcript pages and tool-call states.
	_, err := db.Exec(`CREATE INDEX IF NOT EXISTS events_by_agent ON events (workspace_id, agent_id, seq)`)
	return err
}

// ---- transcript files ----------------------------------------------------------

// files holds one database's transcript directory and its open files.
type files struct {
	dir  string
	mu   sync.Mutex
	open map[string]*os.File
}

var stores sync.Map // database file path -> *files

// filesFor returns the transcript store beside q's database, or nil for a
// database without a file (in memory), whose events stay inline.
func filesFor(q sqlitedb.Querier) *files {
	var path string
	if err := q.QueryRow(`SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&path); err != nil || path == "" {
		return nil
	}
	if f, ok := stores.Load(path); ok {
		return f.(*files)
	}
	f, _ := stores.LoadOrStore(path, &files{dir: filepath.Join(filepath.Dir(path), "transcripts"), open: map[string]*os.File{}})
	return f.(*files)
}

// Close closes the transcript files of db's database (on shutdown).
func Close(db *sql.DB) {
	f := filesFor(db)
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for name, h := range f.open {
		h.Close()
		delete(f.open, name)
	}
}

// handle returns the agent's open file; create makes it if missing (reads
// never create one).
func (f *files) handle(workspaceID string, agentID int64, create bool) (*os.File, error) {
	if workspaceID == "" || strings.ContainsAny(workspaceID, `/\`) || strings.HasPrefix(workspaceID, ".") {
		return nil, fmt.Errorf("eventlog: bad workspace id %q", workspaceID)
	}
	name := filepath.Join(f.dir, workspaceID, fmt.Sprintf("%d.jsonl", agentID))
	if h, ok := f.open[name]; ok {
		return h, nil
	}
	flags := os.O_RDWR
	if create {
		if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
			return nil, err
		}
		flags |= os.O_CREATE
	}
	h, err := os.OpenFile(name, flags, 0o600)
	if err != nil {
		return nil, err
	}
	f.open[name] = h
	return h, nil
}

// line is one event in a transcript file.
type line struct {
	Seq   int64           `json:"seq"`
	At    string          `json:"at"`
	Agent int64           `json:"agent"`
	Kind  string          `json:"kind"`
	Data  json.RawMessage `json:"data"`
}

// write appends the event's line and returns where it is.
func (f *files) write(workspaceID string, l line) (off, n int64, err error) {
	raw, err := json.Marshal(l)
	if err != nil {
		return 0, 0, err
	}
	raw = append(raw, '\n')
	f.mu.Lock()
	defer f.mu.Unlock()
	h, err := f.handle(workspaceID, l.Agent, true)
	if err != nil {
		return 0, 0, err
	}
	if off, err = h.Seek(0, io.SeekEnd); err != nil {
		return 0, 0, err
	}
	if _, err := h.WriteAt(raw, off); err != nil {
		return 0, 0, err
	}
	return off, int64(len(raw)), nil
}

// read returns the data of the line at off.
func (f *files) read(workspaceID string, agentID, off, n int64) (json.RawMessage, error) {
	f.mu.Lock()
	h, err := f.handle(workspaceID, agentID, false)
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	buf := make([]byte, n)
	if _, err := h.ReadAt(buf, off); err != nil {
		return nil, err
	}
	var l line
	if err := json.Unmarshal(buf, &l); err != nil {
		return nil, err
	}
	return l.Data, nil
}

// lost stands in for a line that cannot be read (a file deleted by hand, or
// a tail lost in a power cut), so one bad line does not hide a transcript.
var lost = json.RawMessage(`{"lost":true}`)

// scanner reads event rows, resolving bodies kept in files.
type scanner struct {
	files *files
}

const columns = `workspace_id, seq, at, COALESCE(agent_id, 0), kind, data, file_off, file_len`

func (sc scanner) scan(rows *sql.Rows) (Event, error) {
	var e Event
	var at, data string
	var off, n sql.NullInt64
	if err := rows.Scan(&e.WorkspaceID, &e.Seq, &at, &e.AgentID, &e.Kind, &data, &off, &n); err != nil {
		return e, err
	}
	e.At, e.Data = sqlitedb.ParseStamp(at), json.RawMessage(data)
	if off.Valid {
		e.Data = lost
		if sc.files != nil {
			if d, err := sc.files.read(e.WorkspaceID, e.AgentID, off.Int64, n.Int64); err == nil {
				e.Data = d
			}
		}
	}
	return e, nil
}

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
	if f := filesFor(tx); f != nil && agentID != 0 {
		// The line first: a committed row must never point past the file.
		off, n, err := f.write(workspaceID, line{Seq: seq, At: stamp, Agent: agentID, Kind: kind, Data: raw})
		if err != nil {
			return Event{}, fmt.Errorf("transcript file: %w", err)
		}
		if _, err := tx.Exec(`INSERT INTO events (workspace_id, seq, at, agent_id, kind, data, file_off, file_len) VALUES (?, ?, ?, ?, ?, '', ?, ?)`,
			workspaceID, seq, stamp, agentID, kind, off, n); err != nil {
			return Event{}, err
		}
	} else if _, err := tx.Exec(`INSERT INTO events (workspace_id, seq, at, agent_id, kind, data) VALUES (?, ?, ?, ?, ?, ?)`,
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
	return query(q, `SELECT `+columns+` FROM events WHERE workspace_id = ? AND seq > ? ORDER BY seq LIMIT ?`, workspaceID, after, limit)
}

func query(q sqlitedb.Querier, sqlText string, args ...any) ([]Event, error) {
	sc := scanner{files: filesFor(q)}
	rows, err := q.Query(sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		e, err := sc.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Latest returns, for every agent of the workspace, its last limit events of
// the given kinds plus one more when there are older ones (so a caller can
// tell), all in seq order.
func Latest(q sqlitedb.Querier, workspaceID string, kinds []string, limit int) ([]Event, error) {
	marks, args := kindArgs(kinds)
	args = append([]any{workspaceID}, args...)
	return query(q, `SELECT `+columns+` FROM (SELECT *, ROW_NUMBER() OVER (PARTITION BY agent_id ORDER BY seq DESC) AS nth
		FROM events WHERE workspace_id = ? AND agent_id IS NOT NULL AND kind IN (`+marks+`)) WHERE nth <= ? ORDER BY seq`, append(args, limit+1)...)
}

// Before returns up to limit of one agent's events of the given kinds with
// seq < before, the latest ones, in seq order.
func Before(q sqlitedb.Querier, workspaceID string, agentID int64, kinds []string, before int64, limit int) ([]Event, error) {
	marks, args := kindArgs(kinds)
	args = append([]any{workspaceID, agentID, before}, args...)
	events, err := query(q, `SELECT `+columns+` FROM events WHERE workspace_id = ? AND agent_id = ? AND seq < ? AND kind IN (`+marks+`) ORDER BY seq DESC LIMIT ?`, append(args, limit)...)
	slices.Reverse(events)
	return events, err
}

func kindArgs(kinds []string) (string, []any) {
	args := make([]any, len(kinds))
	for i, k := range kinds {
		args[i] = k
	}
	return strings.TrimSuffix(strings.Repeat("?,", len(kinds)), ","), args
}

// ListAgent returns one agent's events of the given kind, in order.
func ListAgent(q sqlitedb.Querier, workspaceID string, agentID int64, kind string) ([]Event, error) {
	return query(q, `SELECT `+columns+` FROM events WHERE workspace_id = ? AND agent_id = ? AND kind = ? ORDER BY seq`, workspaceID, agentID, kind)
}
