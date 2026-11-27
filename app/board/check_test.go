package board

import (
	"strings"
	"testing"
)

// layoutCheck is the server's eye on the board after a write: it reports the
// problems an agent cannot see from board_read's geometry. These tests cover
// the pure check and the tool paths that append it.

func TestLayoutCheckClean(t *testing.T) {
	rect := sceneElement{ID: "rect1", Type: "rectangle", X: 0, Y: 0, Width: 100, Height: 40}
	label := sceneElement{ID: "label1", Type: "text", X: 5, Y: 5, Width: 90, Height: 25, Container: "rect1"}
	if got := layoutCheck([]sceneElement{rect, label}); got != "" {
		t.Errorf("clean board: %q", got)
	}
}

func TestLayoutCheckOverflow(t *testing.T) {
	rect := sceneElement{ID: "rect1", Type: "rectangle", X: 0, Y: 0, Width: 100, Height: 40}
	label := sceneElement{ID: "label1", Type: "text", X: 5, Y: 5, Width: 120, Height: 60, Container: "rect1"}
	got := layoutCheck([]sceneElement{rect, label})
	for _, want := range []string{"overflows", "rectangle", "20 wide and 20 tall"} {
		if !strings.Contains(got, want) {
			t.Errorf("overflow: %q lacks %q", got, want)
		}
	}
}

func TestLayoutCheckOverlap(t *testing.T) {
	a := sceneElement{ID: "aaa1", Type: "rectangle", X: 0, Y: 0, Width: 100, Height: 100}
	b := sceneElement{ID: "bbb1", Type: "rectangle", X: 50, Y: 50, Width: 100, Height: 100}
	got := layoutCheck([]sceneElement{a, b})
	for _, want := range []string{"overlaps", "50×50"} {
		if !strings.Contains(got, want) {
			t.Errorf("overlap: %q lacks %q", got, want)
		}
	}
}

func TestLayoutCheckSkipsArrowsAndBoundLabels(t *testing.T) {
	// An arrow crossing a rectangle is a connector, not an overlap; a bound
	// label sits inside its own container, so it is not an overlap either.
	arrow := sceneElement{ID: "arrow1", Type: "arrow", X: 0, Y: 0, Points: [][2]float64{{0, 0}, {200, 200}}}
	rect := sceneElement{ID: "rect1", Type: "rectangle", X: 50, Y: 50, Width: 100, Height: 100}
	label := sceneElement{ID: "label1", Type: "text", X: 55, Y: 55, Width: 90, Height: 90, Container: "rect1"}
	if got := layoutCheck([]sceneElement{arrow, rect, label}); got != "" {
		t.Errorf("arrow crossing + bound label: %q", got)
	}
}

func TestFitLabelText(t *testing.T) {
	box := element{"width": 60.0, "height": 40.0}
	wrapped, height := fitLabelText(box, "supercalifragilisticexpialidocious", 20.0)
	if !strings.Contains(wrapped, "\n") {
		t.Errorf("a long word is not wrapped: %q", wrapped)
	}
	if height <= 40 {
		t.Errorf("the box did not grow to fit the wrapped label: %g", height)
	}
	short, h := fitLabelText(box, "ok", 20.0)
	if short != "ok" || h != 40 {
		t.Errorf("a short label: %q %g, want %q %g", short, h, "ok", 40.0)
	}
}

func TestCharWidths(t *testing.T) {
	if charWidthOf(5) <= charWidthOf(2) {
		t.Errorf("Excalifont should be wider than Helvetica: %g vs %g", charWidthOf(5), charWidthOf(2))
	}
	if lineHeightOf(2) >= lineHeightOf(5) {
		t.Errorf("Helvetica line height should be tighter: %g vs %g", lineHeightOf(2), lineHeightOf(5))
	}
}

// board_edit of a shape's label wraps it to the box and grows the box, so the
// label always fits inside (no overflow warning).
func TestBoardEditWrapsAndGrowsLabel(t *testing.T) {
	tf := newToolFixture(t)
	// One labelled rectangle alone, so growing it cannot collide with anything.
	if _, _, err := tf.run(t, "board_add_shapes", map[string]any{"board": tf.board.ID, "shapes": []map[string]any{
		{"type": "rectangle", "x": 0, "y": 0, "width": 160, "height": 60, "text": "Sensor"},
	}}); err != nil {
		t.Fatal(err)
	}
	var rect sceneElement
	for _, e := range tf.scene(t) {
		if e.Type == "rectangle" {
			rect = e
		}
	}
	if rect.ID == "" {
		t.Fatal("no rectangle added")
	}
	long := "a very long label that wraps over several lines"
	out, _, err := tf.run(t, "board_edit", map[string]any{"board": tf.board.ID, "id": rect.ID, "version": rect.Version, "text": long})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "label set to") {
		t.Errorf("result: %q", out)
	}
	if strings.Contains(out, "Layout:") {
		t.Errorf("a wrapped label should fit: %q", out)
	}
	got := sceneElementByID(t, tf, rect.ID)
	if got == nil || got.Height <= 60 {
		t.Fatalf("container did not grow to fit the wrapped label: %+v", got)
	}
	for _, e := range tf.scene(t) {
		if e.Container == rect.ID && !strings.Contains(e.Text, "\n") {
			t.Errorf("label not wrapped: %q", e.Text)
		}
	}
}

// board_edit of a shape's width to narrower than its label leaves the label
// overflowing: the result reports it, so the agent can fix what it cannot see.
func TestBoardEditResizeWarnsOnOverflow(t *testing.T) {
	tf := newToolFixture(t)
	rect, _, _, _, _, _, _, _ := shapeBoard(t, tf)
	out, _, err := tf.run(t, "board_edit", map[string]any{"board": tf.board.ID, "id": rect.ID, "version": rect.Version, "width": 40.0})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Layout:") || !strings.Contains(out, "overflows") {
		t.Errorf("a too-narrow box should warn: %q", out)
	}
}

// board_add_shapes reports when the shapes an agent gave overlap one another.
func TestAddShapesWarnsOnOverlap(t *testing.T) {
	tf := newToolFixture(t)
	shapes := []map[string]any{
		{"type": "rectangle", "x": 0, "y": 0, "width": 100, "height": 100},
		{"type": "rectangle", "x": 50, "y": 50, "width": 100, "height": 100},
	}
	out, _, err := tf.run(t, "board_add_shapes", map[string]any{"board": tf.board.ID, "shapes": shapes})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Layout:") || !strings.Contains(out, "overlaps") {
		t.Errorf("overlapping shapes should warn: %q", out)
	}
}
