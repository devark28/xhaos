package board

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/devark28/xhaos/app/apperr"
	"github.com/devark28/xhaos/app/sqlitedb"
)

// An agent's board_edit changes an element the owner or an agent drew, by its
// ID. It goes through the policy gate like a write, except deletion, which
// always asks (see policy.EffectDelete). The edit is a real Excalidraw edit:
// the element keeps its ID and its version bumps by one with a fresh nonce,
// so it merges with concurrent page saves by the same rule as everything else
// (mergeElements). A stale version — the element moved on since the agent
// read it — is refused and reported with the element's current state.

// EditOp is one change board_edit makes to an element. Zero-valued fields
// mean "leave it alone"; Text, X, Y, Width and Height are pointers so a
// call can tell "not given" from "given as empty/zero".
type EditOp struct {
	Text   *string  // the element's (or its label's) new text
	X, Y   *float64 // move the element (an arrow's start) to this anchor point
	X2, Y2 *float64 // move an arrow's end point
	Width  *float64 // resize a rectangle or ellipse
	Height *float64
	Color  string // stroke colour (#rgb or #rrggbb)
	Fill   string // background of a rectangle or ellipse
	Delete bool   // delete the element (and its bound label)
}

// EditResult is what a board_edit call did.
type EditResult struct {
	Board   Summary
	ID      string
	Changes []string
}

// loadElement returns a board element's raw JSON and its stored header.
func loadElement(q sqlitedb.Querier, boardID int64, id string) (json.RawMessage, header, error) {
	var v, n int64
	var data string
	err := q.QueryRow(`SELECT version, version_nonce, data FROM board_elements WHERE board_id = ? AND element_id = ?`, boardID, id).Scan(&v, &n, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, header{}, fmt.Errorf("%w: element %s", apperr.NotFound, id)
	}
	if err != nil {
		return nil, header{}, err
	}
	return json.RawMessage(data), header{ID: id, Version: v, VersionNonce: n}, nil
}

// loadEdit reads the element the agent wants to change and what the edit also
// touches (a container's bound labels, or the container a text label is bound
// to), checks the version the agent read, and applies the edit to copies. It
// returns the changed elements with their versions bumped and fresh nonces,
// the headers of everything it loaded, and short phrases describing the
// change. It writes nothing: it is the dry run Prepare shows, and the read
// half of editForAgent.
func loadEdit(q sqlitedb.Querier, boardID int64, id string, v int64, op EditOp) (map[string]element, map[string]header, []string, error) {
	target, head, err := loadElement(q, boardID, id)
	if err != nil {
		return nil, nil, nil, err
	}
	if head.Version != v {
		return nil, nil, nil, staleEdit(id, v, head, target)
	}
	loaded := map[string]element{}
	heads := map[string]header{id: head}
	var te element
	if err := json.Unmarshal(target, &te); err != nil {
		return nil, nil, nil, err
	}
	loaded[id] = te
	load := func(id string) error {
		if _, ok := loaded[id]; ok {
			return nil
		}
		raw, h, err := loadElement(q, boardID, id)
		if err != nil {
			return err
		}
		var e element
		if err := json.Unmarshal(raw, &e); err != nil {
			return err
		}
		loaded[id], heads[id] = e, h
		return nil
	}
	for _, id := range boundTextIDs(te) {
		if err := load(id); err != nil {
			return nil, nil, nil, err
		}
	}
	if cid := containerID(te); cid != "" {
		if err := load(cid); err != nil {
			return nil, nil, nil, err
		}
	}
	// Arrows bound to the element at either end are deleted with it when it is
	// deleted, and the shapes they are bound to at the other end lose them from
	// their boundElements. Load the arrows and those shapes for their version
	// checks.
	for _, arrowID := range boundArrowIDs(te) {
		if err := load(arrowID); err != nil {
			return nil, nil, nil, err
		}
		for _, other := range arrowBoundIDs(loaded[arrowID]) {
			if err := load(other); err != nil {
				return nil, nil, nil, err
			}
		}
	}
	// An arrow edit rebinds its endpoints to the shapes they land on, so load
	// the board's live shapes for that.
	if te.str("type") == "arrow" {
		if err := loadLiveShapes(q, boardID, loaded, heads); err != nil {
			return nil, nil, nil, err
		}
	}
	changed, changes, err := applyEdit(id, loaded, heads, op)
	if err != nil {
		return nil, nil, nil, err
	}
	return changed, heads, changes, nil
}

// loadLiveShapes loads the board's live rectangles and ellipses — the shapes
// an arrow can bind to — into loaded and heads, so an arrow edit can rebind
// its endpoints to whatever they land on. Elements already loaded are kept.
func loadLiveShapes(q sqlitedb.Querier, boardID int64, loaded map[string]element, heads map[string]header) error {
	rows, err := q.Query(`SELECT element_id, version, version_nonce, data FROM board_elements WHERE board_id = ?`, boardID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var v, n int64
		var data string
		if err := rows.Scan(&id, &v, &n, &data); err != nil {
			return err
		}
		if _, ok := loaded[id]; ok {
			continue
		}
		var e element
		if err := json.Unmarshal([]byte(data), &e); err != nil {
			return err
		}
		if e.str("type") != "rectangle" && e.str("type") != "ellipse" {
			continue
		}
		if deleted, _ := e["isDeleted"].(bool); deleted {
			continue
		}
		loaded[id], heads[id] = e, header{ID: id, Version: v, VersionNonce: n}
	}
	return rows.Err()
}

// editForAgent loads the element the agent read, checks its version, applies
// the edit, and merges it in exactly as a page's save is.
func (s *Service) editForAgent(ws string, agentID int64, boardID int64, eid string, v int64, op EditOp) (EditResult, error) {
	if err := checkWorkspace(ws); err != nil {
		return EditResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return EditResult{}, err
	}
	defer tx.Rollback()
	board, err := find(tx, ws, boardID)
	if err != nil {
		return EditResult{}, err
	}
	changed, heads, changes, err := loadEdit(tx, boardID, eid, v, op)
	if err != nil {
		return EditResult{}, err
	}
	raws := make([]json.RawMessage, 0, len(changed))
	rawHeads := make([]header, 0, len(changed))
	for id, e := range changed {
		raw, err := json.Marshal(e)
		if err != nil {
			return EditResult{}, err
		}
		raws = append(raws, raw)
		h := heads[id]
		rawHeads = append(rawHeads, header{ID: id, Version: h.Version + 1, VersionNonce: e["versionNonce"].(int64)})
	}
	accepted, _, err := mergeElements(tx, boardID, raws, rawHeads)
	if err != nil {
		return EditResult{}, err
	}
	at := s.now()
	if _, err := tx.Exec(`UPDATE boards SET updated_at = ? WHERE id = ?`, sqlitedb.Stamp(at), boardID); err != nil {
		return EditResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return EditResult{}, err
	}
	s.notify(ws, Notice{Kind: NoticeSaved, WorkspaceID: ws, ID: boardID, Source: fmt.Sprintf("agent-%d", agentID),
		UpdatedAt: sqlitedb.ParseStamp(sqlitedb.Stamp(at)), Elements: accepted})
	return EditResult{Board: board, ID: eid, Changes: changes}, nil
}

// staleEdit reports that the element moved on since the agent read it, with
// its current state so the agent can read it again and retry.
func staleEdit(id string, read int64, head header, raw json.RawMessage) error {
	var e sceneElement
	if json.Unmarshal(raw, &e) == nil {
		bb := e.bounds()
		line := fmt.Sprintf("- %s %s at (%.0f, %.0f) size %.0f×%.0f", e.Type, e.ID, bb.x0, bb.y0, bb.x1-bb.x0, bb.y1-bb.y0)
		if e.Text != "" {
			line += fmt.Sprintf(" text %q", clipText(e.Text, 300))
		}
		if e.Container != "" {
			line += fmt.Sprintf(" in %s", e.Container)
		}
		return fmt.Errorf("element %s changed since you read it (you read v%d, it is now v%d): %s v%d; board_read it again and retry", id, read, head.Version, line, head.Version)
	}
	return fmt.Errorf("element %s changed since you read it (you read v%d, it is now v%d); board_read it again and retry", id, read, head.Version)
}

// ---- the edit itself ---------------------------------------------------------

// applyEdit mutates loaded (keyed by element ID) per op and returns the
// changed elements and short phrases describing each change. It is pure over
// the maps: callers load the target and anything bound to it first.
func applyEdit(targetID string, loaded map[string]element, heads map[string]header, op EditOp) (map[string]element, []string, error) {
	tgt := loaded[targetID]
	kind := tgt.str("type")
	changed := map[string]element{}
	var changes []string

	mark := func(id string) element {
		e, ok := loaded[id]
		if !ok {
			return nil
		}
		if _, done := changed[id]; !done {
			changed[id] = e
		}
		return e
	}

	if op.Delete {
		arrows := boundArrowIDs(tgt)
		switch {
		case len(boundTextIDs(tgt)) > 0:
			mark(targetID)["isDeleted"] = true
			for _, id := range boundTextIDs(tgt) {
				if e := mark(id); e != nil {
					e["isDeleted"] = true
				}
			}
			changes = append(changes, deletePhrase(kind, true, len(arrows)))
		case containerID(tgt) != "":
			mark(targetID)["isDeleted"] = true
			if cont := mark(containerID(tgt)); cont != nil {
				unbind(cont, targetID)
			}
			changes = append(changes, "deleted its label")
		default:
			mark(targetID)["isDeleted"] = true
			changes = append(changes, deletePhrase(kind, false, len(arrows)))
		}
		// Arrows bound to the deleted element at either end are deleted with
		// it, and the shapes they are bound to at the other end lose them from
		// their boundElements. The arrows and those shapes were loaded in
		// loadEdit, so their versions are checked and bumped like the deleted
		// element's.
		for _, id := range arrows {
			if arrow := mark(id); arrow != nil {
				arrow["isDeleted"] = true
				for _, other := range arrowBoundIDs(arrow) {
					if other == targetID {
						continue
					}
					if e := mark(other); e != nil {
						unbind(e, id)
					}
				}
			}
			unbind(tgt, id)
		}
		bump(changed, heads)
		return changed, changes, nil
	}

	if op.X != nil || op.Y != nil {
		if op.X == nil || op.Y == nil {
			return nil, nil, errors.New("a move needs both x and y")
		}
		dx, dy := *op.X-tgt.num("x"), *op.Y-tgt.num("y")
		mark(targetID)
		tgt["x"], tgt["y"] = *op.X, *op.Y
		for _, id := range boundTextIDs(tgt) {
			if e := mark(id); e != nil {
				e["x"], e["y"] = e.num("x")+dx, e.num("y")+dy
			}
		}
		changes = append(changes, fmt.Sprintf("moved to (%.0f, %.0f)", *op.X, *op.Y))
	}

	if op.X2 != nil || op.Y2 != nil {
		if kind != "arrow" {
			return nil, nil, fmt.Errorf("x2, y2 move an arrow's end, not %s", kindName(kind))
		}
		if op.X2 == nil || op.Y2 == nil {
			return nil, nil, errors.New("a move of an arrow's end needs both x2 and y2")
		}
		mark(targetID)
		if err := moveArrowEnd(tgt, targetID, loaded, *op.X2, *op.Y2, mark); err != nil {
			return nil, nil, err
		}
		changes = append(changes, fmt.Sprintf("arrow end moved to (%.0f, %.0f)", *op.X2, *op.Y2))
	}

	if op.Width != nil || op.Height != nil {
		if kind != "rectangle" && kind != "ellipse" {
			return nil, nil, fmt.Errorf("width and height resize a rectangle or ellipse, not %s", kindName(kind))
		}
		w, h := tgt.num("width"), tgt.num("height")
		if op.Width != nil {
			w = *op.Width
		}
		if op.Height != nil {
			h = *op.Height
		}
		if !(w > 0 && h > 0 && w <= maxSide && h <= maxSide) {
			return nil, nil, fmt.Errorf("a %s needs width and height between 0 and %.0f", kind, maxSide)
		}
		mark(targetID)
		tgt["width"], tgt["height"] = w, h
		for _, id := range boundTextIDs(tgt) {
			if e := mark(id); e != nil {
				repositionLabel(e, tgt)
			}
		}
		changes = append(changes, fmt.Sprintf("resized to %.0f×%.0f", w, h))
	}

	if op.Color != "" {
		mark(targetID)["strokeColor"] = op.Color
		changes = append(changes, "recoloured to "+op.Color)
	}
	if op.Fill != "" {
		if kind != "rectangle" && kind != "ellipse" {
			return nil, nil, fmt.Errorf("a fill colours a rectangle or ellipse, not %s", kindName(kind))
		}
		mark(targetID)["backgroundColor"] = op.Fill
		changes = append(changes, "filled "+op.Fill)
	}

	if op.Text != nil {
		text := *op.Text
		if strings.TrimSpace(text) == "" {
			return nil, nil, errors.New("text is empty")
		}
		if utf8len(text) > maxNote {
			return nil, nil, fmt.Errorf("text is at most %d characters", maxNote)
		}
		switch {
		case len(boundTextIDs(tgt)) > 0:
			id := boundTextIDs(tgt)[0]
			label := mark(id)
			mark(targetID) // the container: setLabelText grows it
			setLabelText(label, tgt, text)
			changes = append(changes, fmt.Sprintf("label set to %q", clipText(text, 40)))
		case kind == "text":
			mark(targetID)
			if cid := containerID(tgt); cid != "" {
				if cont := loaded[cid]; cont != nil {
					mark(cid)
					setLabelText(tgt, cont, text)
				} else {
					setText(tgt, text)
				}
			} else {
				setText(tgt, text)
			}
			changes = append(changes, fmt.Sprintf("text set to %q", clipText(text, 40)))
		default:
			return nil, nil, fmt.Errorf("text changes a text element or the label of a rectangle or ellipse, not %s", kindName(kind))
		}
	}

	if len(changed) == 0 {
		return nil, nil, errors.New("nothing to change")
	}
	bump(changed, heads)
	return changed, changes, nil
}

func bump(changed map[string]element, heads map[string]header) {
	for id, e := range changed {
		h := heads[id]
		e["version"] = h.Version + 1
		e["versionNonce"] = random()
	}
}

// moveArrowEnd moves an arrow's end point to (x2, y2) and rebinds it: the end
// binds to the live shape it now lands on (if any), and unbinds from the one
// it left. mark is applyEdit's mark, so a shape whose boundElements change is
// version-checked and bumped along with the arrow.
func moveArrowEnd(arrow element, arrowID string, loaded map[string]element, x2, y2 float64, mark func(string) element) error {
	pts, ok := arrowPoints(arrow)
	if !ok || len(pts) < 2 {
		return errors.New("an arrow needs points to move its end")
	}
	x, y := arrow.num("x"), arrow.num("y")
	prev := point{x + pts[len(pts)-2][0], y + pts[len(pts)-2][1]}
	if x2 == prev.x && y2 == prev.y {
		return errors.New("an arrow's end needs x2, y2 apart from its previous point")
	}
	var oldID string
	if eb, ok := arrow["endBinding"].(map[string]any); ok {
		oldID, _ = eb["elementId"].(string)
	}
	pts[len(pts)-1] = [2]float64{x2 - x, y2 - y}
	arrow["points"] = pts

	var newID string
	var newBox shapeBox
	if b, ok := boxAt(liveBoxes(loaded), x2, y2); ok {
		newID, newBox = b.id, b
		arrow["endBinding"] = map[string]any{"elementId": b.id, "focus": focusOf(prev, point{x2, y2}, b), "gap": 1}
	} else {
		arrow["endBinding"] = nil
	}
	if oldID != newID {
		if oldID != "" {
			if old := mark(oldID); old != nil {
				unbind(old, arrowID)
			}
		}
		if newID != "" {
			mark(newID)
			newBox.bind(arrowID)
		}
	}
	return nil
}

// arrowPoints returns an arrow's points (offsets from its x, y anchor) and
// whether it has any. The last point is its end.
func arrowPoints(e element) ([][2]float64, bool) {
	raw, ok := e["points"].([]any)
	if !ok {
		return nil, false
	}
	pts := make([][2]float64, 0, len(raw))
	for _, item := range raw {
		pair, ok := item.([]any)
		if !ok || len(pair) != 2 {
			return nil, false
		}
		x, okx := pair[0].(float64)
		y, oky := pair[1].(float64)
		if !okx || !oky {
			return nil, false
		}
		pts = append(pts, [2]float64{x, y})
	}
	return pts, true
}

// liveBoxes collects the live rectangles and ellipses in loaded, in drawing
// order, as the shapes an arrow's end can bind to.
func liveBoxes(loaded map[string]element) []shapeBox {
	var boxes []shapeBox
	for id, e := range loaded {
		t := e.str("type")
		if t != "rectangle" && t != "ellipse" {
			continue
		}
		if deleted, _ := e["isDeleted"].(bool); deleted {
			continue
		}
		boxes = append(boxes, shapeBox{id: id, e: e, x: e.num("x"), y: e.num("y"), w: e.num("width"), h: e.num("height")})
	}
	key := func(e element) string {
		if s := e.str("index"); s != "" {
			return s
		}
		return "\U0010FFFF" // no index sorts last, as sortByIndex does
	}
	sort.SliceStable(boxes, func(i, j int) bool { return key(boxes[i].e) < key(boxes[j].e) })
	return boxes
}

// setText writes a text element's words and resizes its box to fit.
func setText(e element, text string) {
	setTextSize(e, text, labelFontSize(e))
}

// setTextSize writes a text element's words and resizes its box to fit, in
// the element's own font.
func setTextSize(e element, text string, size float64) {
	w, h := textSizeIn(text, size, e.num("fontFamily"))
	e["text"], e["originalText"] = text, text
	e["width"], e["height"] = w, h
}

// labelFontSize is the font size a text or label element renders at.
func labelFontSize(e element) float64 {
	if size := e.num("fontSize"); size > 0 {
		return size
	}
	return noteSize
}

// setLabelText writes a bound label's words, wrapped to its container's
// width, resizes the label, and grows the container's height to fit (its
// width stays). Both elements must be marked changed already.
func setLabelText(label, container element, text string) {
	size := labelFontSize(label)
	wrapped, height := fitLabelText(container, text, size)
	setTextSize(label, wrapped, size)
	container["height"] = height
	repositionLabel(label, container)
}

// repositionLabel keeps a bound label inside its container the way layout.go
// places it: padded at the edge, or centred.
func repositionLabel(label, container element) {
	cx, cy := container.num("x"), container.num("y")
	cw, ch := container.num("width"), container.num("height")
	w, h := label.num("width"), label.num("height")
	x := cx + notePad
	if label.str("textAlign") == "center" {
		x = cx + (cw-w)/2
	}
	y := cy + notePad
	if label.str("verticalAlign") == "middle" {
		y = cy + (ch-h)/2
	}
	label["x"], label["y"] = x, y
}

// boundTextIDs lists the bound text elements a shape holds, in order.
func boundTextIDs(e element) []string {
	raw, ok := e["boundElements"].([]any)
	if !ok {
		return nil
	}
	var ids []string
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := m["type"].(string); t == "text" {
			if id, _ := m["id"].(string); id != "" {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// boundArrowIDs lists the arrows bound to a shape, in order.
func boundArrowIDs(e element) []string {
	raw, ok := e["boundElements"].([]any)
	if !ok {
		return nil
	}
	var ids []string
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := m["type"].(string); t == "arrow" {
			if id, _ := m["id"].(string); id != "" {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// arrowBoundIDs lists the element ids an arrow's startBinding and endBinding
// point at, in order, skipping any empty ids.
func arrowBoundIDs(arrow element) []string {
	var ids []string
	for _, k := range []string{"startBinding", "endBinding"} {
		if b, ok := arrow[k].(map[string]any); ok {
			if id, _ := b["elementId"].(string); id != "" {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// deletePhrase is the "deleted …" change phrase for deleting an element of the
// given kind, mentioning its label when it has one and the arrows bound to it.
func deletePhrase(kind string, label bool, arrows int) string {
	s := "deleted " + kindName(kind)
	switch {
	case label && arrows > 0:
		s += ", its label"
	case label:
		s += " and its label"
	}
	if arrows == 1 {
		s += " and 1 arrow"
	} else if arrows > 1 {
		s += fmt.Sprintf(" and %d arrows", arrows)
	}
	return s
}

// unbind removes the binding to text id from a container's boundElements.
func unbind(container element, id string) {
	raw, ok := container["boundElements"].([]any)
	if !ok {
		return
	}
	var kept []any
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok && m["id"] == id {
			continue
		}
		kept = append(kept, item)
	}
	container["boundElements"] = kept
}

func containerID(e element) string { return e.str("containerId") }

func kindName(kind string) string {
	switch kind {
	case "rectangle":
		return "a rectangle"
	case "ellipse":
		return "an ellipse"
	case "arrow":
		return "an arrow"
	case "text":
		return "a text"
	case "image":
		return "an image"
	default:
		return "a " + kind
	}
}

func utf8len(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

// ---- element map field helpers ------------------------------------------------

func (e element) str(k string) string {
	if v, ok := e[k].(string); ok {
		return v
	}
	return ""
}

func (e element) num(k string) float64 {
	switch v := e[k].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	}
	return 0
}
