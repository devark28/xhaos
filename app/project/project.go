// Package project owns projects: folders on the server that the owner
// registers in a workspace. Every agent works in one project; its tools are
// confined to that folder. Agents in different projects coordinate through
// channels, not each other's files.
package project

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/devark28/xhaos/app/apperr"
	"github.com/devark28/xhaos/app/sqlitedb"
	"github.com/devark28/xhaos/app/tool"
	"github.com/devark28/xhaos/app/workspace"
)

type Project struct {
	ID          int64     `json:"id"`
	WorkspaceID string    `json:"workspaceId"`
	Name        string    `json:"name"`
	Path        string    `json:"path"`
	CreatedAt   time.Time `json:"createdAt"`
}

const schema = `
CREATE TABLE IF NOT EXISTS projects (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	workspace_id TEXT NOT NULL REFERENCES workspaces(id),
	name         TEXT NOT NULL,
	path         TEXT NOT NULL,
	created_at   TEXT NOT NULL,
	UNIQUE (workspace_id, path)
);`

// Migrate creates the table. Workspaces must exist.
func Migrate(db *sql.DB) error {
	_, err := db.Exec(schema)
	return err
}

// Ensure registers path as a project of the workspace unless it already is.
func Ensure(db *sql.DB, workspaceID, path string) error {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("%w: %s does not exist", apperr.Invalid, path)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM projects WHERE workspace_id = ? AND path = ?`, workspaceID, real).Scan(&n); err != nil || n > 0 {
		return err
	}
	_, err = add(db, workspaceID, real, "", time.Now())
	return err
}

const selectProject = `SELECT id, workspace_id, name, path, created_at FROM projects`

func scan(r interface{ Scan(...any) error }) (Project, error) {
	var p Project
	var at string
	err := r.Scan(&p.ID, &p.WorkspaceID, &p.Name, &p.Path, &at)
	p.CreatedAt = sqlitedb.ParseStamp(at)
	return p, err
}

// Get loads one project, or returns apperr.NotFound.
func Get(q sqlitedb.Querier, workspaceID string, id int64) (Project, error) {
	p, err := scan(q.QueryRow(selectProject+` WHERE workspace_id = ? AND id = ?`, workspaceID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return p, fmt.Errorf("%w: project %d in workspace %q", apperr.NotFound, id, workspaceID)
	}
	return p, err
}

// First returns the workspace's oldest project (0 when it has none).
func First(q sqlitedb.Querier, workspaceID string) (int64, error) {
	var id int64
	err := q.QueryRow(`SELECT id FROM projects WHERE workspace_id = ? ORDER BY id LIMIT 1`, workspaceID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

func add(q sqlitedb.Querier, workspaceID, path, name string, at time.Time) (Project, error) {
	path = strings.TrimSpace(path)
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	if !filepath.IsAbs(path) {
		return Project{}, fmt.Errorf("%w: give the folder's full path on the server", apperr.Invalid)
	}
	real, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return Project{}, fmt.Errorf("%w: %s does not exist on the server", apperr.Invalid, path)
	}
	if info, err := os.Stat(real); err != nil || !info.IsDir() {
		return Project{}, fmt.Errorf("%w: %s is not a folder", apperr.Invalid, real)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = filepath.Base(real)
	}
	res, err := q.Exec(`INSERT INTO projects (workspace_id, name, path, created_at) VALUES (?, ?, ?, ?)`, workspaceID, name, real, sqlitedb.Stamp(at))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return Project{}, fmt.Errorf("%w: %s is already a project", apperr.Conflict, real)
		}
		return Project{}, err
	}
	id, _ := res.LastInsertId()
	return Get(q, workspaceID, id)
}

// Service exposes projects and their files to clients.
type Service struct{ db *sql.DB }

func NewService(db *sql.DB) *Service { return &Service{db: db} }

// Projects lists a workspace's projects, oldest first.
func (s *Service) Projects(workspaceID string) ([]Project, error) {
	rows, err := s.db.Query(selectProject+` WHERE workspace_id = ? ORDER BY id`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Project{}
	for rows.Next() {
		p, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// AllPaths lists every project's folder, in every workspace.
func AllPaths(q sqlitedb.Querier) ([]string, error) {
	rows, err := q.Query(`SELECT DISTINCT path FROM projects`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

type AddRequest struct {
	WorkspaceID string `json:"workspaceId"`
	Path        string `json:"path"` // an existing folder on the server; ~/ is the server user's home
	Name        string `json:"name,omitempty"`
}

// Add registers a folder as a project.
func (s *Service) Add(req AddRequest) (Project, error) {
	if _, err := workspace.Get(s.db, req.WorkspaceID); err != nil {
		return Project{}, err
	}
	return add(s.db, req.WorkspaceID, req.Path, req.Name, time.Now())
}

type RemoveRequest struct {
	WorkspaceID string `json:"workspaceId"`
	ID          int64  `json:"id"`
}

// Remove unregisters a project (its folder is untouched). A project that
// still has agents, terminated or not, stays: their history refers to it.
func (s *Service) Remove(req RemoveRequest) error {
	if _, err := Get(s.db, req.WorkspaceID, req.ID); err != nil {
		return err
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM agents WHERE workspace_id = ? AND project_id = ?`, req.WorkspaceID, req.ID).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("%w: %d agent(s) belong to this project", apperr.Conflict, n)
	}
	_, err := s.db.Exec(`DELETE FROM projects WHERE workspace_id = ? AND id = ?`, req.WorkspaceID, req.ID)
	return err
}

type FilesRequest struct {
	WorkspaceID string `json:"workspaceId"`
	ProjectID   int64  `json:"projectId"`
	Dir         string `json:"dir,omitempty"` // relative to the project; empty is its root
}

type Entry struct {
	Name string `json:"name"`
	Path string `json:"path"` // relative to the project
	Dir  bool   `json:"dir,omitempty"`
	Size int64  `json:"size,omitempty"`
}

const maxEntries = 2000

// Files lists one directory of a project: folders first, then files, by
// name. .git is left out. Paths cannot leave the project, symlinks included.
func (s *Service) Files(req FilesRequest) ([]Entry, error) {
	p, err := Get(s.db, req.WorkspaceID, req.ProjectID)
	if err != nil {
		return nil, err
	}
	dir, err := tool.Resolve(p.Path, req.Dir)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", apperr.Invalid, err)
	}
	items, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", apperr.Invalid, err)
	}
	real, _ := filepath.EvalSymlinks(p.Path)
	out := []Entry{}
	for _, item := range items {
		if item.Name() == ".git" {
			continue
		}
		rel, _ := filepath.Rel(real, filepath.Join(dir, item.Name()))
		e := Entry{Name: item.Name(), Path: filepath.ToSlash(rel), Dir: item.IsDir()}
		if !e.Dir {
			if info, err := item.Info(); err == nil {
				e.Size = info.Size()
			}
		}
		out = append(out, e)
		if len(out) == maxEntries {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}
