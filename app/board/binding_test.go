package board

import (
	"math"
	"testing"
)

func boxOf(kind, id string, x, y, w, h float64) shapeBox {
	return shapeBox{id: id, e: element{"type": kind}, x: x, y: y, w: w, h: h}
}

// The outline distance is signed the way Excalidraw's
// bindableElementBorderDistanceIfClose signs it: positive inside, negative
// outside, 0 on the outline.

func TestRectOutlineDist(t *testing.T) {
	b := boxOf("rectangle", "r", 0, 0, 100, 40)
	if d := b.dist(50, 20); math.Abs(d-20) > 1e-6 {
		t.Errorf("centre dist %v, want 20", d)
	}
	if d := b.dist(110, 20); math.Abs(d+10) > 1e-6 {
		t.Errorf("10 outside the right edge dist %v, want -10", d)
	}
	// 3px diagonally outside the top-right corner is 3√2 from the corner.
	if d := b.dist(103, -3); math.Abs(d+math.Hypot(3, 3)) > 1e-6 {
		t.Errorf("corner dist %v, want %v", d, -math.Hypot(3, 3))
	}
}

func TestEllipseOutlineDist(t *testing.T) {
	b := boxOf("ellipse", "e", 0, 0, 120, 60)
	if d := b.dist(60, 30); d <= 0 {
		t.Errorf("centre dist %v, want > 0", d)
	}
	if d := b.dist(0, 30); math.Abs(d) > 1e-6 {
		t.Errorf("left vertex dist %v, want 0", d)
	}
	if d := b.dist(140, 30); math.Abs(d+20) > 1e-6 {
		t.Errorf("20 right of the right vertex dist %v, want -20", d)
	}
	// The bounding-box corner (120, 60) is diagonally outside the ellipse and
	// must not count as near its outline, which the old bounding-box test did.
	if b.holds(120, 60) {
		t.Errorf("ellipse bbox corner should not hold")
	}
	if d := b.dist(120, 60); d > -bindDist {
		t.Errorf("ellipse bbox corner dist %v, want <= %v", d, -bindDist)
	}
}

func TestHoldsWithinBindDist(t *testing.T) {
	b := boxOf("rectangle", "r", 0, 0, 100, 40)
	if !b.holds(110, 20) { // 10 outside the edge
		t.Errorf("10 outside should hold")
	}
	if b.holds(116, 20) { // 16 outside the edge
		t.Errorf("16 outside should not hold")
	}
	// On and inside always hold.
	if !b.holds(100, 20) || !b.holds(50, 20) {
		t.Errorf("on the edge and inside should hold")
	}
}

func TestBoxAtClosestOutline(t *testing.T) {
	big := boxOf("rectangle", "big", 0, 0, 200, 200)
	small := boxOf("rectangle", "small", 10, 10, 20, 20)
	boxes := []shapeBox{big, small}
	// 1px outside the small shape's right edge, deep inside the big one: the
	// small shape's outline is closer.
	b, ok := boxAt(boxes, 31, 20)
	if !ok || b.id != "small" {
		t.Fatalf("boxAt = %v,%v, want small", b.id, ok)
	}
	// Far from the small shape, inside the big one: the big one wins.
	b, ok = boxAt(boxes, 150, 150)
	if !ok || b.id != "big" {
		t.Fatalf("boxAt = %v,%v, want big", b.id, ok)
	}
}

func TestBoxAtTiePrefersFront(t *testing.T) {
	a := boxOf("rectangle", "a", 0, 0, 100, 100)
	b := boxOf("rectangle", "b", 0, 0, 100, 100)
	got, ok := boxAt([]shapeBox{a, b}, 50, 50)
	if !ok || got.id != "b" {
		t.Fatalf("boxAt tie = %v,%v, want b (drawn later, in front)", got.id, ok)
	}
}
