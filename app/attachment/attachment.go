// Package attachment stores files the user attaches to messages. The bytes
// live as files in a directory beside the database (one per attachment,
// named by its ID); SQLite keeps only the metadata. Messages refer to
// attachments by ID, and the runner reads them when it builds a turn.
package attachment

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/devark28/xhaos/app/apperr"
	"github.com/devark28/xhaos/app/sqlitedb"
)

// MaxSize is the largest file accepted (larger images and PDFs exceed what
// providers take inline anyway).
const MaxSize = 20 << 20

// Ref is an attachment's metadata, as messages carry it.
type Ref struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
	Size int64  `json:"size"`
}

// File is an attachment with its bytes.
type File struct {
	Ref
	Data []byte `json:"data"`
}

const schema = `
CREATE TABLE IF NOT EXISTS attachments (
	id           TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES workspaces(id),
	name         TEXT NOT NULL,
	type         TEXT NOT NULL,
	size         INTEGER NOT NULL,
	created_at   TEXT NOT NULL
);`

func Migrate(db *sql.DB) error {
	_, err := db.Exec(schema)
	return err
}

// Store keeps attachments in dir.
type Store struct {
	db  *sql.DB
	dir string
}

func NewStore(db *sql.DB, dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{db: db, dir: dir}, nil
}

// Kind classifies a file for the model: "image", "pdf", "text", or "binary".
func Kind(f File) string {
	switch {
	case f.Type == "image/png" || f.Type == "image/jpeg" || f.Type == "image/gif" || f.Type == "image/webp":
		return "image"
	case f.Type == "application/pdf":
		return "pdf"
	case utf8.Valid(f.Data) && !strings.ContainsRune(string(f.Data), 0):
		return "text"
	}
	return "binary"
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Store) path(id string) string { return filepath.Join(s.dir, id) }

// Save writes a new attachment and returns its metadata.
func (s *Store) Save(workspaceID, name, typ string, data []byte) (Ref, error) {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "" || name == "." || name == "/" {
		name = "file"
	}
	if len(data) == 0 {
		return Ref{}, fmt.Errorf("%w: %s is empty", apperr.Invalid, name)
	}
	if len(data) > MaxSize {
		return Ref{}, fmt.Errorf("%w: %s is larger than %d MB", apperr.Invalid, name, MaxSize>>20)
	}
	if typ == "" {
		typ = mime.TypeByExtension(filepath.Ext(name))
	}
	if i := strings.IndexByte(typ, ';'); i >= 0 {
		typ = typ[:i]
	}
	if typ == "" {
		typ = "application/octet-stream"
	}
	ref := Ref{ID: newID(), Name: name, Type: typ, Size: int64(len(data))}
	// The file first: a row never points at missing bytes.
	tmp := s.path(ref.ID) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return Ref{}, err
	}
	if err := os.Rename(tmp, s.path(ref.ID)); err != nil {
		os.Remove(tmp)
		return Ref{}, err
	}
	if _, err := s.db.Exec(`INSERT INTO attachments (id, workspace_id, name, type, size, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		ref.ID, workspaceID, ref.Name, ref.Type, ref.Size, sqlitedb.Stamp(time.Now())); err != nil {
		os.Remove(s.path(ref.ID))
		return Ref{}, err
	}
	return ref, nil
}

// Refs looks up attachments by ID within a workspace, in the given order.
func (s *Store) Refs(q sqlitedb.Querier, workspaceID string, ids []string) ([]Ref, error) {
	out := make([]Ref, 0, len(ids))
	for _, id := range ids {
		var r Ref
		err := q.QueryRow(`SELECT id, name, type, size FROM attachments WHERE workspace_id = ? AND id = ?`, workspaceID, id).Scan(&r.ID, &r.Name, &r.Type, &r.Size)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: attachment %s", apperr.NotFound, id)
		}
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// Load reads an attachment's bytes.
func (s *Store) Load(workspaceID, id string) (File, error) {
	refs, err := s.Refs(s.db, workspaceID, []string{id})
	if err != nil {
		return File{}, err
	}
	data, err := os.ReadFile(s.path(id))
	if err != nil {
		return File{}, fmt.Errorf("attachment %s (%s): %w", refs[0].Name, id, err)
	}
	return File{Ref: refs[0], Data: data}, nil
}

// Service is the client-facing API.
type Service struct{ store *Store }

func NewService(store *Store) *Service { return &Service{store: store} }

type UploadRequest struct {
	WorkspaceID string `json:"workspaceId"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Data        []byte `json:"data"` // base64 in JSON
}

// Upload stores a file; pass the returned ID with a task or message.
func (s *Service) Upload(req UploadRequest) (Ref, error) {
	return s.store.Save(req.WorkspaceID, req.Name, req.Type, req.Data)
}

// Read returns an attachment with its bytes (for previews and downloads).
func (s *Service) Read(workspaceID, id string) (File, error) {
	return s.store.Load(workspaceID, id)
}
