package history

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sessionOne = `{"type":"user","sessionId":"s1","timestamp":"2026-03-01T10:00:00Z","message":{"role":"user","content":"Decide where the cache lives"}}
{"type":"assistant","sessionId":"s1","timestamp":"2026-03-01T10:00:05Z","message":{"role":"assistant","content":[{"type":"text","text":"We keep the verdict cache in memory with a fixed cap."}]}}
{"type":"user","sessionId":"s1","timestamp":"2026-03-01T10:01:00Z","message":{"role":"user","content":"Use this key sk-ant-api03-EXAMPLEKEY0123456789abcdef for the test"}}
`

const sessionTwo = `{"type":"user","sessionId":"s2","timestamp":"2026-03-05T08:00:00Z","message":{"role":"user","content":"Plant the zebra marker in the lockfile parser"}}
`

// writeFolder writes two sessions into root/folder, as Claude Code does.
func writeFolder(t *testing.T, root, folder string) {
	t.Helper()
	dir := filepath.Join(root, folder)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"s1.jsonl": sessionOne, "s2.jsonl": sessionTwo} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestImportIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	root := t.TempDir()
	writeFolder(t, root, "-home-pc-Documents-demo")
	p := ProjectRef{ID: 7, Name: "demo", Path: "/home/devark28/Documents/demo"}
	emb := &fakeEmbedder{dim: 64}

	first, err := ImportFolder(ctx, s, emb, "ws", p, root, "-home-pc-Documents-demo")
	if err != nil {
		t.Fatal(err)
	}
	if first.Files != 2 || first.New == 0 || first.New != first.Embedded || first.Scrubbed == 0 {
		t.Fatalf("first import: %+v", first)
	}
	chunks, scrubbed, err := s.Count("ws")
	if err != nil {
		t.Fatal(err)
	}
	if chunks != first.New || scrubbed != first.Scrubbed {
		t.Fatalf("stored %d chunks (%d scrubbed), report says %d (%d)", chunks, scrubbed, first.New, first.Scrubbed)
	}

	before := emb.calls
	second, err := ImportFolder(ctx, s, emb, "ws", p, root, "-home-pc-Documents-demo")
	if err != nil {
		t.Fatal(err)
	}
	if second.New != 0 || second.Embedded != 0 || emb.calls != before {
		t.Fatalf("second import added %d chunks, embedded %d (embedder calls %d → %d)", second.New, second.Embedded, before, emb.calls)
	}
	if again, _, _ := s.Count("ws"); again != chunks {
		t.Fatalf("chunks went from %d to %d on a re-import", chunks, again)
	}
}

func TestSearchReturnsPlantedChunkFirst(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	root := t.TempDir()
	writeFolder(t, root, "-home-pc-Documents-demo")
	p := ProjectRef{ID: 7, Name: "demo", Path: "/home/devark28/Documents/demo"}
	emb := &fakeEmbedder{dim: 256}
	if _, err := ImportFolder(ctx, s, emb, "ws", p, root, "-home-pc-Documents-demo"); err != nil {
		t.Fatal(err)
	}
	q, _ := emb.Embed(ctx, []string{"zebra lockfile parser"})
	hits, err := s.Search(ctx, "ws", nil, q[0], 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || !strings.Contains(hits[0].Text, "zebra marker") {
		t.Fatalf("top hit is not the planted chunk: %+v", hits)
	}
	if hits[0].Session != "s2" || hits[0].At.Format(time.DateOnly) != "2026-03-05" || hits[0].ProjectID != 7 {
		t.Fatalf("hit metadata: %+v", hits[0])
	}
	for i := 1; i < len(hits); i++ {
		if hits[i].Score > hits[i-1].Score {
			t.Fatal("hits are not best first")
		}
	}
}

func TestSearchScopedToProjects(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	root := t.TempDir()
	writeFolder(t, root, "a")
	writeFolder(t, root, "b")
	emb := &fakeEmbedder{dim: 256}
	if _, err := ImportFolder(ctx, s, emb, "ws", ProjectRef{ID: 1}, root, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportFolder(ctx, s, emb, "ws", ProjectRef{ID: 2}, root, "b"); err != nil {
		t.Fatal(err)
	}
	q, _ := emb.Embed(ctx, []string{"zebra"})
	hits, err := s.Search(ctx, "ws", []int64{1}, q[0], 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("no hits for project 1")
	}
	for _, h := range hits {
		if h.ProjectID != 1 {
			t.Fatalf("hit from project %d leaked into a project 1 search", h.ProjectID)
		}
	}
	other, _ := s.Search(ctx, "other-workspace", nil, q[0], 10)
	if len(other) != 0 {
		t.Fatalf("another workspace sees %d hits", len(other))
	}
}

func TestSearchOnEmptyStore(t *testing.T) {
	s := openStore(t)
	hits, err := s.Search(context.Background(), "ws", nil, []float32{1, 0}, 5)
	if err != nil || len(hits) != 0 {
		t.Fatalf("empty store: %v %v", hits, err)
	}
}

func TestProposeMapsFolders(t *testing.T) {
	projects := []ProjectRef{
		{ID: 1, Path: "/home/devark28/Documents/xhaos"},
		{ID: 2, Path: "/home/devark28/Documents/verlet-ego"},
		{ID: 3, Path: "/srv/a/demo"},
		{ID: 4, Path: "/srv/b/demo"},
	}
	cases := []struct {
		folder string
		want   int64 // 0: no proposal
		exact  bool
	}{
		{"-home-devark28-Documents-xhaos", 1, true},
		{"-home-pc-Documents-xhaos", 1, false},
		{"-home-pc-Documents-verlet-ego", 2, false},
		{"-home-pc-Documents-demo", 0, false}, // two projects end in "demo": the owner maps it
		{"-home-pc-Documents-unknown", 0, false},
	}
	for _, c := range cases {
		props := Propose([]string{c.folder}, projects)
		if c.want == 0 {
			if len(props) != 0 {
				t.Errorf("%s: proposed %+v, want none", c.folder, props[0].Project)
			}
			continue
		}
		if len(props) != 1 || props[0].Project.ID != c.want || props[0].Exact != c.exact {
			t.Errorf("%s: got %+v, want project %d exact %v", c.folder, props, c.want, c.exact)
		}
	}
}

func TestFoldersListsOnlyTranscriptFolders(t *testing.T) {
	root := t.TempDir()
	writeFolder(t, root, "with-transcripts")
	if err := os.MkdirAll(filepath.Join(root, "memory-only"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := Folders(root)
	if err != nil || len(got) != 1 || got[0] != "with-transcripts" {
		t.Fatalf("Folders = %v, %v", got, err)
	}
}

// The count is of secrets removed, not of the marker text: a transcript that
// already contains the literal [redacted] and no secret counts as zero.
func TestImportCountsRemovedSecretsOnly(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	root := t.TempDir()
	dir := filepath.Join(root, "-f")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"type":"user","sessionId":"s1","timestamp":"2026-03-01T10:00:00Z","message":{"role":"user","content":"The log says [redacted] in place of the value"}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "s.jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := ImportFolder(ctx, s, &fakeEmbedder{dim: 64}, "ws", ProjectRef{ID: 1, Name: "demo"}, root, "-f")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Chunks != 1 || rep.Scrubbed != 0 {
		t.Fatalf("literal [redacted] counted as a removed secret: %+v", rep)
	}
	if _, scrubbed, _ := s.Count("ws"); scrubbed != 0 {
		t.Fatalf("stored %d scrubbed chunks, want 0", scrubbed)
	}
}
