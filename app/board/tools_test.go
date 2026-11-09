package board

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	imgpng "image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devark28/xhaos/app/agent"
	"github.com/devark28/xhaos/app/llm"
	"github.com/devark28/xhaos/app/sandbox"
	"github.com/devark28/xhaos/app/tool"
	"github.com/devark28/xhaos/app/workspace"
)

const ws = workspace.DefaultID

type toolFixture struct {
	*fixture
	tools *Tools
	root  string
	agent agent.Agent
	board Summary
}

func newToolFixture(t *testing.T) *toolFixture {
	t.Helper()
	f := newFixture(t)
	root := t.TempDir()
	tf := &toolFixture{fixture: f, root: root, agent: agent.Agent{ID: 7, WorkspaceID: ws, ProjectID: 1}}
	tf.tools = NewTools(f.svc, func(agent.Agent) (string, error) { return root, nil })
	b, err := f.svc.Create(CreateRequest{WorkspaceID: ws, Name: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	tf.board = b
	return tf
}

var callN int

// run prepares and executes a call as the runner does: prepare, (approve),
// prepare again, execute.
func (tf *toolFixture) run(t *testing.T, name string, in any) (string, tool.Call, error) {
	t.Helper()
	raw, _ := json.Marshal(in)
	callN++
	c := llm.ToolCall{ID: "call" + itoa(int64(callN)), Name: name, Input: raw}
	call, err := tf.tools.Prepare(ws, tf.agent, c)
	if err != nil {
		return "", call, err
	}
	if call, err = tf.tools.Prepare(ws, tf.agent, c); err != nil {
		return "", call, err
	}
	out, err := tf.tools.Execute(context.Background(), ws, tf.agent, call)
	return out, call, err
}

func (tf *toolFixture) scene(t *testing.T) []sceneElement {
	t.Helper()
	b, err := tf.svc.Load(ws, tf.board.ID)
	if err != nil {
		t.Fatal(err)
	}
	var out []sceneElement
	for _, raw := range b.Elements {
		var e sceneElement
		if err := json.Unmarshal(raw, &e); err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}

// ownerDrawing saves a rectangle the owner drew, at 0,0 100×80.
func (tf *toolFixture) ownerDrawing(t *testing.T) {
	t.Helper()
	_, err := tf.svc.Save(SaveRequest{WorkspaceID: ws, ID: tf.board.ID, Source: "page1",
		Elements: []json.RawMessage{el("own", 3, 5, `,"type":"rectangle","x":0,"y":0,"width":100,"height":80,"index":"a5"`)}})
	if err != nil {
		t.Fatal(err)
	}
	tf.notes = nil
}

func TestEffects(t *testing.T) {
	tf := newToolFixture(t)
	want := map[string]string{"board_list": tool.Read, "board_read": tool.Read, "board_create": tool.Write,
		"board_add_note": tool.Write, "board_add_image": tool.Write, "board_add_shapes": tool.Write}
	specs := tf.tools.Specs(ws)
	if len(specs) != len(want) {
		t.Fatalf("specs: %d tools, want %d", len(specs), len(want))
	}
	for _, s := range specs {
		if !tf.tools.Owns(s.Name) || effects[s.Name] != want[s.Name] {
			t.Errorf("%s: effect %q, want %q", s.Name, effects[s.Name], want[s.Name])
		}
	}
	for _, info := range tf.tools.Catalog() {
		if info.Source != "boards" {
			t.Errorf("%s: source %q", info.Name, info.Source)
		}
	}
}

func TestAddNoteBesideOwnerDrawing(t *testing.T) {
	tf := newToolFixture(t)
	tf.ownerDrawing(t)
	out, call, err := tf.run(t, "board_add_note", map[string]any{"board": tf.board.ID, "text": "Check the wiring\nthen the power"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(call.Preview, "Check the wiring") || !strings.Contains(call.Preview, "added by #7") {
		t.Errorf("preview: %q", call.Preview)
	}
	if !strings.Contains(out, "Added a note") {
		t.Errorf("result: %q", out)
	}
	if len(tf.notes) != 1 || tf.notes[0].Kind != NoticeSaved || tf.notes[0].Source != "agent-7" || tf.notes[0].ID != tf.board.ID {
		t.Fatalf("notices: %+v", tf.notes)
	}
	if got := len(tf.notes[0].Elements); got != 3 { // tag, box, label
		t.Fatalf("notice has %d elements, want 3", got)
	}
	owner := box{0, 0, 100, 80}
	var group string
	var tagged bool
	for _, e := range tf.scene(t) {
		if e.ID == "own" {
			continue
		}
		if e.agentOf() != 7 {
			t.Errorf("%s: agent %d", e.ID, e.agentOf())
		}
		if len(e.GroupIDs) != 1 || (group != "" && e.GroupIDs[0] != group) {
			t.Errorf("%s: groups %v", e.ID, e.GroupIDs)
		}
		group = e.GroupIDs[0]
		if e.bounds().near(owner, gap/2) {
			t.Errorf("%s at %+v is over or against the owner's drawing", e.ID, e.bounds())
		}
		if e.Index == nil || *e.Index <= "a5" {
			t.Errorf("%s: index %v is not after the owner's", e.ID, e.Index)
		}
		tagged = tagged || e.Text == "added by #7"
	}
	if !tagged {
		t.Error("no \"added by #7\" tag")
	}
}

func TestAdditionsDoNotOverlap(t *testing.T) {
	tf := newToolFixture(t)
	tf.ownerDrawing(t)
	for i := 0; i < 4; i++ {
		if _, _, err := tf.run(t, "board_add_note", map[string]any{"board": tf.board.ID, "text": strings.Repeat("words ", 20+i*10)}); err != nil {
			t.Fatal(err)
		}
	}
	groups := map[string]box{}
	for _, e := range tf.scene(t) {
		if e.ID == "own" {
			continue
		}
		g := e.GroupIDs[0]
		if b, ok := groups[g]; ok {
			groups[g] = b.union(e.bounds())
		} else {
			groups[g] = e.bounds()
		}
	}
	if len(groups) != 4 {
		t.Fatalf("%d groups, want 4", len(groups))
	}
	var list []box
	for _, b := range groups {
		if b.near(box{0, 0, 100, 80}, 0) {
			t.Errorf("group %+v overlaps the owner's drawing", b)
		}
		for _, o := range list {
			if b.near(o, 0) {
				t.Errorf("groups %+v and %+v overlap", b, o)
			}
		}
		list = append(list, b)
	}
}

func TestEmptyBoardStartsAtOrigin(t *testing.T) {
	tf := newToolFixture(t)
	if _, _, err := tf.run(t, "board_add_note", map[string]any{"board": tf.board.ID, "text": "hi"}); err != nil {
		t.Fatal(err)
	}
	for _, e := range tf.scene(t) {
		if e.Text == "added by #7" && (e.X != 0 || e.Y != 0) {
			t.Errorf("tag at %v,%v, want 0,0", e.X, e.Y)
		}
		if e.Index == nil {
			t.Errorf("%s has no index", e.ID)
		}
	}
}

func TestOwnerEditWinsOverAddition(t *testing.T) {
	tf := newToolFixture(t)
	if _, _, err := tf.run(t, "board_add_note", map[string]any{"board": tf.board.ID, "text": "hi"}); err != nil {
		t.Fatal(err)
	}
	// The page deletes the agent's tag (a tombstone at version 2): it stays deleted.
	var tag map[string]any
	b, _ := tf.svc.Load(ws, tf.board.ID)
	for _, raw := range b.Elements {
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		if m["text"] == "added by #7" {
			tag = m
		}
	}
	tag["version"], tag["versionNonce"], tag["isDeleted"] = 2, 1, true
	raw, _ := json.Marshal(tag)
	res, err := tf.svc.Save(SaveRequest{WorkspaceID: ws, ID: tf.board.ID, Source: "page1", Elements: []json.RawMessage{raw}})
	if err != nil || len(res.Newer) != 0 {
		t.Fatalf("save: %v %+v", err, res)
	}
	for _, e := range tf.scene(t) {
		if e.Text == "added by #7" && !e.IsDeleted {
			t.Error("the tag the owner deleted is back")
		}
	}
}

func TestBoardChecks(t *testing.T) {
	tf := newToolFixture(t)
	cases := []struct {
		name string
		in   map[string]any
		want string
	}{
		{"board_add_note", map[string]any{"board": 999, "text": "x"}, "no board #999"},
		{"board_add_note", map[string]any{"text": "x"}, "needs board"},
		{"board_add_note", map[string]any{"board": tf.board.ID, "text": "  "}, "needs text"},
		{"board_add_note", map[string]any{"board": tf.board.ID, "text": strings.Repeat("x", maxNote+1)}, "at most"},
		{"board_add_note", map[string]any{"board": tf.board.ID, "text": "x", "colour": "red"}, "invalid input"},
		{"board_create", map[string]any{"name": " "}, "needs name"},
		{"board_create", map[string]any{"name": strings.Repeat("n", maxName+1)}, "at most"},
		{"board_add_shapes", map[string]any{"board": tf.board.ID}, "needs shapes"},
		{"board_add_shapes", map[string]any{"board": tf.board.ID, "shapes": []map[string]any{{"type": "star", "x": 0, "y": 0}}}, "not \"star\""},
		{"board_add_shapes", map[string]any{"board": tf.board.ID, "shapes": []map[string]any{{"type": "rectangle", "x": 0, "y": 0}}}, "width and height"},
		{"board_add_shapes", map[string]any{"board": tf.board.ID, "shapes": []map[string]any{{"type": "arrow", "x": 5, "y": 5, "x2": 5, "y2": 5}}}, "apart"},
		{"board_add_shapes", map[string]any{"board": tf.board.ID, "shapes": []map[string]any{{"type": "text", "x": 0, "y": 0}}}, "needs text"},
		{"board_add_shapes", map[string]any{"board": tf.board.ID, "shapes": []map[string]any{{"type": "ellipse", "x": 0, "y": 0, "width": 9, "height": 9, "color": "red"}}}, "not #rgb"},
		{"board_add_shapes", map[string]any{"board": tf.board.ID, "shapes": []map[string]any{{"type": "ellipse", "x": 1e9, "y": 0, "width": 9, "height": 9}}}, "coordinates"},
	}
	for _, c := range cases {
		_, _, err := tf.run(t, c.name, c.in)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s %v: err %v, want %q", c.name, c.in, err, c.want)
		}
	}
	if len(tf.notes) != 0 {
		t.Errorf("a refused call changed the board: %+v", tf.notes)
	}
}

func TestOtherWorkspaceBoardIsNotFound(t *testing.T) {
	tf := newToolFixture(t)
	other, err := tf.svc.Create(CreateRequest{WorkspaceID: tf.other, Name: "Theirs"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := tf.run(t, "board_read", map[string]any{"board": other.ID}); err == nil {
		t.Error("read another workspace's board")
	}
	if _, _, err := tf.run(t, "board_add_note", map[string]any{"board": other.ID, "text": "x"}); err == nil {
		t.Error("added to another workspace's board")
	}
}

func TestShapesAndRead(t *testing.T) {
	tf := newToolFixture(t)
	tf.ownerDrawing(t)
	shapes := []map[string]any{
		{"type": "rectangle", "x": 0, "y": 0, "width": 160, "height": 60, "text": "Sensor", "fill": "#a5d8ff"},
		{"type": "arrow", "x": 160, "y": 30, "x2": 260, "y2": 30, "color": "#e03131"},
		{"type": "ellipse", "x": 260, "y": 0, "width": 120, "height": 60, "text": "Pi"},
		{"type": "text", "x": 0, "y": 80, "text": "data path"},
	}
	out, call, err := tf.run(t, "board_add_shapes", map[string]any{"board": tf.board.ID, "shapes": shapes})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(call.Preview, "arrow (160, 30) → (260, 30)") || !strings.Contains(call.Preview, `rectangle at (0, 0) 160×60 "Sensor"`) {
		t.Errorf("preview: %s", call.Preview)
	}
	if !strings.Contains(out, "4 shapes") {
		t.Errorf("result: %s", out)
	}
	byText := map[string]sceneElement{}
	var arrow, rect sceneElement
	for _, e := range tf.scene(t) {
		byText[e.Text] = e
		switch e.Type {
		case "arrow":
			arrow = e
		case "rectangle":
			if e.ID != "own" {
				rect = e
			}
		}
	}
	if len(arrow.Points) != 2 || arrow.Points[1] != [2]float64{100, 0} {
		t.Errorf("arrow points %v", arrow.Points)
	}
	if arrow.X-rect.X != 160 {
		t.Errorf("arrow starts %v right of the rectangle, want 160", arrow.X-rect.X)
	}
	if byText["Sensor"].Container != rect.ID {
		t.Errorf("label not bound to its rectangle")
	}

	read, _, err := tf.run(t, "board_read", map[string]any{"board": tf.board.ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`Board #1 "Plan"`, "rectangle own at (0, 0) size 100×80", `text "Sensor" in ` + rect.ID, "arrow", "(added by #7)", `text "added by #7"`} {
		if !strings.Contains(read, want) {
			t.Errorf("board_read lacks %q:\n%s", want, read)
		}
	}
	if strings.Index(read, "rectangle own") > strings.Index(read, "added by #7") {
		t.Errorf("board_read is not in drawing order:\n%s", read)
	}
	list, _, err := tf.run(t, "board_list", map[string]any{})
	if err != nil || !strings.Contains(list, `#1 Plan`) {
		t.Errorf("board_list: %q %v", list, err)
	}
}

func TestCreate(t *testing.T) {
	tf := newToolFixture(t)
	out, call, err := tf.run(t, "board_create", map[string]any{"name": "Wiring"})
	if err != nil || !strings.Contains(out, `"Wiring"`) || call.Effect != tool.Write {
		t.Fatalf("%q %v %+v", out, err, call)
	}
	if k := kinds(tf.seen); k[len(k)-1] != EventCreated {
		t.Errorf("events %v", k)
	}
}

// ---- images ----

func encode(t *testing.T, format string) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 30, 20))
	img.Set(1, 1, color.RGBA{255, 0, 0, 255})
	var b bytes.Buffer
	var err error
	switch format {
	case "png":
		err = imgpng.Encode(&b, img)
	case "jpeg":
		err = jpeg.Encode(&b, img, nil)
	case "gif":
		err = gif.Encode(&b, img, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// webp is a lossless WebP header of w×h, enough for sniffing and sizing.
func webp(w, h int) []byte {
	b := []byte("RIFF\x00\x00\x00\x00WEBPVP8L\x00\x00\x00\x00\x2f")
	var bits [4]byte
	binary.LittleEndian.PutUint32(bits[:], uint32(w-1)|uint32(h-1)<<14)
	b = append(b, bits[:]...)
	return append(b, make([]byte, 16)...)
}

func TestImageFormatsBySniffing(t *testing.T) {
	root := t.TempDir()
	files := map[string][]byte{
		"a.png": encode(t, "png"), "b.dat": encode(t, "jpeg"), "c.png": encode(t, "gif"), "d.webp": webp(300, 200),
	}
	want := map[string]string{"a.png": "image/png", "b.dat": "image/jpeg", "c.png": "image/gif", "d.webp": "image/webp"}
	for name, data := range files {
		os.WriteFile(filepath.Join(root, name), data, 0o600)
		_, st, err := readImage(root, "", name)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if st.MimeType != want[name] {
			t.Errorf("%s: %s, want %s", name, st.MimeType, want[name])
		}
		w, h := 30, 20
		if name == "d.webp" {
			w, h = 300, 200
		}
		if st.Width != w || st.Height != h {
			t.Errorf("%s: %d×%d, want %d×%d", name, st.Width, st.Height, w, h)
		}
	}
}

func TestImageRefusals(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(root, "fake.png"), []byte("<svg xmlns='http://www.w3.org/2000/svg'/>"), 0o600)
	os.WriteFile(filepath.Join(root, "empty.png"), nil, 0o600)
	os.WriteFile(filepath.Join(outside, "pic.png"), encode(t, "png"), 0o600)
	os.Symlink(filepath.Join(outside, "pic.png"), filepath.Join(root, "link.png"))
	big, _ := os.Create(filepath.Join(root, "big.png"))
	big.Write(encode(t, "png"))
	big.Truncate(maxImage + 1)
	big.Close()
	os.Mkdir(filepath.Join(root, "dir.png"), 0o700)
	cases := map[string]string{
		"fake.png":                        "not a PNG, JPEG, GIF or WebP",
		"empty.png":                       "is empty",
		"big.png":                         "at most 20 MB",
		filepath.Join(outside, "pic.png"): "outside your project",
		"link.png":                        "outside your project",
		"../" + filepath.Base(outside) + "/pic.png": "outside your project",
		"missing.png": "cannot find",
		"dir.png":     "not a file",
		"":            "needs path",
	}
	for path, want := range cases {
		_, _, err := readImage(root, "", path)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err %v, want %q", path, err, want)
		}
	}
	// A file in the agent's $TMPDIR is allowed, by absolute path.
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "shot.png"), encode(t, "png"), 0o600)
	if _, _, err := readImage(root, tmp, filepath.Join(tmp, "shot.png")); err != nil {
		t.Errorf("tmp image: %v", err)
	}
}

func TestAddImageAddsTheApprovedBytes(t *testing.T) {
	tf := newToolFixture(t)
	tf.ownerDrawing(t)
	first := encode(t, "png")
	path := filepath.Join(tf.root, "shots", "plot.png")
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, first, 0o600)
	raw, _ := json.Marshal(map[string]any{"board": tf.board.ID, "path": "shots/plot.png", "caption": "Latency"})
	c := llm.ToolCall{ID: "img1", Name: "board_add_image", Input: raw}
	call, err := tf.tools.Prepare(ws, tf.agent, c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(call.Preview, "plot.png (image/png, 30×20") || !strings.Contains(call.Preview, "Caption: Latency") {
		t.Errorf("preview: %s", call.Preview)
	}
	shown, err := tf.svc.StagedImage(ws, 7, "img1")
	if err != nil || !bytes.Equal(shown.Data, first) || shown.MimeType != "image/png" {
		t.Fatalf("staged image: %v", err)
	}
	if _, err := tf.svc.StagedImage(ws, 8, "img1"); err == nil {
		t.Error("another agent's staged image was found")
	}
	// The file changes while the call waits: the approved bytes are added.
	os.WriteFile(path, encode(t, "jpeg"), 0o600)
	if call, err = tf.tools.Prepare(ws, tf.agent, c); err != nil {
		t.Fatal(err)
	}
	if _, err := tf.tools.Execute(context.Background(), ws, tf.agent, call); err != nil {
		t.Fatal(err)
	}
	var img sceneElement
	for _, e := range tf.scene(t) {
		if e.Type == "image" {
			img = e
		}
	}
	got, err := tf.svc.Image(ws, tf.board.ID, img.FileID)
	if err != nil || !bytes.Equal(got.Data, first) {
		t.Fatalf("board image: %v (same bytes: %v)", err, bytes.Equal(got.Data, first))
	}
	if img.CustomData["xhaosName"] != "plot.png" {
		t.Errorf("image name %v", img.CustomData["xhaosName"])
	}
	if _, err := tf.svc.StagedImage(ws, 7, "img1"); err == nil {
		t.Error("the image stays staged after it was added")
	}
	read, _, _ := tf.run(t, "board_read", map[string]any{"board": tf.board.ID})
	if !strings.Contains(read, `image "plot.png" (image/png)`) || !strings.Contains(read, `"Latency"`) {
		t.Errorf("board_read:\n%s", read)
	}
}

func TestStagedImageSurvivesBoardDelete(t *testing.T) {
	tf := newToolFixture(t)
	data := encode(t, "png")
	os.WriteFile(filepath.Join(tf.root, "p.png"), data, 0o600)
	// Another board uses the same image, then is deleted while the call waits.
	other, _ := tf.svc.Create(CreateRequest{WorkspaceID: ws, Name: "Other"})
	if err := tf.svc.AddImage(ImageUpload{WorkspaceID: ws, BoardID: other.ID, FileID: "f1", Data: data}); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"board": tf.board.ID, "path": "p.png"})
	c := llm.ToolCall{ID: "img2", Name: "board_add_image", Input: raw}
	if _, err := tf.tools.Prepare(ws, tf.agent, c); err != nil {
		t.Fatal(err)
	}
	if err := tf.svc.Delete(ws, other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tf.svc.StagedImage(ws, 7, "img2"); err != nil {
		t.Fatalf("staged image lost: %v", err)
	}
}

func TestImageFromAgentTmp(t *testing.T) {
	tf := newToolFixture(t)
	tmp, err := sandbox.TempDir(tf.root)
	if err != nil {
		t.Skip("no sandbox temp dir here:", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmp) })
	path := filepath.Join(tmp, "chart.gif")
	os.WriteFile(path, encode(t, "gif"), 0o600)
	if _, _, err := tf.run(t, "board_add_image", map[string]any{"board": tf.board.ID, "path": path}); err != nil {
		t.Fatal(err)
	}
}

// ---- placement and order ----

func TestPlaceRightOrBelow(t *testing.T) {
	wide := []sceneElement{{ID: "a", X: 0, Y: 0, Width: 2000, Height: 100}}
	if x, y := place(wide, 200, 100); y < 100+gap || x != 0 {
		t.Errorf("a wide drawing: %v,%v, want below it", x, y)
	}
	tall := []sceneElement{{ID: "a", X: 0, Y: 0, Width: 100, Height: 2000}}
	if x, y := place(tall, 200, 100); x < 100+gap || y != 0 {
		t.Errorf("a tall drawing: %v,%v, want right of it", x, y)
	}
	deleted := []sceneElement{{ID: "a", X: 0, Y: 0, Width: 100, Height: 100, IsDeleted: true}}
	if x, y := place(deleted, 10, 10); x != 0 || y != 0 {
		t.Errorf("only deleted elements: %v,%v, want 0,0", x, y)
	}
	rotated := []sceneElement{{ID: "a", X: 0, Y: 0, Width: 400, Height: 10, Angle: 1.57}}
	x, y := place(rotated, 50, 50)
	if (box{x, y, x + 50, y + 50}).near(rotated[0].bounds(), 0) {
		t.Errorf("a rotated element is covered: %v,%v", x, y)
	}
}

func TestPlaceTiesGoRightAndRepeat(t *testing.T) {
	// A square drawing and a square addition: right of it and below it cost
	// the same (240×100 against 100×240); the tie goes to the right.
	square := []sceneElement{{ID: "a", X: 0, Y: 0, Width: 100, Height: 100}}
	x1, y1 := place(square, 100, 100)
	if x1 != 100+gap || y1 != 0 {
		t.Errorf("a tie: %v,%v, want right of the drawing at %v,0", x1, y1, 100+gap)
	}
	if x2, y2 := place(square, 100, 100); x2 != x1 || y2 != y1 {
		t.Errorf("the same board placed twice: %v,%v then %v,%v", x1, y1, x2, y2)
	}
	// With the right taken by an earlier addition, the next one still goes
	// beside the drawing (down the right column or below), never far off.
	taken := append(square, sceneElement{ID: "b", X: x1, Y: y1, Width: 100, Height: 100, CustomData: map[string]any{"xhaosAgent": float64(7)}})
	x3, y3 := place(taken, 100, 100)
	if !(x3 == 100+gap && y3 == 100+gap) && !(x3 == 0 && y3 == 100+gap) {
		t.Errorf("next to an earlier addition: %v,%v", x3, y3)
	}
	if x4, y4 := place(taken, 100, 100); x4 != x3 || y4 != y3 {
		t.Errorf("placed twice: %v,%v then %v,%v", x3, y3, x4, y4)
	}
}

func TestNextIndex(t *testing.T) {
	cases := map[string]string{"": "a0", "a0": "a1", "a0V": "a1", "a9": "aA", "az": "b00", "b0z": "b10", "Zz": "a0", "Yzz": "Z0"}
	for in, want := range cases {
		if got, ok := nextIndex(in); !ok || got != want {
			t.Errorf("nextIndex(%q) = %q %v, want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"zzzzzzzzzzzzzzzzzzzzzzzzzzz", "!", "a"} {
		if _, ok := nextIndex(bad); ok {
			t.Errorf("nextIndex(%q) is ok", bad)
		}
	}
}
