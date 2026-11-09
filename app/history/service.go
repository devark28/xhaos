package history

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Service is the page's view of the history: the search box, the stats and
// the server-wide switch that lets agents search every project of the
// workspace (off by default: an agent searches its own project only).
type Service struct {
	db    *sql.DB
	store *Store
	emb   Embedder // nil when no embedding server is configured
}

// NewService returns the history service. emb is nil when -history-url is not
// set: search and import then say so.
func NewService(db *sql.DB, emb Embedder) *Service {
	return &Service{db: db, store: NewStore(db), emb: emb}
}

// Enabled reports whether an embedding server is configured (-history-url).
func (s *Service) Enabled() bool { return s.emb != nil }

// Health is the embedding server's state, for the settings' indicator.
type Health struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// Health checks the embedding server (GET {url}/health).
func (s *Service) Health() Health {
	if s.emb == nil {
		return Health{Detail: "no embedding server is configured: start Xhaos with -history-url"}
	}
	c, ok := s.emb.(*Client)
	if !ok {
		return Health{OK: true}
	}
	if err := c.Health(context.Background()); err != nil {
		return Health{Detail: err.Error()}
	}
	return Health{OK: true}
}

// AllProjects reports the server-wide switch: whether agents may search the
// whole workspace's history, not only their own project's.
func (s *Service) AllProjects() (bool, error) { return s.allProjects() }

// SetAllProjects turns that switch on or off.
func (s *Service) SetAllProjects(on bool) error {
	v := "off"
	if on {
		v = "on"
	}
	_, err := s.db.Exec(`INSERT INTO history_settings (key, value) VALUES ('all_projects', ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, v)
	return err
}

func (s *Service) allProjects() (bool, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM history_settings WHERE key = 'all_projects'`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return v == "on", err
}

// Stats is what the workspace's history holds.
type Stats struct {
	Chunks   int `json:"chunks"`
	Scrubbed int `json:"scrubbed"`
}

// Stats counts the workspace's chunks, and how many of them had a secret
// scrubbed.
func (s *Service) Stats(workspaceID string) (Stats, error) {
	c, sc, err := s.store.Count(workspaceID)
	return Stats{Chunks: c, Scrubbed: sc}, err
}

// SearchRequest is the owner's search: the workspace, the query, and
// optionally one project (0: every project of the workspace).
type SearchRequest struct {
	WorkspaceID string `json:"workspaceId"`
	Query       string `json:"query"`
	ProjectID   int64  `json:"projectId,omitempty"`
	Limit       int    `json:"limit,omitempty"`
}

// Result is a search hit with its project's name.
type Result struct {
	Hit
	ProjectName string `json:"projectName"`
	Excerpt     string `json:"excerpt"`
}

// Search answers the owner's search (the page's search box).
func (s *Service) Search(req SearchRequest) ([]Result, error) {
	var projects []int64
	if req.ProjectID != 0 {
		projects = []int64{req.ProjectID}
	}
	return s.search(context.Background(), req.WorkspaceID, req.Query, projects, req.Limit)
}

// minScore is the cosine similarity below which a passage is not returned.
// Measured with bge-small-en-v1.5 on a six-passage fixture: on-topic queries
// scored 0.74 to 0.84 for their best passage, unrelated ones 0.41 to 0.54.
// Provisional: retune on real transcripts (the owner's data is not in the repo).
// A variable only so the tests can switch it off for the hashing fake embedder.
var minScore float32 = 0.6

// search embeds the query and returns the nearest chunks, best first.
func (s *Service) search(ctx context.Context, ws, query string, projects []int64, limit int) ([]Result, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("the search needs a query")
	}
	if s.emb == nil {
		return nil, errors.New("history search needs the embedding server (-history-url)")
	}
	vecs, err := s.emb.Embed(ctx, []string{queryPrefix + query})
	if err != nil {
		return nil, err
	}
	hits, err := s.store.Search(ctx, ws, projects, vecs[0], limit)
	if err != nil {
		return nil, err
	}
	names, err := s.projectNames(ws)
	if err != nil {
		return nil, err
	}
	out := make([]Result, 0, len(hits))
	for _, h := range hits {
		if h.Score < minScore {
			continue // not close enough to the query to be a match
		}
		out = append(out, Result{Hit: h, ProjectName: names[h.ProjectID], Excerpt: Excerpt(h.Text, 300)})
	}
	return out, nil
}

// projectNames maps the workspace's project IDs to their names.
func (s *Service) projectNames(ws string) (map[int64]string, error) {
	rows, err := s.db.Query(`SELECT id, name FROM projects WHERE workspace_id = ?`, ws)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}

// projectRefs lists the workspace's projects for the importer.
func (s *Service) projectRefs(ws string) ([]ProjectRef, error) {
	rows, err := s.db.Query(`SELECT id, name, path FROM projects WHERE workspace_id = ?`, ws)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProjectRef
	for rows.Next() {
		var p ProjectRef
		if err := rows.Scan(&p.ID, &p.Name, &p.Path); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// projectID resolves a project's name in the workspace.
func (s *Service) projectID(ws, name string) (int64, error) {
	var id int64
	err := s.db.QueryRow(`SELECT id FROM projects WHERE workspace_id = ? AND name = ?`, ws, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("no project named %q in this workspace", name)
	}
	return id, err
}

// Plan reads the transcript folders under root and proposes the project of
// each one. Nothing is imported: the owner confirms the proposals first.
func Plan(s *Service, ws, root string) (proposals []Proposal, unmatched []string, err error) {
	folders, err := Folders(root)
	if err != nil {
		return nil, nil, err
	}
	projects, err := s.projectRefs(ws)
	if err != nil {
		return nil, nil, err
	}
	proposals = Propose(folders, projects)
	mapped := map[string]bool{}
	for _, p := range proposals {
		mapped[p.Folder] = true
	}
	for _, f := range folders {
		if !mapped[f] {
			unmatched = append(unmatched, f)
		}
	}
	return proposals, unmatched, nil
}

// Import imports the confirmed proposals (see Plan) into the workspace.
func Import(ctx context.Context, s *Service, ws, root string, confirmed []Proposal) ([]Report, error) {
	if s.emb == nil {
		return nil, errors.New("import needs the embedding server (-history-url)")
	}
	var out []Report
	for _, p := range confirmed {
		rep, err := ImportFolder(ctx, s.store, s.emb, ws, p.Project, root, p.Folder)
		if err != nil {
			return out, fmt.Errorf("%s: %w", p.Folder, err)
		}
		out = append(out, rep)
	}
	return out, nil
}

// timeout bounds one agent search.
const timeout = 60 * time.Second
