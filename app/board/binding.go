package board

import "math"

// An arrow an agent draws between shapes is bound to those shapes the way
// Excalidraw binds an arrow it snaps: the arrow holds startBinding/endBinding
// (the element it starts or ends on, with a focus and a gap) and each shape
// lists the arrow in its boundElements. Excalidraw re-derives a bound arrow's
// end from the binding, so the arrow follows when the shape moves or resizes.
//
// focus is where on the shape the arrow aims, from 0 at its centre to ±1 at a
// corner (values between run along the diagonal toward a corner); gap is how
// far the arrow's end sits off the shape's outline. Both are computed the way
// Excalidraw computes them for an arrow snapped where the agent drew it, so
// the arrow keeps pointing at the same spot when the shape moves.

// bindTol is how far outside a shape an arrow's end may sit and still be
// snapped to it.
const bindTol = 2.0

// binding is an arrow's startBinding or endBinding as Excalidraw stores it.
type binding struct {
	ElementID string  `json:"elementId"`
	Focus     float64 `json:"focus"`
	Gap       float64 `json:"gap"`
}

// boundElement is one entry of a shape's boundElements.
type boundElement struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

// shapeBox is a rectangle or ellipse an arrow may bind to.
type shapeBox struct {
	id   string
	e    element
	x, y float64 // top-left
	w, h float64
}

// holds reports whether the point is on or inside the shape (within bindTol).
func (b shapeBox) holds(x, y float64) bool {
	return x >= b.x-bindTol && x <= b.x+b.w+bindTol && y >= b.y-bindTol && y <= b.y+b.h+bindTol
}

// boxAt is the first shape the point falls on, if any.
func boxAt(boxes []shapeBox, x, y float64) (shapeBox, bool) {
	for _, b := range boxes {
		if b.holds(x, y) {
			return b, true
		}
	}
	return shapeBox{}, false
}

// bindArrow binds an arrow's start and end to the shapes they fall on, if any.
func (a *addition) bindArrow(e element, x0, y0, x1, y1 float64, boxes []shapeBox) {
	id := e["id"].(string)
	if b, ok := boxAt(boxes, x0, y0); ok {
		e["startBinding"] = map[string]any{"elementId": b.id, "focus": focusOf(point{x1, y1}, point{x0, y0}, b), "gap": 1}
		b.bind(id)
	}
	if b, ok := boxAt(boxes, x1, y1); ok {
		e["endBinding"] = map[string]any{"elementId": b.id, "focus": focusOf(point{x0, y0}, point{x1, y1}, b), "gap": 1}
		b.bind(id)
	}
}

// bind appends the arrow to the shape's boundElements once.
func (b shapeBox) bind(arrowID string) {
	var list []map[string]any
	if cur, ok := b.e["boundElements"].([]map[string]any); ok {
		list = cur
	}
	for _, be := range list {
		if be["id"] == arrowID {
			return
		}
	}
	b.e["boundElements"] = append(list, map[string]any{"type": "arrow", "id": arrowID})
}

// ---- focus -------------------------------------------------------------------

// point is a 2D point in a board's coordinates.
type point struct{ x, y float64 }

func (p point) sub(q point) point { return point{p.x - q.x, p.y - q.y} }
func (p point) add(q point) point { return point{p.x + q.x, p.y + q.y} }
func (p point) scale(s float64) point {
	return point{p.x * s, p.y * s}
}
func (p point) len() float64 { return math.Hypot(p.x, p.y) }

func cross(a, b point) float64 { return a.x*b.y - b.x*a.y }

func signum(v float64) float64 {
	switch {
	case v > 0:
		return 1
	case v < 0:
		return -1
	}
	return 0
}

// seg is a line segment between two points.
type seg [2]point

// intersect is the point where two segments cross, if any.
func (s seg) intersect(o seg) (point, bool) {
	// Two line equations A·x + B·y = C solved for their crossing, exactly as
	// Excalidraw's linesIntersectAt does.
	a1, b1 := s[1].y-s[0].y, s[0].x-s[1].x
	a2, b2 := o[1].y-o[0].y, o[0].x-o[1].x
	d := a1*b2 - a2*b1
	if d == 0 {
		return point{}, false
	}
	c1 := a1*s[0].x + b1*s[0].y
	c2 := a2*o[0].x + b2*o[0].y
	p := point{(c1*b2 - c2*b1) / d, (a1*c2 - a2*c1) / d}
	if !s.on(p) || !o.on(p) {
		return point{}, false
	}
	return p, true
}

// on reports whether p lies on the segment: its perpendicular projection onto
// the segment's line falls between the ends (within rounding).
func (s seg) on(p point) bool {
	c, dd := s[1].x-s[0].x, s[1].y-s[0].y
	lenSq := c*c + dd*dd
	if lenSq == 0 {
		return p == s[0]
	}
	t := ((p.x-s[0].x)*c + (p.y-s[0].y)*dd) / lenSq
	return t >= -1e-9 && t <= 1+1e-9
}

// focusOf returns the focus Excalidraw stores for an arrow whose end sits at b
// and whose other end is a, on a rectangle or ellipse whose top-left is box
// x,y and size w×h (angle 0). It mirrors Excalidraw's determineFocusDistance.
func focusOf(a, b point, box shapeBox) float64 {
	o := point{box.x, box.y}
	return focusDistance(a.sub(o), b.sub(o), box.w, box.h)
}

// focusDistance is focusOf with the shape at the origin: a is the arrow's
// other end and b its end, in the shape's own coordinates, w×h its size.
func focusDistance(a, b point, w, h float64) float64 {
	if a == b {
		return 0
	}
	center := point{w / 2, h / 2}
	sign := -signum(cross(b.sub(a), b.sub(center)))
	// The arrow's line, continued from its end through the shape for
	// max(2w, 2h), as Excalidraw draws it.
	dir := b.sub(a)
	l := dir.len()
	if l == 0 {
		return 0
	}
	dir = dir.scale(1 / l)
	interceptor := seg{b, b.add(dir.scale(2 * max(w, h)))}
	// The shape's two diagonals, extended two sizes out, as Excalidraw's are.
	diagonals := [2]seg{
		{point{-w, -h}, point{2 * w, 2 * h}},
		{point{2 * w, -h}, point{-w, 2 * h}},
	}
	halfDiag := math.Hypot(w, h) / 2
	best, first := 0.0, true
	for _, d := range diagonals {
		p, ok := interceptor.intersect(d)
		if !ok {
			continue
		}
		r := sign * center.sub(p).len() / halfDiag
		if first || math.Abs(r) < math.Abs(best) {
			best, first = r, false
		}
	}
	return best
}
