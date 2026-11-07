// Package board keeps a workspace's Excalidraw boards for the Draw tab: named
// scenes that outlive the browser. A board's elements are stored one row each
// and merged by element ID and version (elements.go), so a save from another
// tab or browser never overwrites a newer element. Images are stored once
// each, as files named by their content hash, and a board refers to them by
// the ID the scene uses (images.go).
//
// Boards are not part of an agent's snapshot: the page loads one when the
// Draw tab opens. Changes reach other open pages as events on the
// workspace's event stream, which carry the changed elements and no images.
package board

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/devark28/xhaos/app/apperr"
	"github.com/devark28/xhaos/app/eventlog"
	"github.com/devark28/xhaos/app/sqlitedb"
)

const (
	EventCreated = "board.created"
	EventRenamed = "board.renamed"
	EventDeleted = "board.deleted"
	EventSaved   = "board.saved" // changed elements; the page applies them with reconcileElements

	maxName = 200
)

// Summary is a board without its scene, as the picker lists it.
type Summary struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

const schema = `
CREATE TABLE IF NOT EXISTS boards (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	workspace_id TEXT NOT NULL REFERENCES workspaces(id),
	name         TEXT NOT NULL,
	app_state    TEXT NOT NULL DEFAULT '{}',
	created_at   TEXT NOT NULL,
	updated_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS boards_by_workspace ON boards (workspace_id, id);
CREATE TABLE IF NOT EXISTS board_elements (
	board_id      INTEGER NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
	element_id    TEXT NOT NULL,
	version       INTEGER NOT NULL,
	version_nonce INTEGER NOT NULL,
	data          TEXT NOT NULL,
	PRIMARY KEY (board_id, element_id)
);
CREATE TABLE IF NOT EXISTS board_images (
	board_id  INTEGER NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
	file_id   TEXT NOT NULL,
	hash      TEXT NOT NULL,
	mime_type TEXT NOT NULL,
	created   INTEGER NOT NULL,
	PRIMARY KEY (board_id, file_id)
);
CREATE INDEX IF NOT EXISTS board_images_by_hash ON board_images (hash);`

// Migrate creates the board tables. Run it after the workspace migration.
func Migrate(db *sql.DB) error {
	for _, stmt := range strings.Split(schema, ";\n") {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

// Service is the client-facing API. dir holds the image files.
type Service struct {
	db      *sql.DB
	publish eventlog.Publisher
	dir     string
	now     func() time.Time
	mu      sync.Mutex // serializes image files with their cleanup
}

func NewService(db *sql.DB, publish eventlog.Publisher, dir string) *Service {
	if publish == nil {
		publish = func(eventlog.Event) {}
	}
	return &Service{db: db, publish: publish, dir: dir, now: time.Now}
}

// checkWorkspace refuses IDs that could name a path. Every method calls it
// before touching a workspace's files.
func checkWorkspace(ws string) error {
	if ws == "" || strings.ContainsAny(ws, `/\`) || strings.HasPrefix(ws, ".") {
		return fmt.Errorf("%w: workspace %q", apperr.Invalid, ws)
	}
	return nil
}

func cleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if len(name) > maxName {
		return "", fmt.Errorf("%w: a board name is at most %d characters", apperr.Invalid, maxName)
	}
	return name, nil
}

// find returns a board of the workspace, or NotFound.
func find(q sqlitedb.Querier, ws string, id int64) (Summary, error) {
	var m Summary
	var c, u string
	err := q.QueryRow(`SELECT id, name, created_at, updated_at FROM boards WHERE workspace_id = ? AND id = ?`, ws, id).Scan(&m.ID, &m.Name, &c, &u)
	if errors.Is(err, sql.ErrNoRows) {
		return Summary{}, fmt.Errorf("%w: board #%d", apperr.NotFound, id)
	}
	if err != nil {
		return Summary{}, err
	}
	m.CreatedAt, m.UpdatedAt = sqlitedb.ParseStamp(c), sqlitedb.ParseStamp(u)
	return m, nil
}

// List returns a workspace's boards, oldest first (the picker's order).
func (s *Service) List(workspaceID string) ([]Summary, error) {
	if err := checkWorkspace(workspaceID); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT id, name, created_at, updated_at FROM boards WHERE workspace_id = ? ORDER BY id`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Summary{}
	for rows.Next() {
		var m Summary
		var c, u string
		if err := rows.Scan(&m.ID, &m.Name, &c, &u); err != nil {
			return nil, err
		}
		m.CreatedAt, m.UpdatedAt = sqlitedb.ParseStamp(c), sqlitedb.ParseStamp(u)
		out = append(out, m)
	}
	return out, rows.Err()
}

type CreateRequest struct {
	WorkspaceID string `json:"workspaceId"`
	Name        string `json:"name"` // empty: "Board N"
}

// Create adds an empty board.
func (s *Service) Create(req CreateRequest) (Summary, error) {
	if err := checkWorkspace(req.WorkspaceID); err != nil {
		return Summary{}, err
	}
	name, err := cleanName(req.Name)
	if err != nil {
		return Summary{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Summary{}, err
	}
	defer tx.Rollback()
	if name == "" {
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM boards WHERE workspace_id = ?`, req.WorkspaceID).Scan(&n); err != nil {
			return Summary{}, err
		}
		name = fmt.Sprintf("Board %d", n+1)
	}
	at := sqlitedb.Stamp(s.now())
	res, err := tx.Exec(`INSERT INTO boards (workspace_id, name, app_state, created_at, updated_at) VALUES (?, ?, '{}', ?, ?)`, req.WorkspaceID, name, at, at)
	if err != nil {
		return Summary{}, err
	}
	id, _ := res.LastInsertId()
	m, err := find(tx, req.WorkspaceID, id)
	if err != nil {
		return Summary{}, err
	}
	ev, err := eventlog.Append(tx, req.WorkspaceID, 0, EventCreated, m, s.now())
	if err != nil {
		return Summary{}, err
	}
	if err := tx.Commit(); err != nil {
		return Summary{}, err
	}
	s.publish(ev)
	return m, nil
}

type RenameRequest struct {
	WorkspaceID string `json:"workspaceId"`
	ID          int64  `json:"id"`
	Name        string `json:"name"`
}

// Rename changes a board's name.
func (s *Service) Rename(req RenameRequest) (Summary, error) {
	if err := checkWorkspace(req.WorkspaceID); err != nil {
		return Summary{}, err
	}
	name, err := cleanName(req.Name)
	if err != nil {
		return Summary{}, err
	}
	if name == "" {
		return Summary{}, fmt.Errorf("%w: a board needs a name", apperr.Invalid)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Summary{}, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE boards SET name = ?, updated_at = ? WHERE workspace_id = ? AND id = ?`, name, sqlitedb.Stamp(s.now()), req.WorkspaceID, req.ID)
	if err != nil {
		return Summary{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Summary{}, fmt.Errorf("%w: board #%d", apperr.NotFound, req.ID)
	}
	m, err := find(tx, req.WorkspaceID, req.ID)
	if err != nil {
		return Summary{}, err
	}
	ev, err := eventlog.Append(tx, req.WorkspaceID, 0, EventRenamed, m, s.now())
	if err != nil {
		return Summary{}, err
	}
	if err := tx.Commit(); err != nil {
		return Summary{}, err
	}
	s.publish(ev)
	return m, nil
}

// Delete removes a board and the image files no other board of the workspace
// still uses.
func (s *Service) Delete(workspaceID string, id int64) error {
	if err := checkWorkspace(workspaceID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := find(tx, workspaceID, id); err != nil {
		return err
	}
	rows, err := tx.Query(`SELECT DISTINCT hash FROM board_images WHERE board_id = ?`, id)
	if err != nil {
		return err
	}
	var hashes []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			rows.Close()
			return err
		}
		hashes = append(hashes, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM board_images WHERE board_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM board_elements WHERE board_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM boards WHERE workspace_id = ? AND id = ?`, workspaceID, id); err != nil {
		return err
	}
	ev, err := eventlog.Append(tx, workspaceID, 0, EventDeleted, map[string]int64{"id": id}, s.now())
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.publish(ev)
	s.removeUnusedImages(workspaceID, hashes)
	return nil
}
