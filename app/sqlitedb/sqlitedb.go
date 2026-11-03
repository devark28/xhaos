// Package sqlitedb opens the shared SQLite database. Each feature package
// creates and owns its own tables.
package sqlitedb

import (
	"database/sql"
	"fmt"
	"net/url"
	"time"

	// Build with -tags libsqlite3 to link the system libsqlite3 dynamically;
	// without it the driver compiles its bundled SQLite (slow first build).
	_ "github.com/mattn/go-sqlite3"
)

// Open opens (creating if needed) the database at path with WAL and foreign
// keys enabled. It allows a single connection, so writes are serialized and
// per-workspace sequence numbers are assigned without races.
func Open(path string) (*sql.DB, error) {
	dsn := "file:" + (&url.URL{Path: path}).EscapedPath() + "?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return db, nil
}

// Querier is satisfied by *sql.DB and *sql.Tx.
type Querier interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// Stamp formats t the way every table stores timestamps.
func Stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// ParseStamp parses a stored timestamp; malformed values yield the zero time.
func ParseStamp(v string) time.Time { t, _ := time.Parse(time.RFC3339Nano, v); return t }

// NullID stores 0 as NULL, for optional integer foreign keys.
func NullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}
