package eventlog_test

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/devark28/xhaos/app/eventlog"
	"github.com/devark28/xhaos/app/sqlitedb"
	"github.com/devark28/xhaos/app/workspace"
)

func openDB(t *testing.T, dir string) *sql.DB {
	t.Helper()
	db, err := sqlitedb.Open(filepath.Join(dir, "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Migrate(db); err != nil {
		t.Fatal(err)
	}
	if err := eventlog.Migrate(db); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eventlog.Close(db); db.Close() })
	return db
}

func appendOne(t *testing.T, db *sql.DB, agentID int64, kind string, data any, commit bool) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eventlog.Append(tx, workspace.DefaultID, agentID, kind, data, time.Now()); err != nil {
		t.Fatal(err)
	}
	if commit {
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	} else {
		tx.Rollback()
	}
}

// Agent events go to the agent's JSON Lines file, one self-describing line
// each; SQLite keeps the index. Workspace events stay inline.
func TestAgentEventsLiveInJSONLFiles(t *testing.T) {
	dir := t.TempDir()
	db := openDB(t, dir)
	appendOne(t, db, 7, "agent.message", map[string]string{"body": "hello"}, true)
	appendOne(t, db, 7, "agent.message", map[string]string{"body": "rolled back"}, false)
	appendOne(t, db, 7, "agent.tool", map[string]string{"output": strings.Repeat("x", 50_000)}, true)
	appendOne(t, db, 0, "memory.saved", map[string]string{"content": "inline"}, true)

	var stored string
	db.QueryRow(`SELECT data FROM events WHERE seq = 1`).Scan(&stored)
	if stored != "" {
		t.Fatalf("agent event body in SQLite: %q", stored)
	}
	db.QueryRow(`SELECT data FROM events WHERE kind = 'memory.saved'`).Scan(&stored)
	if !strings.Contains(stored, "inline") {
		t.Fatalf("workspace event not inline: %q", stored)
	}

	// The file reads on its own: seq, kind, agent and data on every line.
	f, err := os.Open(filepath.Join(dir, "transcripts", workspace.DefaultID, "7.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var lines []map[string]any
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var l map[string]any
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			t.Fatalf("not JSON Lines: %v", err)
		}
		lines = append(lines, l)
	}
	if len(lines) != 3 || lines[0]["kind"] != "agent.message" || lines[0]["seq"].(float64) != 1 {
		t.Fatalf("lines = %v", len(lines))
	}

	// The index decides: the rolled-back line is in the file, not in the log.
	events, err := eventlog.List(db, workspace.DefaultID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || !strings.Contains(string(events[0].Data), "hello") || len(events[1].Data) < 50_000 || events[1].Seq != 2 {
		t.Fatalf("events = %d", len(events))
	}
	for _, e := range events {
		if strings.Contains(string(e.Data), "rolled back") {
			t.Fatal("a rolled-back event was listed")
		}
	}
	if got, _ := eventlog.ListAgent(db, workspace.DefaultID, 7, "agent.tool"); len(got) != 1 {
		t.Fatalf("ListAgent = %d", len(got))
	}
}

// Rows written before transcript files keep reading; a missing file shows
// as a lost body instead of hiding the whole log.
func TestOlderRowsAndMissingFiles(t *testing.T) {
	dir := t.TempDir()
	db := openDB(t, dir)
	db.Exec(`INSERT INTO events (workspace_id, seq, at, agent_id, kind, data) VALUES (?, 1, ?, 3, 'agent.message', '{"body":"old"}')`, workspace.DefaultID, sqlitedb.Stamp(time.Now()))
	appendOne(t, db, 4, "agent.message", map[string]string{"body": "new"}, true)
	eventlog.Close(db)
	if err := os.Remove(filepath.Join(dir, "transcripts", workspace.DefaultID, "4.jsonl")); err != nil {
		t.Fatal(err)
	}
	events, err := eventlog.List(db, workspace.DefaultID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || !strings.Contains(string(events[0].Data), "old") || string(events[1].Data) != `{"lost":true}` {
		t.Fatalf("events = %s / %s", events[0].Data, events[1].Data)
	}
	if _, err := os.Stat(filepath.Join(dir, "transcripts", workspace.DefaultID, "4.jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reading recreated the file (%v)", err)
	}
}
