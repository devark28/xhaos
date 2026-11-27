package board

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/devark28/xhaos/app/apperr"
	"github.com/devark28/xhaos/app/sqlitedb"
	"github.com/devark28/xhaos/app/tool"
)

// board_read renders a board for an agent: one line per live element, each
// element named by a short unique id prefix, and narrowed by the call's
// filters (ids, group, type, region) or shown as a compact outline.

// minIDChars is the shortest an element's or group's id is cut to in
// board_read; a longer prefix is used only where it is needed to stay unique.
const minIDChars = 4

// shortestUniquePrefix returns the shortest prefix of full (at least
// minIDChars, or the whole id when it is shorter) that none of the other ids
// in all shares.
func shortestUniquePrefix(full string, all []string) string {
	n := minIDChars
	if n > len(full) {
		n = len(full)
	}
	for ; n < len(full); n++ {
		p := full[:n]
		unique := true
		for _, other := range all {
			if other != full && strings.HasPrefix(other, p) {
				unique = false
				break
			}
		}
		if unique {
			return p
		}
	}
	return full
}

// shortIDs maps each live element's id to its short unique form.
func shortIDs(els []sceneElement) map[string]string {
	ids := make([]string, len(els))
	for i, e := range els {
		ids[i] = e.ID
	}
	out := make(map[string]string, len(els))
	for _, e := range els {
		out[e.ID] = shortestUniquePrefix(e.ID, ids)
	}
	return out
}

// groupShortIDs maps each group id on the board to its short unique form.
func groupShortIDs(els []sceneElement) map[string]string {
	seen := map[string]bool{}
	var groups []string
	for _, e := range els {
		for _, g := range e.GroupIDs {
			if g != "" && !seen[g] {
				seen[g] = true
				groups = append(groups, g)
			}
		}
	}
	out := make(map[string]string, len(groups))
	for _, g := range groups {
		out[g] = shortestUniquePrefix(g, groups)
	}
	return out
}

// resolveID resolves idOrPrefix against candidates: an exact match wins, then
// the single candidate it prefixes. No match is NotFound; several is an error.
func resolveID(idOrPrefix string, candidates []string) (string, error) {
	for _, c := range candidates {
		if c == idOrPrefix {
			return c, nil
		}
	}
	var matches []string
	for _, c := range candidates {
		if strings.HasPrefix(c, idOrPrefix) {
			matches = append(matches, c)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("%w: %s", apperr.NotFound, idOrPrefix)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("%q is ambiguous: it is a prefix of %s", idOrPrefix, strings.Join(matches, ", "))
	}
}

// resolveElementID resolves an element id (or its unique prefix) against the
// board's live elements — the ones board_read shows. board_edit uses it.
func resolveElementID(q sqlitedb.Querier, boardID int64, id string) (string, error) {
	rows, err := q.Query(`SELECT element_id, data FROM board_elements WHERE board_id = ?`, boardID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var eid, data string
		if err := rows.Scan(&eid, &data); err != nil {
			return "", err
		}
		var e sceneElement
		if json.Unmarshal([]byte(data), &e) == nil && e.IsDeleted {
			continue
		}
		ids = append(ids, eid)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return resolveID(id, ids)
}

// readFilter is board_read's narrowing; its zero value keeps everything.
type readFilter struct {
	mode   string          // "outline" or "items" ("" = items)
	ids    map[string]bool // element ids to keep (nil = all)
	group  string          // group id to keep ("" = all)
	typ    string          // element type to keep ("" = all)
	region *box            // keep elements touching this rectangle (nil = all)
}

func (f readFilter) keeps(e sceneElement) bool {
	if f.ids != nil && !f.ids[e.ID] {
		return false
	}
	if f.group != "" {
		in := false
		for _, g := range e.GroupIDs {
			if g == f.group {
				in = true
				break
			}
		}
		if !in {
			return false
		}
	}
	if f.typ != "" && e.Type != f.typ {
		return false
	}
	if f.region != nil && !e.bounds().near(*f.region, 0) {
		return false
	}
	return true
}

// readFilterFrom checks a board_read call's narrowing and resolves its ids and
// group against the board's live elements.
func readFilterFrom(in input, els []sceneElement) (readFilter, error) {
	f := readFilter{mode: strings.TrimSpace(in.Mode), typ: strings.TrimSpace(in.Type)}
	switch f.mode {
	case "", "outline", "items":
	default:
		return f, fmt.Errorf("board_read mode is %q or %q, not %q", "outline", "items", f.mode)
	}
	if len(in.IDs) > 0 {
		ids := make([]string, len(els))
		for i, e := range els {
			ids[i] = e.ID
		}
		f.ids = map[string]bool{}
		for _, raw := range in.IDs {
			raw = strings.TrimSpace(raw)
			if raw == "" {
				return f, errors.New("board_read ids cannot be empty")
			}
			full, err := resolveID(raw, ids)
			if err != nil {
				return f, err
			}
			f.ids[full] = true
		}
	}
	if g := strings.TrimSpace(in.Group); g != "" {
		var groups []string
		seen := map[string]bool{}
		for _, e := range els {
			for _, gr := range e.GroupIDs {
				if gr != "" && !seen[gr] {
					seen[gr] = true
					groups = append(groups, gr)
				}
			}
		}
		full, err := resolveID(g, groups)
		if err != nil {
			return f, err
		}
		f.group = full
	}
	if in.X != nil || in.Y != nil || in.Width != nil || in.Height != nil {
		if in.X == nil || in.Y == nil || in.Width == nil || in.Height == nil {
			return f, errors.New("board_read region needs x, y, width and height")
		}
		if math.IsNaN(*in.X) || math.IsNaN(*in.Y) || math.Abs(*in.X) > maxCoord || math.Abs(*in.Y) > maxCoord {
			return f, fmt.Errorf("board_read region x and y are between -%.0f and %.0f", maxCoord, maxCoord)
		}
		if !(*in.Width > 0 && *in.Height > 0 && *in.Width <= maxSide && *in.Height <= maxSide) {
			return f, fmt.Errorf("board_read region width and height are between 0 and %.0f", maxSide)
		}
		f.region = &box{*in.X, *in.Y, *in.X + *in.Width, *in.Y + *in.Height}
	}
	return f, nil
}

// read renders the board's live elements, narrowed by in's filters.
func (t *Tools) read(ws string, id int64, in input) (string, error) {
	b, err := t.s.Load(ws, id)
	if err != nil {
		return "", err
	}
	mimes := map[string]string{}
	for _, r := range b.Images {
		mimes[r.FileID] = r.MimeType
	}
	var els []sceneElement
	for _, raw := range b.Elements {
		var e sceneElement
		if json.Unmarshal(raw, &e) == nil && !e.IsDeleted {
			els = append(els, e)
		}
	}
	sortByIndex(els)
	f, err := readFilterFrom(in, els)
	if err != nil {
		return "", err
	}
	short := shortIDs(els)
	groups := groupShortIDs(els)
	var kept []sceneElement
	for _, e := range els {
		if f.keeps(e) {
			kept = append(kept, e)
		}
	}
	var out strings.Builder
	unit := " (x right, y down)"
	if f.mode == "outline" {
		unit = " (outline)"
	}
	switch {
	case len(kept) == len(els):
		fmt.Fprintf(&out, "Board #%d %q, %d elements%s.\n", b.ID, b.Name, len(els), unit)
	case len(kept) == 0:
		fmt.Fprintf(&out, "Board #%d %q, no elements match.\n", b.ID, b.Name)
	default:
		fmt.Fprintf(&out, "Board #%d %q, %d of %d elements%s.\n", b.ID, b.Name, len(kept), len(els), unit)
	}
	for i, e := range kept {
		if i == maxRead {
			fmt.Fprintf(&out, "… and %d more.\n", len(kept)-maxRead)
			break
		}
		if f.mode == "outline" {
			writeOutline(&out, e, short, groups)
		} else {
			writeItem(&out, e, short, mimes)
		}
	}
	return tool.Clip(strings.TrimSpace(out.String())), nil
}

// writeItem is one element's detailed line (board_read items).
func writeItem(w *strings.Builder, e sceneElement, short map[string]string, mimes map[string]string) {
	bb := e.bounds()
	fmt.Fprintf(w, "- %s %s v%d at (%.0f, %.0f) size %.0f×%.0f", e.Type, short[e.ID], e.Version, bb.x0, bb.y0, bb.x1-bb.x0, bb.y1-bb.y0)
	if e.Type == "image" {
		name, _ := e.CustomData["xhaosName"].(string)
		if name == "" {
			name = "unnamed"
		}
		fmt.Fprintf(w, " image %q (%s)", name, orUnknown(mimes[e.FileID]))
	}
	if e.Text != "" {
		fmt.Fprintf(w, " text %q", clipText(e.Text, 300))
	}
	if e.Container != "" {
		fmt.Fprintf(w, " in %s", short[e.Container])
	}
	if n := e.agentOf(); n != 0 {
		fmt.Fprintf(w, " (added by #%d)", n)
	}
	w.WriteByte('\n')
}

// writeOutline is one element's compact line (board_read outline): type, short
// id, version and text, without geometry. The version lets an agent board_edit
// straight after an outline read.
func writeOutline(w *strings.Builder, e sceneElement, short, groupShort map[string]string) {
	fmt.Fprintf(w, "- %s %s v%d", e.Type, short[e.ID], e.Version)
	if e.Type == "image" {
		name, _ := e.CustomData["xhaosName"].(string)
		if name == "" {
			name = "unnamed"
		}
		fmt.Fprintf(w, " image %q", name)
	}
	if e.Text != "" {
		fmt.Fprintf(w, " text %q", clipText(e.Text, 60))
	}
	for _, g := range e.GroupIDs {
		if gs := groupShort[g]; gs != "" {
			fmt.Fprintf(w, " group %s", gs)
		}
	}
	w.WriteByte('\n')
}

// sortByIndex puts elements in drawing order: by fractional index, those
// without one last in their stored order (as Excalidraw does).
func sortByIndex(els []sceneElement) {
	key := func(e sceneElement) string {
		if e.Index == nil {
			return "\U0010FFFF"
		}
		return *e.Index
	}
	// insertion sort keeps stored order among equal keys
	for i := 1; i < len(els); i++ {
		for j := i; j > 0 && key(els[j]) < key(els[j-1]); j-- {
			els[j], els[j-1] = els[j-1], els[j]
		}
	}
}
