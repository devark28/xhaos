package board

import (
	"strings"
	"testing"
)

func TestShortestUniquePrefix(t *testing.T) {
	cases := []struct {
		full string
		all  []string
		want string
	}{
		{"abcde", []string{"abcde", "abxyz"}, "abcd"},      // share "abc", unique at 4
		{"abcde", []string{"abcde", "zzzzz"}, "abcd"},      // only id -> minimum
		{"abc", []string{"abc", "abcd"}, "abc"},            // shorter than the minimum
		{"abcdef", []string{"abcdef", "abcdeg"}, "abcdef"}, // share 5, unique at 6 -> full
		{"abcdef", []string{"abcdef"}, "abcd"},
	}
	for _, c := range cases {
		if got := shortestUniquePrefix(c.full, c.all); got != c.want {
			t.Errorf("shortestUniquePrefix(%q, %v) = %q, want %q", c.full, c.all, got, c.want)
		}
	}
}

func TestResolveID(t *testing.T) {
	candidates := []string{"abcd1", "abcd2", "other"}
	cases := []struct {
		in      string
		want    string
		wantErr string
	}{
		{"abcd1", "abcd1", ""},    // exact
		{"other", "other", ""},    // exact
		{"abcd", "", "ambiguous"}, // prefixes two
		{"abcd1x", "", "not found"},
		{"", "", "ambiguous"}, // the empty prefix matches every candidate
	}
	for _, c := range cases {
		got, err := resolveID(c.in, candidates)
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("resolveID(%q): err %v, want %q", c.in, err, c.wantErr)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("resolveID(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
}

// shapeBoard builds the fixture TestShapesAndRead does, then returns the
// gathered elements: the added rectangle, its "Sensor" label, the arrow, the
// ellipse, its "Pi" label, the "data path" text, the "added by #7" tag, and
// the board's one group id.
func shapeBoard(t *testing.T, tf *toolFixture) (rect, sensor, arrow, ellipse, pi, dataPath, tag sceneElement, group string) {
	t.Helper()
	tf.ownerDrawing(t)
	shapes := []map[string]any{
		{"type": "rectangle", "x": 0, "y": 0, "width": 160, "height": 60, "text": "Sensor", "fill": "#a5d8ff"},
		{"type": "arrow", "x": 160, "y": 30, "x2": 260, "y2": 30, "color": "#e03131"},
		{"type": "ellipse", "x": 260, "y": 0, "width": 120, "height": 60, "text": "Pi"},
		{"type": "text", "x": 0, "y": 80, "text": "data path"},
	}
	if _, _, err := tf.run(t, "board_add_shapes", map[string]any{"board": tf.board.ID, "shapes": shapes}); err != nil {
		t.Fatal(err)
	}
	byText := map[string]sceneElement{}
	for _, e := range tf.scene(t) {
		switch e.Type {
		case "arrow":
			arrow = e
		case "rectangle":
			if e.ID != "own" {
				rect = e
			}
		case "ellipse":
			ellipse = e
		}
		if e.Text != "" {
			byText[e.Text] = e
		}
		if len(e.GroupIDs) > 0 {
			group = e.GroupIDs[0]
		}
	}
	sensor, pi, dataPath, tag = byText["Sensor"], byText["Pi"], byText["data path"], byText["added by #7"]
	if rect.ID == "" || sensor.ID == "" || arrow.ID == "" || ellipse.ID == "" || pi.ID == "" || dataPath.ID == "" || tag.ID == "" || group == "" {
		t.Fatalf("fixture incomplete: %+v", byText)
	}
	return rect, sensor, arrow, ellipse, pi, dataPath, tag, group
}

func TestBoardReadShortIDs(t *testing.T) {
	tf := newToolFixture(t)
	rect, sensor, _, _, _, _, _, _ := shapeBoard(t, tf)
	short := shortIDs(tf.scene(t))
	// The owner's short id stays as-is; the generated ids are cut to a unique
	// prefix of at least 4 characters.
	if short["own"] != "own" {
		t.Errorf("own short id %q, want %q", short["own"], "own")
	}
	for _, id := range []string{rect.ID, sensor.ID} {
		got := short[id]
		if len(got) < minIDChars || got != id[:len(got)] {
			t.Errorf("short id of %q is %q", id, got)
		}
		if other, _ := resolveID(got, []string{id, "other"}); other != id {
			t.Errorf("short id %q does not resolve back to %q", got, id)
		}
	}
	// The prefixes are distinct.
	seen := map[string]string{}
	for id, s := range short {
		if prev, dup := seen[s]; dup {
			t.Errorf("short id %q is shared by %q and %q", s, prev, id)
		}
		seen[s] = id
	}
	// board_read prints the short id, and board_edit accepts it.
	read, _, err := tf.run(t, "board_read", map[string]any{"board": tf.board.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read, "rectangle "+short[rect.ID]) || strings.Contains(read, "rectangle "+rect.ID) {
		t.Errorf("board_read does not use the short id:\n%s", read)
	}
	if _, _, err := tf.run(t, "board_edit", map[string]any{"board": tf.board.ID, "id": short[rect.ID], "version": rect.Version, "x": 1.0, "y": 1.0}); err != nil {
		t.Errorf("board_edit did not accept the short id %q: %v", short[rect.ID], err)
	}
}

func TestBoardReadOutline(t *testing.T) {
	tf := newToolFixture(t)
	_, sensor, arrow, _, _, _, _, group := shapeBoard(t, tf)
	short, groups := shortIDs(tf.scene(t)), groupShortIDs(tf.scene(t))
	out, _, err := tf.run(t, "board_read", map[string]any{"board": tf.board.ID, "mode": "outline"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "(outline)") {
		t.Errorf("outline has no marker:\n%s", out)
	}
	if !strings.Contains(out, "text "+short[sensor.ID]+` text "Sensor" group `+groups[group]) {
		t.Errorf("outline lacks the Sensor line with its group:\n%s", out)
	}
	if !strings.Contains(out, "arrow "+short[arrow.ID]) {
		t.Errorf("outline lacks the arrow:\n%s", out)
	}
	if strings.Contains(out, "v1 at (") || strings.Contains(out, "(x right, y down)") {
		t.Errorf("outline shows geometry:\n%s", out)
	}
}

func TestBoardReadFilters(t *testing.T) {
	tf := newToolFixture(t)
	rect, sensor, arrow, _, _, dataPath, tag, group := shapeBoard(t, tf)
	short := shortIDs(tf.scene(t))

	ids, _, err := tf.run(t, "board_read", map[string]any{"board": tf.board.ID, "ids": []string{short[rect.ID]}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ids, "1 of 8 elements") || !strings.Contains(ids, "rectangle "+short[rect.ID]) || strings.Contains(ids, "arrow") {
		t.Errorf("ids filter:\n%s", ids)
	}

	byGroup, _, err := tf.run(t, "board_read", map[string]any{"board": tf.board.ID, "group": group})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(byGroup, "7 of 8 elements") || !strings.Contains(byGroup, `text "Sensor"`) || strings.Contains(byGroup, "rectangle own") {
		t.Errorf("group filter:\n%s", byGroup)
	}

	byType, _, err := tf.run(t, "board_read", map[string]any{"board": tf.board.ID, "type": "text"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"4 of 8 elements", `"added by #7"`, `"data path"`} {
		if !strings.Contains(byType, want) {
			t.Errorf("type filter lacks %q:\n%s", want, byType)
		}
	}
	for _, no := range []string{"arrow", "rectangle own"} {
		if strings.Contains(byType, no) {
			t.Errorf("type filter shows %q:\n%s", no, byType)
		}
	}

	region, _, err := tf.run(t, "board_read", map[string]any{"board": tf.board.ID, "x": 0.0, "y": 0.0, "width": 100.0, "height": 80.0})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(region, "1 of 8 elements") || !strings.Contains(region, "rectangle own") || strings.Contains(region, "arrow") {
		t.Errorf("region filter:\n%s", region)
	}

	// The default stays the whole board, in items mode, and lists every live
	// element the filters above referred to.
	full, _, err := tf.run(t, "board_read", map[string]any{"board": tf.board.ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{short[rect.ID], short[sensor.ID], short[arrow.ID], short[dataPath.ID], short[tag.ID]} {
		if !strings.Contains(full, want) {
			t.Errorf("full read lacks %q:\n%s", want, full)
		}
	}
}

func TestBoardReadErrors(t *testing.T) {
	tf := newToolFixture(t)
	_, _, _, _, _, _, _, _ = shapeBoard(t, tf)
	cases := []struct {
		in   map[string]any
		want string
	}{
		{map[string]any{"board": tf.board.ID, "mode": "everything"}, "mode is"},
		{map[string]any{"board": tf.board.ID, "x": 0.0}, "needs x, y, width and height"},
		{map[string]any{"board": tf.board.ID, "ids": []string{""}}, "ids cannot be empty"},
		{map[string]any{"board": tf.board.ID, "ids": []string{"nope"}}, "not found"},
		{map[string]any{"board": tf.board.ID, "group": "nope"}, "not found"},
	}
	for _, c := range cases {
		_, _, err := tf.run(t, "board_read", c.in)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: err %v, want %q", c.in, err, c.want)
		}
	}
	// Two ids that share their first four characters are ambiguous, but two
	// ids whose 4-character prefixes differ resolve fine.
	if got, err := resolveID("abcd", []string{"abcd1", "abcd2"}); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("ambiguous id: %q %v", got, err)
	}
}
