package board

import (
	"fmt"
	"strings"
)

// layoutCheck reports, after a board write, the live board's problems an
// agent cannot see: bound labels that still overflow their shape, and shapes
// or texts that overlap one another. It is empty when the board is clean.
// els are the board's live elements, in any order.
func layoutCheck(els []sceneElement) string {
	short := shortIDs(els)
	byID := make(map[string]sceneElement, len(els))
	for _, e := range els {
		byID[e.ID] = e
	}
	var issues []string

	// Labels that still overflow their container: growing only the height
	// cannot fix a box too narrow for a single word.
	for _, e := range els {
		if e.Type != "text" || e.Container == "" {
			continue
		}
		c, ok := byID[e.Container]
		if !ok || c.IsDeleted {
			continue
		}
		if w, h := overflowOf(e, c); w > 0 || h > 0 {
			issues = append(issues, fmt.Sprintf("label %s overflows its %s %s by %.0f wide and %.0f tall", short[e.ID], c.Type, short[c.ID], w, h))
		}
	}

	// Overlaps among shapes, texts and images. Arrows are connectors and a
	// bound label sits inside its own container, so neither is an overlap.
	if len(els) <= overlapLimit {
		for i := 0; i < len(els); i++ {
			a := els[i]
			if a.IsDeleted || !overlapCheckable(a) {
				continue
			}
			for j := i + 1; j < len(els); j++ {
				b := els[j]
				if b.IsDeleted || !overlapCheckable(b) {
					continue
				}
				if a.Container == b.ID || b.Container == a.ID {
					continue
				}
				if w, h := overlapOf(a, b); w > 0 && h > 0 {
					issues = append(issues, fmt.Sprintf("%s %s overlaps %s %s by %.0f×%.0f", a.Type, short[a.ID], b.Type, short[b.ID], w, h))
				}
			}
		}
	}

	if len(issues) == 0 {
		return ""
	}
	return strings.Join(issues, "; ")
}

// overlapLimit bounds the pairwise overlap pass; a board past it only gets the
// label check (the pairwise pass would cost O(n²)).
const overlapLimit = 2000

// overlapCheckable reports whether an element takes part in the overlap pass.
func overlapCheckable(e sceneElement) bool {
	switch e.Type {
	case "rectangle", "ellipse", "text", "image":
		return true
	}
	return false
}

// overlapOf is how much a and b's bounding boxes intrude on one another: a
// dimension is 0 when they only touch or are apart.
func overlapOf(a, b sceneElement) (w, h float64) {
	ba, bb := a.bounds(), b.bounds()
	return min(ba.x1, bb.x1) - max(ba.x0, bb.x0), min(ba.y1, bb.y1) - max(ba.y0, bb.y0)
}

// overflowOf is how far a bound label's box exceeds its container's box.
func overflowOf(label, container sceneElement) (w, h float64) {
	lb, cb := label.bounds(), container.bounds()
	return max(0, (lb.x1-lb.x0)-(cb.x1-cb.x0)), max(0, (lb.y1-lb.y0)-(cb.y1-cb.y0))
}
