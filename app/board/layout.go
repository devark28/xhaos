package board

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// What an agent adds to a board is built here as Excalidraw elements: an
// addition is a group of elements in its own coordinates (top-left at 0,0),
// tagged "added by #N", then moved into free space beside the owner's drawing
// (place) and given scene order after everything the board holds (nextIndex).

const (
	gap          = 40.0 // between an addition and what is already there
	tagSize      = 14.0 // font size of the "added by #N" tag
	noteSize     = 20.0
	captionSize  = 16.0
	lineHeight   = 1.25
	charWidth    = 0.6 // of the font size: a generous average, so text never runs past its box
	notePad      = 5.0 // Excalidraw's padding around text in a container
	noteColumns  = 48  // characters per line of a note
	maxImageSide = 640.0
	fontFamily   = 5 // Excalifont, Excalidraw's default hand-drawn font
	ink          = "#1e1e1e"
	tagInk       = "#868e96"
	noteFill     = "#ffec99"
)

// element is one Excalidraw element as JSON fields.
type element map[string]any

// addition is a group of elements an agent adds in one call.
type addition struct {
	agent    int64
	group    string
	elements []element
}

func newAddition(agentID int64) *addition {
	a := &addition{agent: agentID, group: newID()}
	a.text(0, 0, tagLine(agentID), tagSize, tagInk)
	return a
}

func tagLine(agentID int64) string { return "added by #" + strconv.FormatInt(agentID, 10) }

// newID is a random element or group ID, like Excalidraw's.
func newID() string {
	b := make([]byte, 15)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// random is a positive 31-bit integer (seeds and version nonces).
func random() int64 {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return int64(binary.BigEndian.Uint32(b[:])>>1) + 1
}

func (a *addition) base(kind string, x, y, w, h float64) element {
	e := element{
		"id": newID(), "type": kind, "x": x, "y": y, "width": w, "height": h, "angle": 0,
		"strokeColor": ink, "backgroundColor": "transparent", "fillStyle": "solid",
		"strokeWidth": 2, "strokeStyle": "solid", "roughness": 1, "opacity": 100,
		"groupIds": []string{a.group}, "frameId": nil, "roundness": nil,
		"seed": random(), "version": 1, "versionNonce": random(), "isDeleted": false,
		"boundElements": nil, "updated": time.Now().UnixMilli(), "link": nil, "locked": false, "index": nil,
		"customData": map[string]any{"xhaosAgent": a.agent},
	}
	a.elements = append(a.elements, e)
	return e
}

// textSize estimates a text's box: Excalidraw measures it in the browser, the
// server can only guess, so the guess errs on the wide side.
func textSize(text string, size float64) (w, h float64) {
	lines := strings.Split(text, "\n")
	longest := 0
	for _, l := range lines {
		longest = max(longest, utf8.RuneCountInString(l))
	}
	return math.Ceil(max(1, float64(longest)) * size * charWidth), math.Ceil(float64(len(lines)) * size * lineHeight)
}

func (a *addition) text(x, y float64, text string, size float64, color string) element {
	w, h := textSize(text, size)
	e := a.base("text", x, y, w, h)
	e["strokeColor"] = color
	e["fontSize"], e["fontFamily"], e["lineHeight"] = size, fontFamily, lineHeight
	e["text"], e["originalText"] = text, text
	e["textAlign"], e["verticalAlign"] = "left", "top"
	e["containerId"], e["autoResize"] = nil, true
	return e
}

// label puts text inside a container (rectangle or ellipse), bound to it as
// Excalidraw binds text it edits in a shape.
func (a *addition) label(container element, text string, size float64, align, valign string) {
	cx, cy := container["x"].(float64), container["y"].(float64)
	cw, ch := container["width"].(float64), container["height"].(float64)
	w, h := textSize(text, size)
	x := cx + notePad
	if align == "center" {
		x = cx + (cw-w)/2
	}
	y := cy + notePad
	if valign == "middle" {
		y = cy + (ch-h)/2
	}
	t := a.text(x, y, text, size, ink)
	t["textAlign"], t["verticalAlign"] = align, valign
	t["containerId"], t["autoResize"] = container["id"], false
	container["boundElements"] = []map[string]any{{"type": "text", "id": t["id"]}}
}

// wrap breaks text into lines of at most width characters, at spaces where
// it can.
func wrap(text string, width int) string {
	var out []string
	for _, para := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line := ""
		for _, word := range strings.Fields(para) {
			for utf8.RuneCountInString(word) > width { // a word longer than a line
				r := []rune(word)
				if line != "" {
					out, line = append(out, line), ""
				}
				out, word = append(out, string(r[:width])), string(r[width:])
			}
			switch {
			case line == "":
				line = word
			case utf8.RuneCountInString(line)+1+utf8.RuneCountInString(word) <= width:
				line += " " + word
			default:
				out, line = append(out, line), word
			}
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

const contentTop = tagSize*lineHeight + 8 // content starts below the tag

// note adds a yellow sticky note holding text.
func (a *addition) note(text string) {
	text = wrap(text, noteColumns)
	w, h := textSize(text, noteSize)
	box := a.base("rectangle", 0, contentTop, w+2*notePad+16, h+2*notePad+16)
	box["backgroundColor"], box["strokeColor"], box["roundness"] = noteFill, "#f08c00", map[string]any{"type": 3}
	a.label(box, text, noteSize, "left", "top")
}

// image adds a picture, scaled down to fit maxImageSide, with an optional
// caption below it. name is kept for board_read.
func (a *addition) image(fileID, name string, width, height int, caption string) {
	w, h := float64(width), float64(height)
	if s := maxImageSide / max(w, h); s < 1 {
		w, h = math.Round(w*s), math.Round(h*s)
	}
	img := a.base("image", 0, contentTop, max(w, 1), max(h, 1))
	img["strokeColor"] = "transparent"
	img["fileId"], img["status"], img["scale"], img["crop"] = fileID, "saved", []int{1, 1}, nil
	img["customData"] = map[string]any{"xhaosAgent": a.agent, "xhaosName": name}
	if caption != "" {
		a.text(0, contentTop+h+8, wrap(caption, 60), captionSize, ink)
	}
}

// Shape is one shape for board_add_shapes, in the addition's own coordinates.
type Shape struct {
	Type   string  `json:"type"` // rectangle, ellipse, arrow, text
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
	X2     float64 `json:"x2"` // an arrow's end
	Y2     float64 `json:"y2"`
	Text   string  `json:"text"`  // a label inside a rectangle or ellipse, or a text's words
	Color  string  `json:"color"` // stroke, as #rgb or #rrggbb
	Fill   string  `json:"fill"`  // background of a rectangle or ellipse
}

// shapes adds the shapes, moved so their top-left corner sits below the tag.
func (a *addition) shapes(list []Shape) {
	minX, minY := math.Inf(1), math.Inf(1)
	for _, s := range list {
		minX, minY = min(minX, s.X), min(minY, s.Y)
		if s.Type == "arrow" {
			minX, minY = min(minX, s.X2), min(minY, s.Y2)
		}
	}
	dx, dy := -minX, contentTop-minY
	for _, s := range list {
		x, y := s.X+dx, s.Y+dy
		var e element
		switch s.Type {
		case "rectangle", "ellipse":
			e = a.base(s.Type, x, y, s.Width, s.Height)
			if s.Type == "rectangle" {
				e["roundness"] = map[string]any{"type": 3}
			} else {
				e["roundness"] = map[string]any{"type": 2}
			}
			if s.Fill != "" {
				e["backgroundColor"] = s.Fill
			}
		case "arrow":
			px, py := s.X2-s.X, s.Y2-s.Y
			e = a.base("arrow", x, y, math.Abs(px), math.Abs(py))
			e["points"] = [][2]float64{{0, 0}, {px, py}}
			e["lastCommittedPoint"], e["startBinding"], e["endBinding"] = nil, nil, nil
			e["startArrowhead"], e["endArrowhead"], e["elbowed"] = nil, "arrow", false
			e["roundness"] = map[string]any{"type": 2}
		case "text":
			e = a.text(x, y, s.Text, noteSize, ink)
		}
		if s.Color != "" {
			e["strokeColor"] = s.Color
		}
		if s.Text != "" && (s.Type == "rectangle" || s.Type == "ellipse") {
			cols := max(4, int((s.Width-2*notePad)/(noteSize*charWidth)))
			a.label(e, wrap(s.Text, cols), noteSize, "center", "middle")
		}
	}
}

// ---- where an addition goes ----------------------------------------------------

// box is an axis-aligned bounding box.
type box struct{ x0, y0, x1, y1 float64 }

func (b box) union(o box) box {
	return box{min(b.x0, o.x0), min(b.y0, o.y0), max(b.x1, o.x1), max(b.y1, o.y1)}
}
func (b box) area() float64 { return (b.x1 - b.x0) * (b.y1 - b.y0) }

// near reports whether b comes within margin of o.
func (b box) near(o box, margin float64) bool {
	return b.x0 < o.x1+margin && o.x0 < b.x1+margin && b.y0 < o.y1+margin && o.y0 < b.y1+margin
}

// sceneElement is what placement reads of a stored element.
type sceneElement struct {
	ID         string         `json:"id"`
	Type       string         `json:"type"`
	X          float64        `json:"x"`
	Y          float64        `json:"y"`
	Width      float64        `json:"width"`
	Height     float64        `json:"height"`
	Angle      float64        `json:"angle"`
	Points     [][2]float64   `json:"points"`
	IsDeleted  bool           `json:"isDeleted"`
	Index      *string        `json:"index"`
	Text       string         `json:"text"`
	FileID     string         `json:"fileId"`
	GroupIDs   []string       `json:"groupIds"`
	Container  string         `json:"containerId"`
	CustomData map[string]any `json:"customData"`
}

// agentOf is the agent that added an element (0: the owner drew it).
func (e sceneElement) agentOf() int64 {
	if v, ok := e.CustomData["xhaosAgent"].(float64); ok {
		return int64(v)
	}
	return 0
}

// bounds is the element's box; a rotated element counts with the circle
// around it.
func (e sceneElement) bounds() box {
	b := box{e.X, e.Y, e.X + e.Width, e.Y + e.Height}
	if len(e.Points) > 0 {
		b = box{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
		for _, p := range e.Points {
			b = b.union(box{e.X + p[0], e.Y + p[1], e.X + p[0], e.Y + p[1]})
		}
	}
	if e.Angle != 0 {
		cx, cy := (b.x0+b.x1)/2, (b.y0+b.y1)/2
		r := math.Hypot(b.x1-b.x0, b.y1-b.y0) / 2
		b = box{cx - r, cy - r, cx + r, cy + r}
	}
	return b
}

// place picks the top-left corner for an addition of size w×h. It goes to
// the right of the owner's drawing (a column, filled from the top) or below
// it (a row, filled from the left), never inside the owner's drawing's box
// and never within gap of anything on the board; of the free spots it takes
// the one that keeps the board most compact. Elements are the board's live
// ones.
func place(elements []sceneElement, w, h float64) (float64, float64) {
	var all, owner []box
	for _, e := range elements {
		if e.IsDeleted {
			continue
		}
		b := e.bounds()
		if math.IsNaN(b.x0+b.y0+b.x1+b.y1) || math.IsInf(b.x0+b.y0+b.x1+b.y1, 0) {
			continue
		}
		all = append(all, b)
		if e.agentOf() == 0 {
			owner = append(owner, b)
		}
	}
	if len(all) == 0 {
		return 0, 0
	}
	whole := all[0]
	for _, b := range all[1:] {
		whole = whole.union(b)
	}
	drawing := whole // with nothing of the owner's, agents' additions take its place
	if len(owner) > 0 {
		drawing = owner[0]
		for _, b := range owner[1:] {
			drawing = drawing.union(b)
		}
	}
	free := func(x, y float64) bool {
		c := box{x, y, x + w, y + h}
		for _, b := range all {
			if c.near(b, gap/2) {
				return false
			}
		}
		return true
	}
	type spot struct{ x, y float64 }
	// Candidates in order: right of the drawing at its top, below it at its
	// left, then further down the right column and along the row below.
	right, below := drawing.x1+gap, drawing.y1+gap
	spots := []spot{{right, drawing.y0}, {drawing.x0, below}}
	for _, b := range all {
		if b.x1 > right-gap && b.x0 < right+w+gap {
			spots = append(spots, spot{right, b.y1 + gap})
		}
	}
	for _, b := range all {
		if b.y1 > below-gap && b.y0 < below+h+gap {
			spots = append(spots, spot{b.x1 + gap, below})
		}
	}
	// The board stays compact: the spot whose board has the shortest longer
	// side wins, then the one that adds least area. A later candidate wins
	// only when strictly better, so ties go to the earlier one (right first).
	cost := func(s spot) (float64, float64) {
		u := whole.union(box{s.x, s.y, s.x + w, s.y + h})
		return max(u.x1-u.x0, u.y1-u.y0), u.area()
	}
	var best *spot
	var side, area float64
	for i := range spots {
		s := spots[i]
		if !free(s.x, s.y) {
			continue
		}
		if sd, ar := cost(s); best == nil || sd < side || sd == side && ar < area {
			best, side, area = &spots[i], sd, ar
		}
	}
	if best == nil {
		return drawing.x0, whole.y1 + gap // last resort: below everything, always free
	}
	return best.x, best.y
}

// size is an addition's extent in its own coordinates.
func (a *addition) size() (w, h float64) {
	b := box{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	for _, e := range a.elements {
		b = b.union(a.bounds(e))
	}
	return b.x1 - b.x0, b.y1 - b.y0
}

func (a *addition) bounds(e element) box {
	raw, _ := json.Marshal(e)
	var s sceneElement
	_ = json.Unmarshal(raw, &s)
	return s.bounds()
}

// moveTo shifts the addition so its own origin sits at x, y.
func (a *addition) moveTo(x, y float64) {
	for _, e := range a.elements {
		e["x"], e["y"] = e["x"].(float64)+x, e["y"].(float64)+y
	}
}

// ---- scene order ----------------------------------------------------------------

// Excalidraw orders a scene by each element's fractional index (a string key
// from rocicorp/fractional-indexing, base 62). New elements go after the
// highest key on the board, so every page puts them on top in the same order.
const digits = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// integerLength is the length of a key's integer part, from its head.
func integerLength(head byte) int {
	switch {
	case head >= 'a' && head <= 'z':
		return int(head-'a') + 2
	case head >= 'A' && head <= 'Z':
		return int('Z'-head) + 2
	}
	return 0
}

// nextIndex is the key after key (fractional-indexing's generateKeyBetween(key,
// null)): its integer part, incremented. "" (no key yet) gives "a0". ok is
// false for a key it cannot follow; the elements then go without one, and
// the page assigns them.
func nextIndex(key string) (string, bool) {
	if key == "" {
		return "a0", true
	}
	n := integerLength(key[0])
	if n == 0 || len(key) < n {
		return "", false
	}
	head, digs := key[0], []byte(key[1:n])
	for _, d := range digs {
		if strings.IndexByte(digits, d) < 0 {
			return "", false
		}
	}
	for i := len(digs) - 1; i >= 0; i-- {
		at := strings.IndexByte(digits, digs[i]) + 1
		if at < len(digits) {
			digs[i] = digits[at]
			return string(head) + string(digs), true
		}
		digs[i] = digits[0]
	}
	// Every digit carried: the integer part grows (or, past "z", gives up).
	switch {
	case head == 'Z':
		return "a0", true
	case head == 'z':
		return "", false
	}
	head++
	if head > 'a' {
		digs = append(digs, digits[0])
	} else {
		digs = digs[:len(digs)-1]
	}
	return string(head) + string(digs), true
}

// order gives the addition's elements keys after last, in their order (a
// shape's label after its shape, as Excalidraw requires).
func (a *addition) order(last string) {
	key := last
	for _, e := range a.elements {
		next, ok := nextIndex(key)
		if !ok {
			return
		}
		e["index"], key = next, next
	}
}
