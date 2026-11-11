// Package mcpserver is Xhaos as an MCP server: an MCP client (Claude Code,
// say) reaches /xhaos/mcp with an access token and lists, creates and
// drives agents, reads and answers their questions, and talks in channels,
// on the owner's behalf and within the token's scopes.
//
// Tokens are separate from the passphrase and from channel links. The owner
// creates them in Settings (with the passphrase), each with a name and
// scopes; the token is shown once and only its SHA-256 hash is kept (it is
// 192 random bits: nothing to guess, nothing to salt). Revoking deletes it.
// A token reaches only its own workspace, and only while that workspace is
// unlocked: after a restart its tools answer "locked" until the owner signs
// in on the page.
//
// What an MCP client does is recorded as the owner's action with the
// token's name beside it (Via), so the page shows what came from where.
package mcpserver

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/devark28/xhaos/app/apperr"
	"github.com/devark28/xhaos/app/sqlitedb"
)

// Scopes a token may hold. Each tool needs one (see tools.go).
const (
	// ScopeRead lists agents, reads their messages, questions, channels and
	// status, and waits for events.
	ScopeRead = "read"
	// ScopeDrive creates agents, sends them messages and tasks, starts and
	// stops them, and opens, reads and posts to channels.
	ScopeDrive = "drive"
	// ScopeAnswer answers agents' questions (ask_user) for the owner.
	ScopeAnswer = "answer"
	// ScopeApprove allows or denies an agent's git commit or push, only when
	// it stays on non-protected branches (never main, integration,
	// release… or tags), and only as a plain git command.
	ScopeApprove = "approve-feature-branch"
)

// Scopes lists every scope, in the order the page shows them.
var Scopes = []string{ScopeRead, ScopeDrive, ScopeAnswer, ScopeApprove}

const schema = `
CREATE TABLE IF NOT EXISTS mcp_tokens (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	workspace_id TEXT NOT NULL REFERENCES workspaces(id),
	name         TEXT NOT NULL,
	hash         TEXT NOT NULL UNIQUE,
	scopes       TEXT NOT NULL, -- space-separated
	created_at   TEXT NOT NULL,
	last_used_at TEXT NOT NULL DEFAULT '',
	UNIQUE (workspace_id, name)
);`

// Migrate creates the tokens table.
func Migrate(db *sql.DB) error { _, err := db.Exec(schema); return err }

// TokenPrefix starts every token, so one found in a log or a config file
// is recognisable.
const TokenPrefix = "xhm_"

// Path is where the endpoint takes requests; Alias is the shorter address
// the docs give (the same handler).
const (
	Path  = "/xhaos/mcp"
	Alias = "/mcp"
)

// Token is a token as the page lists it: never its secret.
type Token struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Scopes     []string   `json:"scopes"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
}

// Service is the page's side: list, create and revoke tokens. It is bound
// to the page; the endpoint itself is Server.
type Service struct {
	db *sql.DB
	// Verify checks the workspace's passphrase (creating a token asks for
	// it); nil skips the check (tests without passphrases).
	Verify func(workspaceID, pass string) error
	now    func() time.Time

	mu   sync.Mutex
	used map[int64]time.Time // last stored last_used_at, to write it at most once a minute
}

func NewService(db *sql.DB) *Service {
	return &Service{db: db, now: time.Now, used: map[int64]time.Time{}}
}

// Tokens lists the workspace's tokens, newest first.
func (s *Service) Tokens(workspaceID string) ([]Token, error) {
	rows, err := s.db.Query(`SELECT id, name, scopes, created_at, last_used_at FROM mcp_tokens WHERE workspace_id = ? ORDER BY id DESC`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Token{}
	for rows.Next() {
		var t Token
		var scopes, created, used string
		if err := rows.Scan(&t.ID, &t.Name, &scopes, &created, &used); err != nil {
			return nil, err
		}
		t.Scopes = strings.Fields(scopes)
		t.CreatedAt = sqlitedb.ParseStamp(created)
		if used != "" {
			u := sqlitedb.ParseStamp(used)
			t.LastUsedAt = &u
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// CreateRequest makes a token. The passphrase is asked again: a token can
// act for the owner from anywhere the server is reachable.
type CreateRequest struct {
	WorkspaceID string   `json:"workspaceId"`
	Name        string   `json:"name"`
	Scopes      []string `json:"scopes"`
	Passphrase  string   `json:"passphrase"`
}

// Issued is a new token, its secret shown this once.
type Issued struct {
	Token  Token  `json:"token"`
	Secret string `json:"secret"`
	Path   string `json:"path"`
}

// Create makes a token with a name (shown beside everything it does) and
// scopes; the secret is returned this once.
func (s *Service) Create(req CreateRequest) (Issued, error) {
	name := strings.Join(strings.Fields(req.Name), " ")
	if name == "" || len([]rune(name)) > 40 {
		return Issued{}, fmt.Errorf("%w: a token needs a name (up to 40 characters), shown beside what it does", apperr.Invalid)
	}
	scopes, err := cleanScopes(req.Scopes)
	if err != nil {
		return Issued{}, err
	}
	if s.Verify != nil {
		if err := s.Verify(req.WorkspaceID, req.Passphrase); err != nil {
			return Issued{}, err
		}
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return Issued{}, err
	}
	secret := TokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	at := s.now()
	res, err := s.db.Exec(`INSERT INTO mcp_tokens (workspace_id, name, hash, scopes, created_at) VALUES (?, ?, ?, ?, ?)`,
		req.WorkspaceID, name, hashToken(secret), strings.Join(scopes, " "), sqlitedb.Stamp(at))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return Issued{}, fmt.Errorf("%w: there is already a token named %q", apperr.Conflict, name)
		}
		return Issued{}, err
	}
	id, _ := res.LastInsertId()
	return Issued{Token: Token{ID: id, Name: name, Scopes: scopes, CreatedAt: at}, Secret: secret, Path: Alias}, nil
}

// RevokeRequest revokes a token.
type RevokeRequest struct {
	WorkspaceID string `json:"workspaceId"`
	ID          int64  `json:"id"`
}

// Revoke deletes a token: requests with it fail from now on.
func (s *Service) Revoke(req RevokeRequest) error {
	res, err := s.db.Exec(`DELETE FROM mcp_tokens WHERE workspace_id = ? AND id = ?`, req.WorkspaceID, req.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: token %d", apperr.NotFound, req.ID)
	}
	return nil
}

func cleanScopes(in []string) ([]string, error) {
	var out []string
	for _, sc := range in {
		sc = strings.TrimSpace(sc)
		if sc == "" || slices.Contains(out, sc) {
			continue
		}
		if !slices.Contains(Scopes, sc) {
			return nil, fmt.Errorf("%w: unknown scope %q (scopes: %s)", apperr.Invalid, sc, strings.Join(Scopes, ", "))
		}
		out = append(out, sc)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: choose at least one scope", apperr.Invalid)
	}
	slices.SortFunc(out, func(a, b string) int { return slices.Index(Scopes, a) - slices.Index(Scopes, b) })
	return out, nil
}

func hashToken(t string) string { s := sha256.Sum256([]byte(t)); return hex.EncodeToString(s[:]) }

// identity is who a request is: one token.
type identity struct {
	TokenID   int64
	Workspace string
	Name      string
	Scopes    []string
}

func (id identity) has(scope string) bool { return slices.Contains(id.Scopes, scope) }

var errToken = errors.New("missing, wrong or revoked MCP token")

// lookup finds the token secret belongs to, and notes its use.
func (s *Service) lookup(secret string) (identity, error) {
	if !strings.HasPrefix(secret, TokenPrefix) {
		return identity{}, errToken
	}
	var id identity
	var scopes string
	err := s.db.QueryRow(`SELECT id, workspace_id, name, scopes FROM mcp_tokens WHERE hash = ?`, hashToken(secret)).Scan(&id.TokenID, &id.Workspace, &id.Name, &scopes)
	if err != nil {
		return identity{}, errToken
	}
	id.Scopes = strings.Fields(scopes)
	s.touch(id.TokenID)
	return id, nil
}

// live reports whether a token still exists (not revoked).
func (s *Service) live(id int64) bool {
	var n int
	return s.db.QueryRow(`SELECT COUNT(*) FROM mcp_tokens WHERE id = ?`, id).Scan(&n) == nil && n > 0
}

// touch records a token's use, at most once a minute.
func (s *Service) touch(id int64) {
	now := s.now()
	s.mu.Lock()
	if now.Sub(s.used[id]) < time.Minute {
		s.mu.Unlock()
		return
	}
	s.used[id] = now
	s.mu.Unlock()
	_, _ = s.db.Exec(`UPDATE mcp_tokens SET last_used_at = ? WHERE id = ?`, sqlitedb.Stamp(now), id)
}
