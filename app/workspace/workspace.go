// Package workspace owns workspaces: the top-level scope for agents, events,
// memory, and policy.
package workspace

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/devark28/xhaos/app/apperr"
	"github.com/devark28/xhaos/app/sqlitedb"
)

// DefaultID is created on first start so the UI has somewhere to begin.
const DefaultID = "default"

type Workspace struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
}

const schema = `
CREATE TABLE IF NOT EXISTS workspaces (
	id         TEXT PRIMARY KEY,
	name       TEXT NOT NULL,
	created_at TEXT NOT NULL
);`

// Migrate creates the table and seeds the default workspace.
func Migrate(db *sql.DB) error {
	if _, err := db.Exec(schema); err != nil {
		return err
	}
	_, err := db.Exec(`INSERT OR IGNORE INTO workspaces (id, name, created_at) VALUES (?, ?, ?)`,
		DefaultID, "Default", sqlitedb.Stamp(time.Now()))
	return err
}

// Get loads one workspace, or returns apperr.NotFound.
func Get(q sqlitedb.Querier, id string) (Workspace, error) {
	var w Workspace
	var at string
	err := q.QueryRow(`SELECT id, name, created_at FROM workspaces WHERE id = ?`, id).Scan(&w.ID, &w.Name, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return w, fmt.Errorf("%w: workspace %q", apperr.NotFound, id)
	}
	w.CreatedAt = sqlitedb.ParseStamp(at)
	return w, err
}

// Service exposes workspaces to clients.
type Service struct{ db *sql.DB }

func NewService(db *sql.DB) *Service { return &Service{db: db} }

// Workspaces lists all workspaces.
func (s *Service) Workspaces() ([]Workspace, error) {
	rows, err := s.db.Query(`SELECT id, name, created_at FROM workspaces ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Workspace{}
	for rows.Next() {
		var w Workspace
		var at string
		if err := rows.Scan(&w.ID, &w.Name, &at); err != nil {
			return nil, err
		}
		w.CreatedAt = sqlitedb.ParseStamp(at)
		out = append(out, w)
	}
	return out, rows.Err()
}

// Create adds a workspace (package auth gives it its passphrase).
func Create(db *sql.DB, id, name string) (Workspace, error) {
	at := time.Now()
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, created_at) VALUES (?, ?, ?)`, id, name, sqlitedb.Stamp(at)); err != nil {
		return Workspace{}, err
	}
	return Workspace{ID: id, Name: name, CreatedAt: at}, nil
}

// Rename changes a workspace's name.
func Rename(db *sql.DB, id, name string) error {
	_, err := db.Exec(`UPDATE workspaces SET name = ? WHERE id = ?`, name, id)
	return err
}
