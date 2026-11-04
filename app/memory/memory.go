// Package memory keeps a workspace's shared memories: short notes (a
// preference, a convention, a decision) that every agent in the workspace
// should know about. Agents see an index of them in their system prompt,
// which is rebuilt every turn and so survives compaction, and read one in
// full with memory_read. The user edits them in the Memories tab; agents add
// them with memory_save, gated like a file write.
package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"xhaos/app/agent"
	"xhaos/app/apperr"
	"xhaos/app/eventlog"
	"xhaos/app/llm"
	"xhaos/app/sqlitedb"
	"xhaos/app/tool"
)

const (
	EventSaved   = "memory.saved"
	EventDeleted = "memory.deleted"

	// The index shows at most indexEntries memories, each clipped to its
	// first line and indexWidth characters.
	indexEntries = 60
	indexWidth   = 120
	maxBody      = 20_000
)

type Memory struct {
	ID          int64     `json:"id"`
	WorkspaceID string    `json:"workspaceId"`
	Content     string    `json:"content"`
	Source      string    `json:"source"` // "user", or "agent #N"
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

const schema = `
CREATE TABLE IF NOT EXISTS memories (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	workspace_id TEXT NOT NULL REFERENCES workspaces(id),
	content      TEXT NOT NULL,
	source       TEXT NOT NULL,
	created_at   TEXT NOT NULL,
	updated_at   TEXT NOT NULL
);`

func Migrate(db *sql.DB) error {
	_, err := db.Exec(schema)
	return err
}

type Service struct {
	db      *sql.DB
	publish eventlog.Publisher
	now     func() time.Time
}

func NewService(db *sql.DB, publish eventlog.Publisher) *Service {
	if publish == nil {
		publish = func(eventlog.Event) {}
	}
	return &Service{db: db, publish: publish, now: time.Now}
}

func scan(rows *sql.Rows) ([]Memory, error) {
	defer rows.Close()
	out := []Memory{}
	for rows.Next() {
		var m Memory
		var c, u string
		if err := rows.Scan(&m.ID, &m.WorkspaceID, &m.Content, &m.Source, &c, &u); err != nil {
			return nil, err
		}
		m.CreatedAt, m.UpdatedAt = sqlitedb.ParseStamp(c), sqlitedb.ParseStamp(u)
		out = append(out, m)
	}
	return out, rows.Err()
}

const columns = `id, workspace_id, content, source, created_at, updated_at`

// Memories lists a workspace's memories, newest first.
func (s *Service) Memories(workspaceID string) ([]Memory, error) {
	rows, err := s.db.Query(`SELECT `+columns+` FROM memories WHERE workspace_id = ? ORDER BY id DESC`, workspaceID)
	if err != nil {
		return nil, err
	}
	return scan(rows)
}

func (s *Service) get(workspaceID string, id int64) (Memory, error) {
	rows, err := s.db.Query(`SELECT `+columns+` FROM memories WHERE workspace_id = ? AND id = ?`, workspaceID, id)
	if err != nil {
		return Memory{}, err
	}
	list, err := scan(rows)
	if err != nil {
		return Memory{}, err
	}
	if len(list) == 0 {
		return Memory{}, fmt.Errorf("%w: memory #%d", apperr.NotFound, id)
	}
	return list[0], nil
}

type SaveRequest struct {
	WorkspaceID string `json:"workspaceId"`
	ID          int64  `json:"id,omitempty"` // 0 adds a memory
	Content     string `json:"content"`
}

// Save adds or edits a memory as the user.
func (s *Service) Save(req SaveRequest) (Memory, error) { return s.save(req, "user", 0) }

// Import adds the user's memories kept by an older client in its browser,
// skipping any whose content is already saved. It returns the full list.
func (s *Service) Import(workspaceID string, contents []string) ([]Memory, error) {
	have, err := s.Memories(workspaceID)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, m := range have {
		seen[m.Content] = true
	}
	for i := len(contents) - 1; i >= 0; i-- { // oldest first, so newest gets the highest ID
		c := strings.TrimSpace(contents[i])
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		if _, err := s.Save(SaveRequest{WorkspaceID: workspaceID, Content: c}); err != nil {
			return nil, err
		}
	}
	return s.Memories(workspaceID)
}

func (s *Service) save(req SaveRequest, source string, agentID int64) (Memory, error) {
	content := strings.TrimSpace(req.Content)
	if content == "" {
		return Memory{}, fmt.Errorf("%w: a memory needs content", apperr.Invalid)
	}
	if len(content) > maxBody {
		return Memory{}, fmt.Errorf("%w: a memory is at most %d characters; keep it short and point to a file for details", apperr.Invalid, maxBody)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Memory{}, err
	}
	defer tx.Rollback()
	at := sqlitedb.Stamp(s.now())
	id := req.ID
	if id == 0 {
		res, err := tx.Exec(`INSERT INTO memories (workspace_id, content, source, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`, req.WorkspaceID, content, source, at, at)
		if err != nil {
			return Memory{}, err
		}
		id, _ = res.LastInsertId()
	} else {
		res, err := tx.Exec(`UPDATE memories SET content = ?, updated_at = ? WHERE workspace_id = ? AND id = ?`, content, at, req.WorkspaceID, id)
		if err != nil {
			return Memory{}, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return Memory{}, fmt.Errorf("%w: memory #%d", apperr.NotFound, id)
		}
	}
	var m Memory
	var c, u string
	if err := tx.QueryRow(`SELECT `+columns+` FROM memories WHERE id = ?`, id).Scan(&m.ID, &m.WorkspaceID, &m.Content, &m.Source, &c, &u); err != nil {
		return Memory{}, err
	}
	m.CreatedAt, m.UpdatedAt = sqlitedb.ParseStamp(c), sqlitedb.ParseStamp(u)
	ev, err := eventlog.Append(tx, req.WorkspaceID, agentID, EventSaved, m, s.now())
	if err != nil {
		return Memory{}, err
	}
	if err := tx.Commit(); err != nil {
		return Memory{}, err
	}
	s.publish(ev)
	return m, nil
}

// Delete removes a memory.
func (s *Service) Delete(workspaceID string, id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`DELETE FROM memories WHERE workspace_id = ? AND id = ?`, workspaceID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: memory #%d", apperr.NotFound, id)
	}
	ev, err := eventlog.Append(tx, workspaceID, 0, EventDeleted, map[string]int64{"id": id}, s.now())
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.publish(ev)
	return nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i]) + " …"
	}
	if r := []rune(s); len(r) > indexWidth {
		s = string(r[:indexWidth]) + "…"
	}
	return s
}

// Index is the system prompt's list of memories: one line each.
func (s *Service) Index(workspaceID string) string {
	list, err := s.Memories(workspaceID)
	if err != nil || len(list) == 0 {
		return "There are no workspace memories yet. When you learn something every agent here should know (a preference the user stated, a convention, a decision), save it with memory_save."
	}
	var b strings.Builder
	b.WriteString("Workspace memories, shared by every agent here (newest first; read one in full with memory_read, add one with memory_save):\n")
	for i, m := range list {
		if i == indexEntries {
			fmt.Fprintf(&b, "… and %d older; memory_list shows them all.\n", len(list)-indexEntries)
			break
		}
		fmt.Fprintf(&b, "- #%d %s\n", m.ID, firstLine(m.Content))
	}
	return strings.TrimSpace(b.String())
}

// ---- tools for agents ----------------------------------------------------------

type Tools struct{ s *Service }

func (s *Service) Tools() *Tools { return &Tools{s: s} }

var effects = map[string]string{
	"memory_list": tool.Read, "memory_read": tool.Read,
	// Memories reach every agent's prompt: gated like a file write.
	"memory_save": tool.Write,
}

func (t *Tools) Owns(name string) bool { _, ok := effects[name]; return ok }

// Catalog describes these tools for the Tools panel.
func (t *Tools) Catalog() []tool.Info {
	var out []tool.Info
	for _, spec := range t.Specs("") {
		out = append(out, tool.Info{Name: spec.Name, Description: spec.Description, Effect: effects[spec.Name], Source: "memory"})
	}
	return out
}

func (t *Tools) Specs(string) []llm.Tool {
	integer := map[string]any{"type": "integer"}
	return []llm.Tool{
		{Name: "memory_list", Description: "List every workspace memory with its first line.", Schema: llm.ToolSchema{Properties: map[string]any{}}},
		{Name: "memory_read", Description: "Read a workspace memory in full.", Schema: llm.ToolSchema{Properties: map[string]any{"id": integer}, Required: []string{"id"}}},
		{Name: "memory_save", Description: "Save a short note every agent in this workspace will see in its prompt: a preference the user stated, a project convention, or a decision. Start with a one-line summary. Pass id to update an existing memory instead of adding a near-duplicate.",
			Schema: llm.ToolSchema{Properties: map[string]any{"content": map[string]any{"type": "string"}, "id": integer}, Required: []string{"content"}}},
	}
}

type input struct {
	ID      int64  `json:"id"`
	Content string `json:"content"`
}

func (t *Tools) Prepare(_ string, _ agent.Agent, c llm.ToolCall) (tool.Call, error) {
	call := tool.Call{ID: c.ID, Name: c.Name, Effect: effects[c.Name], Args: c.Input}
	var in input
	dec := json.NewDecoder(strings.NewReader(string(c.Input)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return call, fmt.Errorf("invalid input for %s: %v", c.Name, err)
	}
	switch c.Name {
	case "memory_read":
		call.Summary = fmt.Sprintf("memory #%d", in.ID)
	case "memory_save":
		if strings.TrimSpace(in.Content) == "" {
			return call, errors.New("memory_save needs content")
		}
		call.Summary = firstLine(in.Content)
		if in.ID != 0 {
			call.Summary = fmt.Sprintf("#%d: %s", in.ID, call.Summary)
		}
		call.Preview = in.Content
	default:
		call.Summary = "memories"
	}
	return call, nil
}

func (t *Tools) Execute(_ context.Context, ws string, a agent.Agent, call tool.Call) (string, error) {
	var in input
	_ = json.Unmarshal(call.Args, &in)
	switch call.Name {
	case "memory_list":
		list, err := t.s.Memories(ws)
		if err != nil {
			return "", err
		}
		if len(list) == 0 {
			return "No memories yet.", nil
		}
		var b strings.Builder
		for _, m := range list {
			fmt.Fprintf(&b, "#%d %s (%s)\n", m.ID, firstLine(m.Content), m.Source)
		}
		return tool.Clip(strings.TrimSpace(b.String())), nil
	case "memory_read":
		m, err := t.s.get(ws, in.ID)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Memory #%d (from %s, updated %s):\n%s", m.ID, m.Source, m.UpdatedAt.Format("2006-01-02"), m.Content), nil
	case "memory_save":
		m, err := t.s.save(SaveRequest{WorkspaceID: ws, ID: in.ID, Content: in.Content}, fmt.Sprintf("agent #%d", a.ID), a.ID)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Saved memory #%d. Every agent in the workspace sees it from its next turn.", m.ID), nil
	}
	return "", fmt.Errorf("unknown tool %s", call.Name)
}
