// Package prefs keeps the user's interface preferences (appearance) on the
// server, so every browser that signs in gets the same ones. Values are
// JSON documents under a short key; the page decides what is in them.
package prefs

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"xhaos/app/apperr"
	"xhaos/app/sqlitedb"
)

const schema = `
CREATE TABLE IF NOT EXISTS prefs (
	key        TEXT PRIMARY KEY,
	value      TEXT NOT NULL,
	updated_at TEXT NOT NULL
);`

func Migrate(db *sql.DB) error { _, err := db.Exec(schema); return err }

// keys the page may store, and the largest value each may hold.
var keys = map[string]int{"appearance": 4096}

type Service struct{ db *sql.DB }

func NewService(db *sql.DB) *Service { return &Service{db: db} }

// Get returns the stored value, or null when none is stored.
func (s *Service) Get(key string) (json.RawMessage, error) {
	if _, ok := keys[key]; !ok {
		return nil, fmt.Errorf("%w: unknown preference %q", apperr.Invalid, key)
	}
	var v string
	err := s.db.QueryRow(`SELECT value FROM prefs WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return json.RawMessage("null"), nil
	}
	return json.RawMessage(v), err
}

// Set stores a value (a JSON object).
func (s *Service) Set(key string, value json.RawMessage) error {
	limit, ok := keys[key]
	if !ok {
		return fmt.Errorf("%w: unknown preference %q", apperr.Invalid, key)
	}
	var obj map[string]any
	if len(value) > limit || json.Unmarshal(value, &obj) != nil {
		return fmt.Errorf("%w: %s must be a JSON object under %d bytes", apperr.Invalid, key, limit)
	}
	_, err := s.db.Exec(`INSERT INTO prefs (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, key, string(value), sqlitedb.Stamp(time.Now()))
	return err
}
