package history

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devark28/xhaos/app/agent"
	"github.com/devark28/xhaos/app/llm"
	"github.com/devark28/xhaos/app/sqlitedb"
	"github.com/devark28/xhaos/app/tool"
)

// setup imports two projects (1 "alpha", 2 "beta") with the same kind of
// session, so a search finds passages in both.
func setup(t *testing.T) (*Service, *Tools) {
	t.Helper()
	noCutoff(t) // the hashing fake embedder scores far below the model
	db, err := sqlitedb.Open(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE projects (id INTEGER PRIMARY KEY, workspace_id TEXT, name TEXT, path TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO projects (id, workspace_id, name, path) VALUES (1, 'ws', 'alpha', '/a'), (2, 'ws', 'beta', '/b')`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	emb := &fakeEmbedder{dim: 256}
	s := NewService(db, emb)
	root := t.TempDir()
	writeFolder(t, root, "a")
	writeFolder(t, root, "b")
	if _, err := ImportFolder(context.Background(), s.store, emb, "ws", ProjectRef{ID: 1, Name: "alpha"}, root, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportFolder(context.Background(), s.store, emb, "ws", ProjectRef{ID: 2, Name: "beta"}, root, "b"); err != nil {
		t.Fatal(err)
	}
	return s, NewTools(s)
}

func call(t *testing.T, tl *Tools, a agent.Agent, args string) (string, error) {
	t.Helper()
	c, err := tl.Prepare("ws", a, llm.ToolCall{ID: "1", Name: toolName, Input: json.RawMessage(args)})
	if err != nil {
		return "", err
	}
	return tl.Execute(context.Background(), "ws", a, c)
}

func TestHistorySearchIsScopedToTheAgentsProject(t *testing.T) {
	_, tl := setup(t)
	alpha := agent.Agent{ProjectID: 1}

	out, err := call(t, tl, alpha, `{"query":"zebra marker"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "alpha") || strings.Contains(out, "beta") {
		t.Fatalf("own-project search should only show alpha:\n%s", out)
	}
	if !strings.Contains(out, "2026-03-05") || !strings.Contains(out, "s2") {
		t.Fatalf("result lacks the session date and id:\n%s", out)
	}

	if _, err := call(t, tl, alpha, `{"query":"zebra","project":"beta"}`); err == nil || !strings.Contains(err.Error(), "not this agent's project") {
		t.Fatalf("searching another project without the allowance: %v", err)
	}
	if out, err := call(t, tl, alpha, `{"query":"zebra","project":"alpha"}`); err != nil || strings.Contains(out, "beta") {
		t.Fatalf("naming its own project: %v\n%s", err, out)
	}
	if _, err := call(t, tl, alpha, `{"query":"zebra","project":"nope"}`); err == nil {
		t.Fatal("an unknown project name must fail")
	}
}

func TestHistorySearchWorkspaceAllowance(t *testing.T) {
	s, tl := setup(t)
	if err := s.SetAllProjects(true); err != nil {
		t.Fatal(err)
	}
	alpha := agent.Agent{ProjectID: 1}
	out, err := call(t, tl, alpha, `{"query":"zebra marker"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "alpha") || !strings.Contains(out, "beta") {
		t.Fatalf("workspace allowance should search both projects:\n%s", out)
	}
	if _, err := call(t, tl, alpha, `{"query":"zebra","project":"beta"}`); err != nil {
		t.Fatalf("naming another project with the allowance: %v", err)
	}
	if err := s.SetAllProjects(false); err != nil {
		t.Fatal(err)
	}
	if on, _ := s.AllProjects(); on {
		t.Fatal("the switch did not turn off")
	}
}

func TestHistorySearchNeedsAProject(t *testing.T) {
	_, tl := setup(t)
	if _, err := call(t, tl, agent.Agent{}, `{"query":"zebra"}`); err == nil {
		t.Fatal("an agent with no project and no allowance must be refused")
	}
}

func TestHistorySearchInputChecks(t *testing.T) {
	_, tl := setup(t)
	a := agent.Agent{ProjectID: 1}
	for _, bad := range []string{`{}`, `{"query":"  "}`, `{"query":"x","limit":11}`, `{"query":"x","extra":1}`} {
		if _, err := tl.Prepare("ws", a, llm.ToolCall{ID: "1", Name: toolName, Input: json.RawMessage(bad)}); err == nil {
			t.Errorf("Prepare(%s) accepted", bad)
		}
	}
	c, err := tl.Prepare("ws", a, llm.ToolCall{ID: "1", Name: toolName, Input: json.RawMessage(`{"query":"zebra"}`)})
	if err != nil || c.Effect != tool.Read {
		t.Fatalf("a search is a read: %+v %v", c, err)
	}
}

func TestOwnerSearchAndStats(t *testing.T) {
	s, _ := setup(t)
	res, err := s.Search(SearchRequest{WorkspaceID: "ws", Query: "zebra marker", ProjectID: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) == 0 || res[0].ProjectName != "beta" || res[0].Excerpt == "" {
		t.Fatalf("owner search: %+v", res)
	}
	st, err := s.Stats("ws")
	if err != nil || st.Chunks == 0 || st.Scrubbed == 0 {
		t.Fatalf("stats: %+v %v", st, err)
	}
	if _, err := s.Search(SearchRequest{WorkspaceID: "ws", Query: "  "}); err == nil {
		t.Fatal("an empty query must fail")
	}
}

func TestSearchWithoutEmbeddingServerSaysSo(t *testing.T) {
	db, err := sqlitedb.Open(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	s := NewService(db, nil)
	if s.Enabled() || s.Health().OK {
		t.Fatal("no embedding server must read as off")
	}
	if _, err := s.Search(SearchRequest{WorkspaceID: "ws", Query: "x"}); err == nil || !strings.Contains(err.Error(), "-history-url") {
		t.Fatalf("search without a server: %v", err)
	}
}

// noCutoff turns the similarity cutoff off for the test, for the fake embedder.
func noCutoff(t *testing.T) {
	t.Helper()
	old := minScore
	minScore = -1
	t.Cleanup(func() { minScore = old })
}

// The cutoff drops a passage that shares no words with the query, and keeps
// one that does (the fake embedder scores identical wording as 1).
func TestSearchCutoffDropsUnrelatedPassages(t *testing.T) {
	s, _ := setup(t)
	old := minScore
	minScore = 0.6
	t.Cleanup(func() { minScore = old })
	none, err := s.Search(SearchRequest{WorkspaceID: "ws", Query: "pizza recipe with mushrooms"})
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Fatalf("unrelated query returned %d passages: %+v", len(none), none)
	}
	some, err := s.Search(SearchRequest{WorkspaceID: "ws", Query: "zebra marker in the lockfile parser"})
	if err != nil {
		t.Fatal(err)
	}
	if len(some) == 0 {
		t.Fatal("related query returned nothing")
	}
}
