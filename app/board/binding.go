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

// bindDist is how far outside a shape an arrow's end may sit and still be
// snapped to it: Excalidraw's maxBindingDistance at zoom 1, the smallest its
// zoom-dependent value takes (BASE_BINDING_DISTANCE = max(BASE_BINDING_GAP, 15)
// = 15, clamped to 15..30). Boards have no zoom, so we use the zoom-1 value.
const bindDist = 15.0

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

// dist is the signed distance from the point to the shape's outline, measured
// the way Excalidraw's bindableElementBorderDistanceIfClose measures it:
// positive inside, negative outside, 0 on the outline.
func (b shapeBox) dist(x, y float64) float64 {
	p := point{x - b.x, y - b.y}
	if b.e.str("type") == "ellipse" {
		d := ellipseDist(p.x, p.y, b.w, b.h)
		if ellipseInside(p.x, p.y, b.w, b.h) {
			return d
		}
		return -d
	}
	d := rectOutlineDist(p, b.w, b.h)
	if p.x >= 0 && p.x <= b.w && p.y >= 0 && p.y <= b.h {
		return d
	}
	return -d
}

// holds reports whether the point is on or inside the shape, or within bindDist
// of its outline (Excalidraw's isPointInBindingArea).
func (b shapeBox) holds(x, y float64) bool {
	return b.dist(x, y) > -bindDist
}

// boxAt is the shape the point falls on, if any: the one whose outline it is
// closest to, as Excalidraw's getHoveredElementForBinding chooses (ties go to
// the shape drawn later, which is in front). It skips Excalidraw's
// nested-overlap rule, which never applies to a board's non-overlapping shapes.
func boxAt(boxes []shapeBox, x, y float64) (shapeBox, bool) {
	var best shapeBox
	bestAbs, found := math.Inf(1), false
	for _, b := range boxes {
		if !b.holds(x, y) {
			continue
		}
		// boxes are sorted by index ascending; <= keeps the later (front) one
		// on an equal distance.
		if abs := math.Abs(b.dist(x, y)); abs <= bestAbs {
			best, bestAbs, found = b, abs, true
		}
	}
	return best, found
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
	// A shape drawn in this call holds []map[string]any; one loaded from the
	// board holds []any (JSON). Keep every existing entry either way: dropping
	// them unbinds the shape's own label.
	var list []any
	switch cur := b.e["boundElements"].(type) {
	case []any:
		list = cur
	case []map[string]any:
		for _, m := range cur {
			list = append(list, m)
		}
	}
	for _, item := range list {
		if m, ok := item.(map[string]any); ok && m["id"] == arrowID {
			return
		}
	}
	b.e["boundElements"] = append(list, map[string]any{"type": "arrow", "id": arrowID})
}

// ---- distance to a shape's outline --------------------------------------------

// rectOutlineDist is the shortest distance from a point in a rectangle's own
// coordinates (top-left at 0,0, size w×h) to its outline. As in Excalidraw's
// distanceToRectanguloidElement, the outline is its four sides: for a
// sharp-cornered rectangle (roundness type 3) each side's segment also covers
// the corner, so the four sides suffice.
func rectOutlineDist(p point, w, h float64) float64 {
	sides := [4]seg{
		{point{0, 0}, point{w, 0}},
		{point{w, 0}, point{w, h}},
		{point{w, h}, point{0, h}},
		{point{0, h}, point{0, 0}},
	}
	d := math.Inf(1)
	for _, s := range sides {
		d = min(d, s.distTo(p))
	}
	return d
}

// ellipseInside reports whether a point in an ellipse's own coordinates
// (top-left at 0,0, size w×h) is on or inside its outline.
func ellipseInside(px, py, w, h float64) bool {
	a, b := w/2, h/2
	nx, ny := (px-a)/a, (py-b)/b
	return nx*nx+ny*ny <= 1
}

// ellipseDist is the shortest distance from a point in an ellipse's own
// coordinates to its outline, exactly Excalidraw's ellipseDistanceFromPoint
// (three iterations of its Newton step, then the distance to the closest
// point found).
func ellipseDist(px, py, w, h float64) float64 {
	a, b := w/2, h/2
	tx0, ty0 := px-a, py-b
	if a == b { // a circle is exact, and avoids the 0/0 below at its centre
		return math.Abs(math.Hypot(tx0, ty0) - a)
	}
	ax, ay := math.Abs(tx0), math.Abs(ty0)
	tx, ty := 0.707, 0.707
	for i := 0; i < 3; i++ {
		x, y := a*tx, b*ty
		ex := ((a*a - b*b) * tx * tx * tx) / a
		ey := ((b*b - a*a) * ty * ty * ty) / b
		rx, ry := x-ex, y-ey
		qx, qy := ax-ex, ay-ey
		r := math.Hypot(ry, rx)
		q := math.Hypot(qy, qx)
		if q == 0 {
			break
		}
		tx = clamp01(((qx*r)/q + ex) / a)
		ty = clamp01(((qy*r)/q + ey) / b)
		t := math.Hypot(ty, tx)
		tx /= t
		ty /= t
	}
	mx, my := a*tx, b*ty
	if tx0 < 0 {
		mx = -mx
	}
	if ty0 < 0 {
		my = -my
	}
	return math.Hypot(tx0-mx, ty0-my)
}

func clamp01(v float64) float64 { return min(1, max(0, v)) }

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

// distTo is the shortest distance from point p to the segment.
func (s seg) distTo(p point) float64 {
	dx, dy := s[1].x-s[0].x, s[1].y-s[0].y
	l2 := dx*dx + dy*dy
	if l2 == 0 {
		return p.sub(s[0]).len()
	}
	t := clamp01(((p.x-s[0].x)*dx + (p.y-s[0].y)*dy) / l2)
	return math.Hypot(p.x-(s[0].x+t*dx), p.y-(s[0].y+t*dy))
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

// ---- routing a bound arrow ----------------------------------------------------

// routeArrow re-aims a bound arrow's ends at the shapes they are bound to, as
// Excalidraw draws an arrow it binds with focus 0: each bound end sits on its
// shape's outline (plus the binding's gap) on the line from the shape's centre
// toward the other end, which is the other shape's centre when that end is
// bound too. It is what keeps an arrow attached when an agent moves or
// resizes a shape (the page does the same itself when the owner drags one),
// and what makes an arrow an agent draws start and end on the outlines
// instead of wherever its coordinates fell. Unbound ends stay where they are;
// an arrow with middle points keeps them and aims each end at its neighbour.
func routeArrow(arrow element, shapes map[string]element) {
	abs, ok := absArrowPoints(arrow)
	if !ok || len(abs) < 2 {
		return
	}
	sb, sgap := boundBox(arrow, "startBinding", shapes)
	eb, egap := boundBox(arrow, "endBinding", shapes)
	if sb == nil && eb == nil {
		return
	}
	n := len(abs)
	startRef, endRef := abs[1], abs[n-2]
	if n == 2 {
		if eb != nil {
			startRef = eb.center()
		}
		if sb != nil {
			endRef = sb.center()
		}
	}
	if sb != nil {
		abs[0] = sb.edgeToward(startRef, sgap)
		arrow["startBinding"].(map[string]any)["focus"] = 0.0
	}
	if eb != nil {
		abs[n-1] = eb.edgeToward(endRef, egap)
		arrow["endBinding"].(map[string]any)["focus"] = 0.0
	}
	setArrowPoints(arrow, abs)
}

// routeBound re-routes every arrow bound to a shape that changed, marking each
// arrow so its version is checked and bumped with the shape.
func routeBound(shapeIDs []string, loaded map[string]element, mark func(string) element) {
	done := map[string]bool{}
	for _, sid := range shapeIDs {
		s := loaded[sid]
		if s == nil {
			continue
		}
		if t := s.str("type"); t != "rectangle" && t != "ellipse" {
			continue
		}
		for _, aid := range boundArrowIDs(s) {
			if done[aid] {
				continue
			}
			if a := mark(aid); a != nil {
				if deleted, _ := a["isDeleted"].(bool); !deleted {
					routeArrow(a, loaded)
				}
				done[aid] = true
			}
		}
	}
}

// boundBox is the live shape an arrow's binding names, and the binding's gap.
func boundBox(arrow element, key string, shapes map[string]element) (*shapeBox, float64) {
	b, ok := arrow[key].(map[string]any)
	if !ok {
		return nil, 0
	}
	id, _ := b["elementId"].(string)
	e := shapes[id]
	if e == nil {
		return nil, 0
	}
	if deleted, _ := e["isDeleted"].(bool); deleted {
		return nil, 0
	}
	gap, _ := b["gap"].(float64)
	if g, ok := b["gap"].(int); ok {
		gap = float64(g)
	}
	return &shapeBox{id: id, e: e, x: e.num("x"), y: e.num("y"), w: e.num("width"), h: e.num("height")}, gap
}

func (b shapeBox) center() point { return point{b.x + b.w/2, b.y + b.h/2} }

// edgeToward is where the line from the shape's centre toward p leaves the
// shape's outline, moved gap further out along that line.
func (b shapeBox) edgeToward(p point, gap float64) point {
	c := b.center()
	d := p.sub(c)
	if d.len() == 0 {
		d = point{1, 0}
	}
	var t float64
	if b.e.str("type") == "ellipse" {
		a, bb := b.w/2, b.h/2
		t = 1 / math.Sqrt((d.x/a)*(d.x/a)+(d.y/bb)*(d.y/bb))
	} else {
		tx, ty := math.Inf(1), math.Inf(1)
		if d.x != 0 {
			tx = (b.w / 2) / math.Abs(d.x)
		}
		if d.y != 0 {
			ty = (b.h / 2) / math.Abs(d.y)
		}
		t = min(tx, ty)
	}
	return c.add(d.scale(t)).add(d.scale(gap / d.len()))
}

// absArrowPoints is an arrow's points in board coordinates.
func absArrowPoints(arrow element) ([]point, bool) {
	pts, ok := arrowPoints(arrow)
	if !ok {
		return nil, false
	}
	x, y := arrow.num("x"), arrow.num("y")
	abs := make([]point, len(pts))
	for i, p := range pts {
		abs[i] = point{x + p[0], y + p[1]}
	}
	return abs, true
}

// setArrowPoints stores board-coordinate points on an arrow: its anchor is the
// first point, its points are offsets from it, and its size their extent.
func setArrowPoints(arrow element, abs []point) {
	o := abs[0]
	pts := make([][2]float64, len(abs))
	minX, minY, maxX, maxY := 0.0, 0.0, 0.0, 0.0
	for i, p := range abs {
		dx, dy := p.x-o.x, p.y-o.y
		pts[i] = [2]float64{dx, dy}
		minX, minY, maxX, maxY = min(minX, dx), min(minY, dy), max(maxX, dx), max(maxY, dy)
	}
	arrow["x"], arrow["y"] = o.x, o.y
	arrow["points"] = pts
	arrow["width"], arrow["height"] = maxX-minX, maxY-minY
}
