package board

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devark28/xhaos/app/apperr"
	"github.com/devark28/xhaos/app/eventlog"
	"github.com/devark28/xhaos/app/sqlitedb"
	"github.com/devark28/xhaos/app/workspace"
)

// png is a valid 1x1 PNG, enough for the content sniffing.
var png = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89")

type fixture struct {
	db    *sql.DB
	svc   *Service
	dir   string
	other string // a second workspace
	seen  []eventlog.Event
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	db, err := sqlitedb.Open(filepath.Join(dir, "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := workspace.Migrate(db); err != nil {
		t.Fatal(err)
	}
	if err := eventlog.Migrate(db); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.Create(db, "other", "Other"); err != nil {
		t.Fatal(err)
	}
	f := &fixture{db: db, dir: filepath.Join(dir, "boards"), other: "other"}
	f.svc = NewService(db, func(e eventlog.Event) { f.seen = append(f.seen, e) }, f.dir)
	return f
}

func el(id string, version, nonce int64, extra string) json.RawMessage {
	return json.RawMessage(`{"id":"` + id + `","version":` + itoa(version) + `,"versionNonce":` + itoa(nonce) + `,"isDeleted":false` + extra + `}`)
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func ids(list []json.RawMessage) []string {
	var out []string
	for _, raw := range list {
		var h header
		_ = json.Unmarshal(raw, &h)
		out = append(out, h.ID)
	}
	return out
}

func kinds(events []eventlog.Event) []string {
	var out []string
	for _, e := range events {
		out = append(out, e.Kind)
	}
	return out
}

func TestBoardsListCreateRenameDeleteLoad(t *testing.T) {
	f := newFixture(t)
	first, err := f.svc.Create(CreateRequest{WorkspaceID: workspace.DefaultID})
	if err != nil {
		t.Fatal(err)
	}
	if first.Name != "Board 1" {
		t.Errorf("default name = %q", first.Name)
	}
	second, err := f.svc.Create(CreateRequest{WorkspaceID: workspace.DefaultID, Name: "  Sketches "})
	if err != nil {
		t.Fatal(err)
	}
	if second.Name != "Sketches" {
		t.Errorf("name not trimmed: %q", second.Name)
	}
	list, err := f.svc.List(workspace.DefaultID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != first.ID || list[1].ID != second.ID {
		t.Fatalf("list = %+v", list)
	}
	renamed, err := f.svc.Rename(RenameRequest{WorkspaceID: workspace.DefaultID, ID: first.ID, Name: "Plan"})
	if err != nil || renamed.Name != "Plan" {
		t.Fatalf("rename: %+v %v", renamed, err)
	}
	if _, err := f.svc.Rename(RenameRequest{WorkspaceID: workspace.DefaultID, ID: first.ID, Name: "  "}); !errors.Is(err, apperr.Invalid) {
		t.Errorf("empty rename: %v", err)
	}
	if err := f.svc.Delete(workspace.DefaultID, second.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Load(workspace.DefaultID, second.ID); !errors.Is(err, apperr.NotFound) {
		t.Errorf("deleted board still loads: %v", err)
	}
	b, err := f.svc.Load(workspace.DefaultID, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if b.Name != "Plan" || len(b.Elements) != 0 || string(b.AppState) != "{}" {
		t.Errorf("fresh board = %+v", b)
	}
	want := []string{EventCreated, EventCreated, EventRenamed, EventDeleted}
	if got := kinds(f.seen); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("events = %v, want %v", got, want)
	}
}

func TestWorkspacesAreSeparate(t *testing.T) {
	f := newFixture(t)
	mine, err := f.svc.Create(CreateRequest{WorkspaceID: workspace.DefaultID, Name: "Private"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Save(SaveRequest{WorkspaceID: workspace.DefaultID, ID: mine.ID, Elements: []json.RawMessage{el("a", 1, 1, "")}}); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.AddImage(ImageUpload{WorkspaceID: workspace.DefaultID, BoardID: mine.ID, FileID: "img", Data: png}); err != nil {
		t.Fatal(err)
	}
	other := f.other
	if list, _ := f.svc.List(other); len(list) != 0 {
		t.Errorf("other workspace lists %d boards", len(list))
	}
	checks := map[string]error{
		"Load": func() error { _, err := f.svc.Load(other, mine.ID); return err }(),
		"Rename": func() error {
			_, err := f.svc.Rename(RenameRequest{WorkspaceID: other, ID: mine.ID, Name: "x"})
			return err
		}(),
		"Delete": f.svc.Delete(other, mine.ID),
		"Save": func() error {
			_, err := f.svc.Save(SaveRequest{WorkspaceID: other, ID: mine.ID, Elements: []json.RawMessage{el("b", 1, 1, "")}})
			return err
		}(),
		"AddImage": f.svc.AddImage(ImageUpload{WorkspaceID: other, BoardID: mine.ID, FileID: "img2", Data: png}),
		"Image":    func() error { _, err := f.svc.Image(other, mine.ID, "img"); return err }(),
	}
	for name, err := range checks {
		if !errors.Is(err, apperr.NotFound) {
			t.Errorf("%s from another workspace: %v (want not found)", name, err)
		}
	}
	// The owner's board is untouched by those attempts.
	b, err := f.svc.Load(workspace.DefaultID, mine.ID)
	if err != nil || b.Name != "Private" || len(b.Elements) != 1 || len(b.Images) != 1 {
		t.Fatalf("board changed by another workspace: %+v %v", b, err)
	}
	if _, err := f.svc.List("../default"); !errors.Is(err, apperr.Invalid) {
		t.Errorf("path-like workspace accepted: %v", err)
	}
}

func TestSaveMergesByVersion(t *testing.T) {
	f := newFixture(t)
	b, _ := f.svc.Create(CreateRequest{WorkspaceID: workspace.DefaultID})
	ws := workspace.DefaultID
	if _, err := f.svc.Save(SaveRequest{WorkspaceID: ws, ID: b.ID, Elements: []json.RawMessage{el("a", 3, 50, `,"x":"tab1"`)}}); err != nil {
		t.Fatal(err)
	}
	// A stale tab (older version) must not overwrite the newer element.
	res, err := f.svc.Save(SaveRequest{WorkspaceID: ws, ID: b.ID, Elements: []json.RawMessage{el("a", 2, 10, `,"x":"stale"`)}})
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(res.Newer); len(got) != 1 || got[0] != "a" {
		t.Fatalf("stale save: newer = %v, want [a]", got)
	}
	if !strings.Contains(string(res.Newer[0]), `"tab1"`) {
		t.Errorf("newer is not the stored element: %s", res.Newer[0])
	}
	// A newer version does replace it.
	if _, err := f.svc.Save(SaveRequest{WorkspaceID: ws, ID: b.ID, Elements: []json.RawMessage{el("a", 4, 99, `,"x":"tab2"`)}}); err != nil {
		t.Fatal(err)
	}
	got, _ := f.svc.Load(ws, b.ID)
	if len(got.Elements) != 1 || !strings.Contains(string(got.Elements[0]), `"tab2"`) {
		t.Fatalf("after newer save: %s", got.Elements)
	}
	// The save that changed nothing is a no-op: no event, nothing newer.
	before := len(f.seen)
	res, _ = f.svc.Save(SaveRequest{WorkspaceID: ws, ID: b.ID, Elements: []json.RawMessage{el("a", 4, 99, `,"x":"tab2"`)}})
	if len(res.Newer) != 0 || len(f.seen) != before {
		t.Errorf("identical save: newer=%d events+%d", len(res.Newer), len(f.seen)-before)
	}
}

// Equal versions: the lower versionNonce wins, as in Excalidraw.
func TestStoredWinsRule(t *testing.T) {
	cases := []struct {
		stored, in header
		want       bool
	}{
		{header{Version: 5}, header{Version: 4}, true},
		{header{Version: 4}, header{Version: 5}, false},
		{header{Version: 5, VersionNonce: 1}, header{Version: 5, VersionNonce: 2}, true},
		{header{Version: 5, VersionNonce: 2}, header{Version: 5, VersionNonce: 1}, false},
	}
	for _, c := range cases {
		if got := storedWins(c.stored, c.in); got != c.want {
			t.Errorf("storedWins(%+v, %+v) = %v, want %v", c.stored, c.in, got, c.want)
		}
	}
}

func TestSaveKeepsElementsItDoesNotMention(t *testing.T) {
	f := newFixture(t)
	b, _ := f.svc.Create(CreateRequest{WorkspaceID: workspace.DefaultID})
	ws := workspace.DefaultID
	f.svc.Save(SaveRequest{WorkspaceID: ws, ID: b.ID, Elements: []json.RawMessage{el("tab1", 1, 1, "")}})
	f.svc.Save(SaveRequest{WorkspaceID: ws, ID: b.ID, Elements: []json.RawMessage{el("tab2", 1, 1, "")}})
	got, _ := f.svc.Load(ws, b.ID)
	if strings.Join(ids(got.Elements), ",") != "tab1,tab2" {
		t.Fatalf("elements = %v", ids(got.Elements))
	}
}

func TestDeletionIsATombstoneThatWins(t *testing.T) {
	f := newFixture(t)
	b, _ := f.svc.Create(CreateRequest{WorkspaceID: workspace.DefaultID})
	ws := workspace.DefaultID
	f.svc.Save(SaveRequest{WorkspaceID: ws, ID: b.ID, Elements: []json.RawMessage{el("a", 1, 1, "")}})
	// Tab 1 deletes a (version 2, tombstone).
	tomb := json.RawMessage(`{"id":"a","version":2,"versionNonce":7,"isDeleted":true}`)
	f.svc.Save(SaveRequest{WorkspaceID: ws, ID: b.ID, Elements: []json.RawMessage{tomb}})
	// Tab 2, still showing a at version 1, saves: the tombstone stays.
	res, _ := f.svc.Save(SaveRequest{WorkspaceID: ws, ID: b.ID, Elements: []json.RawMessage{el("a", 1, 1, "")}})
	if len(res.Newer) != 1 || !strings.Contains(string(res.Newer[0]), `"isDeleted":true`) {
		t.Fatalf("stale save resurrected a: newer=%s", res.Newer)
	}
}

func TestSaveEventCarriesOnlyChanges(t *testing.T) {
	f := newFixture(t)
	b, _ := f.svc.Create(CreateRequest{WorkspaceID: workspace.DefaultID})
	ws := workspace.DefaultID
	f.seen = nil
	f.svc.Save(SaveRequest{WorkspaceID: ws, ID: b.ID, Elements: []json.RawMessage{el("a", 1, 1, ""), el("b", 1, 1, "")}})
	f.svc.Save(SaveRequest{WorkspaceID: ws, ID: b.ID, Elements: []json.RawMessage{el("a", 1, 1, ""), el("b", 2, 1, "")}})
	if len(f.seen) != 2 || f.seen[0].Kind != EventSaved {
		t.Fatalf("events = %v", kinds(f.seen))
	}
	var body struct {
		ID       int64             `json:"id"`
		Elements []json.RawMessage `json:"elements"`
	}
	json.Unmarshal(f.seen[1].Data, &body)
	if body.ID != b.ID || strings.Join(ids(body.Elements), ",") != "b" {
		t.Errorf("second event carries %s, want only b", f.seen[1].Data)
	}
}

func TestSaveRejectsBadElements(t *testing.T) {
	f := newFixture(t)
	b, _ := f.svc.Create(CreateRequest{WorkspaceID: workspace.DefaultID})
	ws := workspace.DefaultID
	if _, err := f.svc.Save(SaveRequest{WorkspaceID: ws, ID: b.ID, Elements: []json.RawMessage{json.RawMessage(`{"version":1}`)}}); !errors.Is(err, apperr.Invalid) {
		t.Errorf("element without id: %v", err)
	}
	if _, err := f.svc.Save(SaveRequest{WorkspaceID: ws, ID: b.ID, Elements: []json.RawMessage{json.RawMessage(`[1]`)}}); !errors.Is(err, apperr.Invalid) {
		t.Errorf("element not an object: %v", err)
	}
	many := make([]json.RawMessage, maxElements+1)
	for i := range many {
		many[i] = el("e", 1, 1, "")
	}
	if _, err := f.svc.Save(SaveRequest{WorkspaceID: ws, ID: b.ID, Elements: many}); !errors.Is(err, apperr.Invalid) {
		t.Errorf("too many elements: %v", err)
	}
}

func TestImagesAreStoredOnceByHash(t *testing.T) {
	f := newFixture(t)
	ws := workspace.DefaultID
	a, _ := f.svc.Create(CreateRequest{WorkspaceID: ws})
	b, _ := f.svc.Create(CreateRequest{WorkspaceID: ws})
	if err := f.svc.AddImage(ImageUpload{WorkspaceID: ws, BoardID: a.ID, FileID: "f1", Created: 1, Data: png}); err != nil {
		t.Fatal(err)
	}
	// The same bytes under another file ID on another board: no second file.
	if err := f.svc.AddImage(ImageUpload{WorkspaceID: ws, BoardID: b.ID, FileID: "f2", Created: 2, Data: png}); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Join(f.dir, ws))
	if len(entries) != 1 {
		t.Fatalf("%d files for one image", len(entries))
	}
	img, err := f.svc.Image(ws, b.ID, "f2")
	if err != nil || img.MimeType != "image/png" || !bytes.Equal(img.Data, png) {
		t.Fatalf("image = %s %d bytes %v", img.MimeType, len(img.Data), err)
	}
	loaded, _ := f.svc.Load(ws, a.ID)
	if len(loaded.Images) != 1 || loaded.Images[0].FileID != "f1" || loaded.Images[0].MimeType != "image/png" {
		t.Errorf("images on the board = %+v", loaded.Images)
	}
	// Deleting one board keeps the file the other still uses.
	if err := f.svc.Delete(ws, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Image(ws, b.ID, "f2"); err != nil {
		t.Fatalf("shared image lost: %v", err)
	}
	// Deleting the last board removes the file.
	if err := f.svc.Delete(ws, b.ID); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(filepath.Join(f.dir, ws)); len(entries) != 0 {
		t.Errorf("%d files left after the last board is gone", len(entries))
	}
}

func TestImagesTypeComesFromTheBytes(t *testing.T) {
	f := newFixture(t)
	ws := workspace.DefaultID
	b, _ := f.svc.Create(CreateRequest{WorkspaceID: ws})
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	for name, data := range map[string][]byte{"svg": svg, "text": []byte("hello"), "empty": nil} {
		if err := f.svc.AddImage(ImageUpload{WorkspaceID: ws, BoardID: b.ID, FileID: name, Data: data}); !errors.Is(err, apperr.Invalid) {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
}
