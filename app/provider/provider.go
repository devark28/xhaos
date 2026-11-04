// Package provider stores model provider configs (kind, endpoint, models, and
// an API key encrypted at rest). Keys are entered from any client and never
// sent back: clients see only whether a key is set and its last characters.
package provider

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"xhaos/app/apperr"
	"xhaos/app/llm"
	"xhaos/app/secretbox"
	"xhaos/app/sqlitedb"
)

type Provider struct {
	ID      string   `json:"id"`
	Kind    string   `json:"kind"`
	Name    string   `json:"name"`
	BaseURL string   `json:"baseUrl,omitempty"`
	Models  []string `json:"models"`
	HasKey  bool     `json:"hasKey"`
	KeyHint string   `json:"keyHint,omitempty"` // e.g. "…a1b2"
	// ContextTokens is the context window agents may fill before older
	// messages are compacted; 0 means the kind's default (see Budget).
	ContextTokens int64     `json:"contextTokens,omitempty"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type SaveRequest struct {
	ID      string   `json:"id"`
	Kind    string   `json:"kind"`
	Name    string   `json:"name,omitempty"`
	BaseURL string   `json:"baseUrl,omitempty"`
	APIKey  string   `json:"apiKey,omitempty"` // empty keeps the stored key
	Models  []string `json:"models,omitempty"` // empty: the adapter's defaults
	// ContextTokens overrides the kind's default context budget (0 keeps it).
	ContextTokens int64 `json:"contextTokens,omitempty"`
}

type TestResult struct {
	OK      bool     `json:"ok"`
	Message string   `json:"message"`
	Models  []string `json:"models,omitempty"`
}

const schema = `
CREATE TABLE IF NOT EXISTS providers (
	id         TEXT PRIMARY KEY,
	kind       TEXT NOT NULL,
	name       TEXT NOT NULL,
	base_url   TEXT NOT NULL DEFAULT '',
	models     TEXT NOT NULL,
	key_sealed TEXT NOT NULL DEFAULT '',
	key_hint   TEXT NOT NULL DEFAULT '',
	updated_at TEXT NOT NULL,
	context_tokens INTEGER NOT NULL DEFAULT 0
);`

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// Store is the server-side view, including decrypted credentials. It is not
// bound to clients.
type Store struct {
	db       *sql.DB
	box      *secretbox.Box
	adapters map[string]llm.Adapter
	defaults map[string][]string
}

// NewStore migrates the providers table. adapters are keyed by kind; defaults
// are the model lists used when a config names none.
func NewStore(db *sql.DB, box *secretbox.Box, adapters []llm.Adapter, defaults map[string][]string) (*Store, error) {
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("migrate providers: %w", err)
	}
	// Added after the first release: older tables lack the column.
	if _, err := db.Exec(`ALTER TABLE providers ADD COLUMN context_tokens INTEGER NOT NULL DEFAULT 0`); err != nil && !strings.Contains(err.Error(), "duplicate column") {
		return nil, fmt.Errorf("migrate providers: %w", err)
	}
	byKind := map[string]llm.Adapter{}
	for _, a := range adapters {
		byKind[a.Kind()] = a
	}
	return &Store{db: db, box: box, adapters: byKind, defaults: defaults}, nil
}

// keyOptional reports whether a kind's adapter can work without an API key
// (local OpenAI-compatible servers usually take none).
func (st *Store) keyOptional(kind string) bool {
	k, ok := st.adapters[kind].(interface{ KeyOptional() bool })
	return ok && k.KeyOptional()
}

// Adapter returns the adapter for kind, or nil.
func (st *Store) Adapter(kind string) llm.Adapter { return st.adapters[kind] }

const selectProvider = `SELECT id, kind, name, base_url, models, key_sealed, key_hint, updated_at, context_tokens FROM providers`

func scan(r interface{ Scan(...any) error }) (Provider, string, error) {
	var p Provider
	var models, sealed, at string
	if err := r.Scan(&p.ID, &p.Kind, &p.Name, &p.BaseURL, &models, &sealed, &p.KeyHint, &at, &p.ContextTokens); err != nil {
		return p, "", err
	}
	_ = json.Unmarshal([]byte(models), &p.Models)
	p.HasKey, p.UpdatedAt = sealed != "", sqlitedb.ParseStamp(at)
	return p, sealed, nil
}

// Credentials returns the provider and its decrypted credentials.
func (st *Store) Credentials(id string) (Provider, llm.Credentials, error) {
	p, sealed, err := scan(st.db.QueryRow(selectProvider+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return p, llm.Credentials{}, fmt.Errorf("%w: provider %q", apperr.NotFound, id)
	}
	if err != nil {
		return p, llm.Credentials{}, err
	}
	if sealed == "" {
		if st.keyOptional(p.Kind) {
			return p, llm.Credentials{BaseURL: p.BaseURL}, nil
		}
		return p, llm.Credentials{}, fmt.Errorf("%w: provider %q has no API key", apperr.Invalid, id)
	}
	key, err := st.box.Unseal(sealed)
	return p, llm.Credentials{APIKey: key, BaseURL: p.BaseURL}, err
}

// DefaultContext is each kind's context budget when a provider sets none:
// conservative, since one value covers all of a provider's models.
var DefaultContext = map[string]int64{"anthropic": 200_000, "openai": 128_000}

// Budget is how many input tokens an agent on this provider may use before
// older messages are compacted.
func (p Provider) Budget() int64 {
	if p.ContextTokens > 0 {
		return p.ContextTokens
	}
	if n := DefaultContext[p.Kind]; n > 0 {
		return n
	}
	return 128_000
}

// Service is the client-facing API. Every exported method is callable by clients.
type Service struct{ st *Store }

func NewService(st *Store) *Service { return &Service{st: st} }

// Providers lists configs without secrets.
func (s *Service) Providers() ([]Provider, error) {
	rows, err := s.st.db.Query(selectProvider + ` ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Provider{}
	for rows.Next() {
		p, _, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Save creates or updates a provider. Saving is naturally idempotent (keyed
// by ID). An empty APIKey keeps the stored key.
func (s *Service) Save(req SaveRequest) (Provider, error) {
	req.ID, req.Kind = strings.TrimSpace(req.ID), strings.TrimSpace(req.Kind)
	if !idPattern.MatchString(req.ID) {
		return Provider{}, fmt.Errorf("%w: id must be lowercase letters, digits, and dashes", apperr.Invalid)
	}
	if s.st.adapters[req.Kind] == nil {
		return Provider{}, fmt.Errorf("%w: unsupported provider kind %q", apperr.Invalid, req.Kind)
	}
	if req.Name = strings.TrimSpace(req.Name); req.Name == "" {
		req.Name = req.ID
	}
	models := cleanList(req.Models)
	if len(models) == 0 {
		models = s.st.defaults[req.Kind]
	}
	if len(models) == 0 {
		// Re-saving (e.g. a new key) keeps the provider's current list.
		if old, _, err := scan(s.st.db.QueryRow(selectProvider+` WHERE id = ?`, req.ID)); err == nil {
			models = old.Models
		}
	}
	if len(models) == 0 {
		// No list given and no defaults (OpenAI-compatible endpoints differ):
		// ask the endpoint, with the key being saved or the stored one.
		listed, err := s.listModels(req)
		if err != nil {
			return Provider{}, fmt.Errorf("%w: list the models to offer, or fix the endpoint: %v", apperr.Invalid, err)
		}
		models = listed
	}
	modelsJSON, _ := json.Marshal(models)
	key := strings.TrimSpace(req.APIKey)
	sealed, hint := "", ""
	if key != "" {
		var err error
		if sealed, err = s.st.box.Seal(key); err != nil {
			return Provider{}, err
		}
		hint = "…" + key[max(0, len(key)-4):]
	}
	if req.ContextTokens < 0 {
		return Provider{}, fmt.Errorf("%w: context tokens cannot be negative", apperr.Invalid)
	}
	_, err := s.st.db.Exec(`INSERT INTO providers (id, kind, name, base_url, models, key_sealed, key_hint, updated_at, context_tokens)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET kind = excluded.kind, name = excluded.name, base_url = excluded.base_url,
			models = excluded.models, updated_at = excluded.updated_at, context_tokens = excluded.context_tokens,
			key_sealed = CASE WHEN excluded.key_sealed = '' THEN providers.key_sealed ELSE excluded.key_sealed END,
			key_hint   = CASE WHEN excluded.key_sealed = '' THEN providers.key_hint   ELSE excluded.key_hint   END`,
		req.ID, req.Kind, req.Name, strings.TrimSpace(req.BaseURL), string(modelsJSON), sealed, hint, sqlitedb.Stamp(time.Now()), req.ContextTokens)
	if err != nil {
		return Provider{}, err
	}
	p, _, err := scan(s.st.db.QueryRow(selectProvider+` WHERE id = ?`, req.ID))
	return p, err
}

// Delete removes a provider and its key. Deleting a missing one is not an error.
func (s *Service) Delete(id string) error {
	_, err := s.st.db.Exec(`DELETE FROM providers WHERE id = ?`, id)
	return err
}

// Test checks the stored key by listing the provider's models.
func (s *Service) Test(id string) (TestResult, error) {
	p, creds, err := s.st.Credentials(id)
	if err != nil {
		return TestResult{Message: err.Error()}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	models, err := s.st.adapters[p.Kind].Models(ctx, creds)
	if err != nil {
		return TestResult{Message: err.Error()}, nil
	}
	what := "Key works"
	if !p.HasKey {
		what = "Endpoint works"
	}
	return TestResult{OK: true, Message: fmt.Sprintf("%s; %d models available.", what, len(models)), Models: models}, nil
}

// listModels asks the endpoint being saved for its models.
func (s *Service) listModels(req SaveRequest) ([]string, error) {
	creds := llm.Credentials{APIKey: strings.TrimSpace(req.APIKey), BaseURL: strings.TrimSpace(req.BaseURL)}
	if creds.APIKey == "" {
		if _, stored, err := s.st.Credentials(req.ID); err == nil {
			creds.APIKey = stored.APIKey
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	models, err := s.st.adapters[req.Kind].Models(ctx, creds)
	if err == nil && len(models) == 0 {
		err = errors.New("the endpoint lists no models")
	}
	return models, err
}

func cleanList(in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
