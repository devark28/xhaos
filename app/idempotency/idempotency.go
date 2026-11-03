// Package idempotency makes mutations safe to retry: the first call with a
// key runs and stores its result; a retry with the same key and request gets
// that result back without running again.
package idempotency

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"xhaos/app/apperr"
	"xhaos/app/sqlitedb"
)

const schema = `
CREATE TABLE IF NOT EXISTS idempotency (
	key          TEXT PRIMARY KEY,
	op           TEXT NOT NULL,
	request_hash TEXT NOT NULL,
	result       TEXT NOT NULL,
	created_at   TEXT NOT NULL
);`

func Migrate(db *sql.DB) error { _, err := db.Exec(schema); return err }

// Run executes fn in a transaction guarded by key. fn fills *result. On a
// retry (same op, key and request) Run fills *result from storage, skips fn,
// and returns replayed=true. The same key with a different request is a
// conflict.
func Run(db *sql.DB, op, key string, req, result any, fn func(*sql.Tx) error) (replayed bool, err error) {
	if strings.TrimSpace(key) == "" {
		return false, fmt.Errorf("%w: idempotencyKey is required", apperr.Invalid)
	}
	reqJSON, err := json.Marshal(req)
	if err != nil {
		return false, err
	}
	sum := sha256.Sum256(reqJSON)
	hash := hex.EncodeToString(sum[:])

	tx, err := db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var prevOp, prevHash, prevResult string
	switch err := tx.QueryRow(`SELECT op, request_hash, result FROM idempotency WHERE key = ?`, key).Scan(&prevOp, &prevHash, &prevResult); {
	case err == nil:
		if prevOp != op || prevHash != hash {
			return false, fmt.Errorf("%w: idempotency key %q was used for a different request", apperr.Conflict, key)
		}
		return true, json.Unmarshal([]byte(prevResult), result)
	case !errors.Is(err, sql.ErrNoRows):
		return false, err
	}

	if err := fn(tx); err != nil {
		return false, err
	}
	resJSON, err := json.Marshal(result)
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec(`INSERT INTO idempotency (key, op, request_hash, result, created_at) VALUES (?, ?, ?, ?, ?)`,
		key, op, hash, string(resJSON), sqlitedb.Stamp(time.Now())); err != nil {
		return false, err
	}
	return false, tx.Commit()
}
