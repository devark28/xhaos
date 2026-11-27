package board

import (
	"math"
	"testing"
)

func findByText(scene []sceneElement, text string) (box, label sceneElement) {
	for _, e := range scene {
		if e.Text == text {
			label = e
		}
	}
	for _, e := range scene {
		if e.ID == label.Container {
			box = e
		}
	}
	return box, label
}

func arrowOf(scene []sceneElement) sceneElement {
	for _, e := range scene {
		if e.Type == "arrow" && !e.IsDeleted {
			return e
		}
	}
	return sceneElement{}
}

func onOutline(t *testing.T, what string, p point, box sceneElement) {
	t.Helper()
	b := shapeBox{e: element{"type": box.Type}, x: box.X, y: box.Y, w: box.Width, h: box.Height}
	if d := b.dist(p.x, p.y); d > 0 || d < -1.5 {
		t.Errorf("%s %+v is %.2f from the outline of %s, want just outside it", what, p, d, box.ID)
	}
}

func ends(a sceneElement) (point, point) {
	n := len(a.Points) - 1
	return point{a.X + a.Points[0][0], a.Y + a.Points[0][1]}, point{a.X + a.Points[n][0], a.Y + a.Points[n][1]}
}

// The owner saw this: an agent moved box C with board_edit and the arrow into
// it stayed behind. A move must carry the bound arrow's end along.
func TestBoardEditMoveCarriesArrows(t *testing.T) {
	tf := newToolFixture(t)
	shapes := []map[string]any{
		{"type": "rectangle", "x": 0, "y": 0, "width": 160, "height": 60, "text": "B"},
		{"type": "rectangle", "x": 320, "y": 0, "width": 160, "height": 60, "text": "C"},
		{"type": "arrow", "x": 160, "y": 30, "x2": 316, "y2": 30},
	}
	if _, _, err := tf.run(t, "board_add_shapes", map[string]any{"board": tf.board.ID, "shapes": shapes}); err != nil {
		t.Fatal(err)
	}
	c, _ := findByText(tf.scene(t), "C")
	if _, _, err := tf.run(t, "board_edit", map[string]any{"board": tf.board.ID, "id": c.ID, "version": c.Version, "x": 520, "y": 200}); err != nil {
		t.Fatal(err)
	}
	scene := tf.scene(t)
	b, _ := findByText(scene, "B")
	c, _ = findByText(scene, "C")
	a := arrowOf(scene)
	start, end := ends(a)
	onOutline(t, "start", start, b)
	onOutline(t, "end", end, c)
	if a.EndBinding == nil || a.EndBinding.ElementID != c.ID {
		t.Errorf("end binding lost: %+v", a.EndBinding)
	}
}

// The owner saw this: attaching an arrow's end to box "End" with x2/y2 dropped
// End's label from its boundElements, and the page then discarded the label.
func TestArrowEndKeepsTargetLabel(t *testing.T) {
	tf := newToolFixture(t)
	shapes := []map[string]any{
		{"type": "rectangle", "x": 0, "y": 0, "width": 160, "height": 60, "text": "Start"},
		{"type": "rectangle", "x": 400, "y": 0, "width": 160, "height": 60, "text": "End"},
		{"type": "arrow", "x": 160, "y": 30, "x2": 260, "y2": 120},
	}
	if _, _, err := tf.run(t, "board_add_shapes", map[string]any{"board": tf.board.ID, "shapes": shapes}); err != nil {
		t.Fatal(err)
	}
	scene := tf.scene(t)
	end, label := findByText(scene, "End")
	a := arrowOf(scene)
	if _, _, err := tf.run(t, "board_edit", map[string]any{"board": tf.board.ID, "id": a.ID, "version": a.Version, "x2": end.X + 80, "y2": end.Y + 30}); err != nil {
		t.Fatal(err)
	}
	scene = tf.scene(t)
	end, label = findByText(scene, "End")
	hasLabel := false
	for _, be := range end.BoundElements {
		hasLabel = hasLabel || (be.ID == label.ID && be.Type == "text")
	}
	if !hasLabel || label.Container != end.ID {
		t.Errorf("End lost its label from boundElements: %+v", end.BoundElements)
	}
	if !boundTo(end.BoundElements, a.ID) {
		t.Errorf("End does not list the arrow: %+v", end.BoundElements)
	}
	_, tip := ends(arrowOf(scene))
	onOutline(t, "end", tip, end)
}

// The owner saw arrows leave boxes from corners and odd points. A bound arrow
// is aimed centre to centre and meets each outline on that line.
func TestAddedArrowAimsAtCentres(t *testing.T) {
	tf := newToolFixture(t)
	shapes := []map[string]any{
		{"type": "rectangle", "x": 0, "y": 0, "width": 100, "height": 60, "text": "A"},
		{"type": "rectangle", "x": 300, "y": 200, "width": 100, "height": 60, "text": "B"},
		{"type": "arrow", "x": 96, "y": 58, "x2": 304, "y2": 203}, // drawn near the corners
	}
	if _, _, err := tf.run(t, "board_add_shapes", map[string]any{"board": tf.board.ID, "shapes": shapes}); err != nil {
		t.Fatal(err)
	}
	scene := tf.scene(t)
	a, _ := findByText(scene, "A")
	b, _ := findByText(scene, "B")
	arrow := arrowOf(scene)
	start, end := ends(arrow)
	onOutline(t, "start", start, a)
	onOutline(t, "end", end, b)
	ca, cb := point{a.X + a.Width/2, a.Y + a.Height/2}, point{b.X + b.Width/2, b.Y + b.Height/2}
	line := cb.sub(ca)
	for name, p := range map[string]point{"start": start, "end": end} {
		if off := math.Abs(cross(line, p.sub(ca))) / line.len(); off > 1 {
			t.Errorf("%s %+v is %.2f off the centre-to-centre line", name, p, off)
		}
	}
}
