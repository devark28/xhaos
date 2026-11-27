package board

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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
	X, Y   *float64 // move the element to this anchor point
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
	// Arrows bound to the element are touched too (their bindings are cleared
	// when it is deleted), so load them here for their version checks.
	for _, id := range boundArrowIDs(te) {
		if err := load(id); err != nil {
			return nil, nil, nil, err
		}
	}
	changed, changes, err := applyEdit(id, loaded, heads, op)
	if err != nil {
		return nil, nil, nil, err
	}
	return changed, heads, changes, nil
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
		switch {
		case len(boundTextIDs(tgt)) > 0:
			mark(targetID)["isDeleted"] = true
			for _, id := range boundTextIDs(tgt) {
				if e := mark(id); e != nil {
					e["isDeleted"] = true
				}
			}
			changes = append(changes, "deleted "+kindName(kind)+" and its label")
		case containerID(tgt) != "":
			mark(targetID)["isDeleted"] = true
			if cont := mark(containerID(tgt)); cont != nil {
				unbind(cont, targetID)
			}
			changes = append(changes, "deleted its label")
		default:
			mark(targetID)["isDeleted"] = true
			changes = append(changes, "deleted "+kindName(kind))
		}
		// Arrows bound to the deleted element must not keep aiming at it: clear
		// each one's startBinding/endBinding and drop it from the element's
		// boundElements. The arrows were loaded in loadEdit, so their versions
		// are checked and bumped like the deleted element's.
		for _, id := range boundArrowIDs(tgt) {
			if arrow := mark(id); arrow != nil {
				unbindArrow(arrow, targetID)
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

// unbindArrow clears the arrow's startBinding or endBinding that points at
// shapeID, so the arrow no longer aims at a deleted element.
func unbindArrow(arrow element, shapeID string) {
	if sb, ok := arrow["startBinding"].(map[string]any); ok && sb["elementId"] == shapeID {
		arrow["startBinding"] = nil
	}
	if eb, ok := arrow["endBinding"].(map[string]any); ok && eb["elementId"] == shapeID {
		arrow["endBinding"] = nil
	}
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
