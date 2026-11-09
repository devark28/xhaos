package history

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"
)

// Migrate creates the chunk table and the settings (server-wide key/value
// pairs).
func Migrate(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS history_chunks (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		workspace_id TEXT    NOT NULL,
		project_id   INTEGER NOT NULL,
		session      TEXT    NOT NULL,
		at           TEXT    NOT NULL,
		turn         INTEGER NOT NULL,
		part         INTEGER NOT NULL,
		role         TEXT    NOT NULL,
		text         TEXT    NOT NULL,
		scrubbed     INTEGER NOT NULL DEFAULT 0,
		dedupe       TEXT    NOT NULL UNIQUE,
		vec          BLOB    NOT NULL
	);
	CREATE INDEX IF NOT EXISTS history_chunks_project ON history_chunks (workspace_id, project_id);
	CREATE TABLE IF NOT EXISTS history_settings (
		key   TEXT NOT NULL PRIMARY KEY,
		value TEXT NOT NULL
	);`)
	return err
}

// Store keeps the chunks and answers searches. The vectors of a workspace are
// loaded once into memory and reloaded after an import.
type Store struct {
	db *sql.DB

	mu    sync.Mutex
	index map[string]*index // by workspace; dropped on every insert
}

// NewStore wraps db (see Migrate).
func NewStore(db *sql.DB) *Store { return &Store{db: db, index: map[string]*index{}} }

// Stored is one chunk to store, with its vector and whether it was scrubbed.
type Stored struct {
	Chunk    Chunk
	Vec      []float32
	Scrubbed bool
}

// Add stores the chunks of one project and returns how many were new. The same
// chunk added twice is stored once (its key is the workspace, project,
// session, turn, part and text).
func (s *Store) Add(ctx context.Context, workspaceID string, projectID int64, items []Stored) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO history_chunks
		(workspace_id, project_id, session, at, turn, part, role, text, scrubbed, dedupe, vec)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	added := 0
	for _, it := range items {
		c := it.Chunk
		key := dedupeKey(workspaceID, projectID, c)
		res, err := stmt.ExecContext(ctx, workspaceID, projectID, c.Session, c.Time.UTC().Format(time.RFC3339Nano),
			c.Turn, c.Part, c.Role, c.Text, boolInt(it.Scrubbed), key, encodeVec(it.Vec))
		if err != nil {
			return added, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			added++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	if added > 0 {
		s.mu.Lock()
		delete(s.index, workspaceID)
		s.mu.Unlock()
	}
	return added, nil
}

func dedupeKey(ws string, project int64, c Chunk) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%d\x00%s\x00%d\x00%d\x00%s", ws, project, c.Session, c.Turn, c.Part, c.Text)
	return hex.EncodeToString(h.Sum(nil))
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Count returns how many chunks the workspace holds, and how many of them
// had a secret scrubbed.
func (s *Store) Count(workspaceID string) (chunks, scrubbed int, err error) {
	err = s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(scrubbed > 0), 0) FROM history_chunks WHERE workspace_id = ?`, workspaceID).Scan(&chunks, &scrubbed)
	return chunks, scrubbed, err
}

// Hit is one search result.
type Hit struct {
	ID        int64     `json:"id"`
	ProjectID int64     `json:"projectId"`
	Session   string    `json:"session"`
	At        time.Time `json:"at"`
	Turn      int       `json:"turn"`
	Part      int       `json:"part"`
	Role      string    `json:"role"`
	Text      string    `json:"text"`
	Score     float32   `json:"score"`
}

type index struct {
	ids      []int64
	projects []int64
	dim      int
	vecs     []float32 // row i is vecs[i*dim : (i+1)*dim]
}

// load reads the workspace's vectors (not the texts) into memory.
func (s *Store) load(ctx context.Context, ws string) (*index, error) {
	s.mu.Lock()
	if ix, ok := s.index[ws]; ok {
		s.mu.Unlock()
		return ix, nil
	}
	s.mu.Unlock()
	rows, err := s.db.QueryContext(ctx, `SELECT id, project_id, vec FROM history_chunks WHERE workspace_id = ? ORDER BY id`, ws)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ix := &index{}
	for rows.Next() {
		var id, project int64
		var blob []byte
		if err := rows.Scan(&id, &project, &blob); err != nil {
			return nil, err
		}
		v := decodeVec(blob)
		if ix.dim == 0 {
			ix.dim = len(v)
		}
		if len(v) != ix.dim {
			return nil, fmt.Errorf("history: chunk #%d has a %d-value vector, want %d", id, len(v), ix.dim)
		}
		ix.ids = append(ix.ids, id)
		ix.projects = append(ix.projects, project)
		ix.vecs = append(ix.vecs, v...)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.index[ws] = ix
	s.mu.Unlock()
	return ix, nil
}

// Search returns the limit chunks of the workspace nearest to q (unit length),
// best first. projects limits the search to those project IDs (nil: all).
func (s *Store) Search(ctx context.Context, ws string, projects []int64, q []float32, limit int) ([]Hit, error) {
	if limit <= 0 {
		limit = 5
	}
	ix, err := s.load(ctx, ws)
	if err != nil {
		return nil, err
	}
	if len(ix.ids) == 0 {
		return nil, nil
	}
	if len(q) != ix.dim {
		return nil, fmt.Errorf("history: query has %d values, the index %d", len(q), ix.dim)
	}
	allowed := map[int64]bool{}
	for _, p := range projects {
		allowed[p] = true
	}
	type scored struct {
		row   int
		score float32
	}
	var best []scored // the limit best so far, highest first
	for i := range ix.ids {
		if projects != nil && !allowed[ix.projects[i]] {
			continue
		}
		v := ix.vecs[i*ix.dim : (i+1)*ix.dim]
		var dot float32
		for j, x := range v {
			dot += x * q[j]
		}
		if len(best) == limit && dot <= best[limit-1].score {
			continue
		}
		best = append(best, scored{i, dot})
		sort.SliceStable(best, func(a, b int) bool { return best[a].score > best[b].score })
		if len(best) > limit {
			best = best[:limit]
		}
	}
	hits := make([]Hit, 0, len(best))
	for _, b := range best {
		h, err := s.hit(ctx, ix.ids[b.row])
		if err != nil {
			return nil, err
		}
		h.Score = b.score
		hits = append(hits, h)
	}
	return hits, nil
}

func (s *Store) hit(ctx context.Context, id int64) (Hit, error) {
	var h Hit
	var at string
	err := s.db.QueryRowContext(ctx, `SELECT id, project_id, session, at, turn, part, role, text FROM history_chunks WHERE id = ?`, id).
		Scan(&h.ID, &h.ProjectID, &h.Session, &at, &h.Turn, &h.Part, &h.Role, &h.Text)
	if errors.Is(err, sql.ErrNoRows) {
		return h, fmt.Errorf("history: chunk #%d is gone", id)
	}
	if err != nil {
		return h, err
	}
	h.At, _ = time.Parse(time.RFC3339Nano, at)
	return h, nil
}

func encodeVec(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(x))
	}
	return b
}

func decodeVec(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v
}

// Existing reports which of the dedupe keys are already stored.
func (s *Store) existing(ctx context.Context, keys []string) (map[string]bool, error) {
	out := make(map[string]bool, len(keys))
	stmt, err := s.db.PrepareContext(ctx, `SELECT 1 FROM history_chunks WHERE dedupe = ?`)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()
	for _, k := range keys {
		var one int
		err := stmt.QueryRowContext(ctx, k).Scan(&one)
		if err == nil {
			out[k] = true
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	return out, nil
}
