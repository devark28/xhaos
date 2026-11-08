package board

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/devark28/xhaos/app/agent"
	"github.com/devark28/xhaos/app/llm"
	"github.com/devark28/xhaos/app/sandbox"
	"github.com/devark28/xhaos/app/tool"
)

// ---- tools for agents ----------------------------------------------------------
//
// Reading boards is free. Adding to one (or creating one) is gated like a
// file write: the owner's board is theirs. What an agent adds is grouped,
// tagged "added by #N", and placed beside the owner's drawing (layout.go).

const (
	maxNote   = 4000 // characters of a note or a shape's text
	maxShapes = 200
	maxCoord  = 100_000.0
	maxSide   = 20_000.0
	maxRead   = 400 // elements board_read lists
)

// Tools is the agent-side toolset (a function builds it, not a method, so
// binding Service to the page does not expose it).
type Tools struct {
	s *Service
	// Root is the calling agent's project folder; images are read from it
	// or from the agent's $TMPDIR (sandbox.TempDir of it).
	Root func(a agent.Agent) (string, error)
}

func NewTools(s *Service, root func(a agent.Agent) (string, error)) *Tools {
	return &Tools{s: s, Root: root}
}

var effects = map[string]string{
	"board_list": tool.Read, "board_read": tool.Read,
	"board_create": tool.Write, "board_add_note": tool.Write, "board_add_image": tool.Write, "board_add_shapes": tool.Write,
}

func (t *Tools) Owns(name string) bool { _, ok := effects[name]; return ok }

// Catalog describes these tools for the Tools panel.
func (t *Tools) Catalog() []tool.Info {
	var out []tool.Info
	for _, spec := range t.Specs("") {
		out = append(out, tool.Info{Name: spec.Name, Description: spec.Description, Effect: effects[spec.Name], Source: "boards"})
	}
	return out
}

func (t *Tools) Specs(string) []llm.Tool {
	str := map[string]any{"type": "string"}
	num := map[string]any{"type": "number"}
	boardID := map[string]any{"type": "integer", "description": "The board's id (board_list shows them)."}
	shape := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"type": map[string]any{"type": "string", "enum": []string{"rectangle", "ellipse", "arrow", "text"}},
			"x":    num, "y": num,
			"width":  map[string]any{"type": "number", "description": "rectangle and ellipse"},
			"height": map[string]any{"type": "number", "description": "rectangle and ellipse"},
			"x2":     map[string]any{"type": "number", "description": "arrow: where it points"},
			"y2":     map[string]any{"type": "number", "description": "arrow: where it points"},
			"text":   map[string]any{"type": "string", "description": "text: its words; rectangle or ellipse: a label inside"},
			"color":  map[string]any{"type": "string", "description": "stroke or text colour, #rgb or #rrggbb"},
			"fill":   map[string]any{"type": "string", "description": "rectangle or ellipse background, #rgb or #rrggbb"},
		},
		"required": []string{"type", "x", "y"},
	}
	where := " It is grouped, tagged \"added by\" you, and placed in free space beside what is on the board (never over it)."
	return []llm.Tool{
		{Name: "board_list", Description: "List the workspace's boards (the user's Excalidraw drawings in the Draw tab): id and name.",
			Schema: llm.ToolSchema{Properties: map[string]any{}}},
		{Name: "board_read", Description: "Read a board as text: each element's type, id, position, size and text, in drawing order. Images are listed by name, without their pixels.",
			Schema: llm.ToolSchema{Properties: map[string]any{"board": boardID}, Required: []string{"board"}}},
		{Name: "board_create", Description: "Create an empty board. The user may review it first.",
			Schema: llm.ToolSchema{Properties: map[string]any{"name": str}, Required: []string{"name"}}},
		{Name: "board_add_note", Description: "Add a sticky note with text to a board." + where + " The user may review it first.",
			Schema: llm.ToolSchema{Properties: map[string]any{"board": boardID, "text": str}, Required: []string{"board", "text"}}},
		{Name: "board_add_image", Description: "Add a PNG, JPEG, GIF or WebP image (at most 20 MB) to a board, with an optional caption. path is in your project, or in your $TMPDIR (absolute)." + where + " The user may review it first.",
			Schema: llm.ToolSchema{Properties: map[string]any{"board": boardID, "path": str, "caption": str}, Required: []string{"board", "path"}}},
		{Name: "board_add_shapes", Description: fmt.Sprintf("Add a small diagram to a board: rectangles, ellipses, arrows (x,y to x2,y2) and text, in your own coordinates (x right, y down; only their relative positions matter), at most %d shapes.", maxShapes) + where + " The user may review it first.",
			Schema: llm.ToolSchema{Properties: map[string]any{"board": boardID, "shapes": map[string]any{"type": "array", "items": shape}}, Required: []string{"board", "shapes"}}},
	}
}

type input struct {
	Board   int64   `json:"board"`
	Name    string  `json:"name"`
	Text    string  `json:"text"`
	Path    string  `json:"path"`
	Caption string  `json:"caption"`
	Shapes  []Shape `json:"shapes"`
}

var colorRE = regexp.MustCompile(`^(#[0-9a-fA-F]{3}|#[0-9a-fA-F]{6}|transparent)$`)

func (t *Tools) Prepare(ws string, a agent.Agent, c llm.ToolCall) (tool.Call, error) {
	call := tool.Call{ID: c.ID, Name: c.Name, Effect: effects[c.Name], Args: c.Input}
	var in input
	dec := json.NewDecoder(strings.NewReader(string(c.Input)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return call, fmt.Errorf("invalid input for %s: %v", c.Name, err)
	}
	if c.Name == "board_list" {
		call.Summary = "boards"
		return call, nil
	}
	if c.Name == "board_create" {
		name, err := cleanName(in.Name)
		if err != nil {
			return call, errors.New(strings.TrimPrefix(err.Error(), "invalid: "))
		}
		if name == "" {
			return call, errors.New("board_create needs name")
		}
		call.Summary, call.Preview = fmt.Sprintf("%q", name), fmt.Sprintf("Create an empty board named %q.", name)
		return call, nil
	}
	if in.Board <= 0 {
		return call, fmt.Errorf("%s needs board (an id from board_list)", c.Name)
	}
	if err := checkWorkspace(ws); err != nil {
		return call, err
	}
	b, err := find(t.s.db, ws, in.Board)
	if err != nil {
		return call, fmt.Errorf("there is no board #%d; board_list shows the boards", in.Board)
	}
	on := fmt.Sprintf("board #%d %q", b.ID, b.Name)
	switch c.Name {
	case "board_read":
		call.Summary = on
	case "board_add_note":
		if strings.TrimSpace(in.Text) == "" {
			return call, errors.New("board_add_note needs text")
		}
		if n := utf8.RuneCountInString(in.Text); n > maxNote {
			return call, fmt.Errorf("a note is at most %d characters (this is %d); split it, or write a file", maxNote, n)
		}
		call.Summary = fmt.Sprintf("%s: %s", on, firstLine(in.Text))
		call.Preview = fmt.Sprintf("Add a note to %s, tagged \"added by #%d\":\n\n%s", on, a.ID, in.Text)
	case "board_add_image":
		if utf8.RuneCountInString(in.Caption) > maxNote {
			return call, fmt.Errorf("a caption is at most %d characters", maxNote)
		}
		root, tmp, err := t.folders(a)
		if err != nil {
			return call, err
		}
		st, err := t.s.stage(ws, a.ID, c.ID, root, tmp, in.Path)
		if err != nil {
			return call, err
		}
		call.Summary = fmt.Sprintf("%s: %s", on, st.Name)
		call.Preview = fmt.Sprintf("Add an image to %s, tagged \"added by #%d\":\n\n%s (%s, %d×%d, %s)", on, a.ID, st.Name, st.MimeType, st.Width, st.Height, sizeText(st.Size))
		if in.Caption != "" {
			call.Preview += "\nCaption: " + in.Caption
		}
	case "board_add_shapes":
		if err := checkShapes(in.Shapes); err != nil {
			return call, err
		}
		call.Summary = fmt.Sprintf("%s: %d shapes", on, len(in.Shapes))
		var p strings.Builder
		fmt.Fprintf(&p, "Add %d shapes to %s, grouped and tagged \"added by #%d\":\n", len(in.Shapes), on, a.ID)
		for _, s := range in.Shapes {
			p.WriteString("\n- " + describeShape(s))
		}
		call.Preview = p.String()
	}
	return call, nil
}

// folders are where an agent's images may come from: its project and its
// $TMPDIR.
func (t *Tools) folders(a agent.Agent) (string, string, error) {
	if t.Root == nil {
		return "", "", errors.New("images cannot be added on this server")
	}
	root, err := t.Root(a)
	if err != nil {
		return "", "", err
	}
	tmp, err := sandbox.TempDir(root)
	if err != nil {
		return "", "", err
	}
	return root, tmp, nil
}

func (t *Tools) Execute(_ context.Context, ws string, a agent.Agent, call tool.Call) (string, error) {
	var in input
	_ = json.Unmarshal(call.Args, &in)
	switch call.Name {
	case "board_list":
		list, err := t.s.List(ws)
		if err != nil {
			return "", err
		}
		if len(list) == 0 {
			return "No boards yet. board_create makes one.", nil
		}
		var b strings.Builder
		for _, m := range list {
			fmt.Fprintf(&b, "#%d %s (updated %s)\n", m.ID, m.Name, m.UpdatedAt.Format("2006-01-02 15:04"))
		}
		return tool.Clip(strings.TrimSpace(b.String())), nil
	case "board_read":
		return t.read(ws, in.Board)
	case "board_create":
		m, err := t.s.Create(CreateRequest{WorkspaceID: ws, Name: in.Name})
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Created board #%d %q.", m.ID, m.Name), nil
	case "board_add_note":
		add := newAddition(a.ID)
		add.note(in.Text)
		return t.add(ws, a, in.Board, add, nil, "", call.ID, "a note")
	case "board_add_image":
		st, err := t.s.stagedImage(ws, a.ID, call.ID)
		if err != nil {
			return "", errors.New("the image is no longer staged; call board_add_image again")
		}
		add := newAddition(a.ID)
		add.image(st.Hash, st.Name, st.Width, st.Height, in.Caption)
		return t.add(ws, a, in.Board, add, &st, st.Hash, call.ID, "image "+st.Name)
	case "board_add_shapes":
		if err := checkShapes(in.Shapes); err != nil {
			return "", err
		}
		add := newAddition(a.ID)
		add.shapes(in.Shapes)
		return t.add(ws, a, in.Board, add, nil, "", call.ID, fmt.Sprintf("%d shapes", len(in.Shapes)))
	}
	return "", fmt.Errorf("unknown tool %s", call.Name)
}

func (t *Tools) add(ws string, a agent.Agent, boardID int64, add *addition, img *staged, fileID, callID, what string) (string, error) {
	res, err := t.s.addForAgent(ws, a.ID, boardID, add, img, fileID, callID)
	if err != nil {
		return "", err
	}
	w, h := add.size()
	return fmt.Sprintf("Added %s to board #%d %q, at (%.0f, %.0f), %.0f×%.0f, as group %s (%d elements). It shows on open pages now.",
		what, res.Board.ID, res.Board.Name, res.X, res.Y, w, h, res.Group, len(res.Elements)), nil
}

// read is board_read's text: one line per live element, in drawing order.
func (t *Tools) read(ws string, id int64) (string, error) {
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
	var out strings.Builder
	fmt.Fprintf(&out, "Board #%d %q, %d elements (x right, y down).\n", b.ID, b.Name, len(els))
	for i, e := range els {
		if i == maxRead {
			fmt.Fprintf(&out, "… and %d more.\n", len(els)-maxRead)
			break
		}
		bb := e.bounds()
		fmt.Fprintf(&out, "- %s %s at (%.0f, %.0f) size %.0f×%.0f", e.Type, e.ID, bb.x0, bb.y0, bb.x1-bb.x0, bb.y1-bb.y0)
		if e.Type == "image" {
			name, _ := e.CustomData["xhaosName"].(string)
			if name == "" {
				name = "unnamed"
			}
			fmt.Fprintf(&out, " image %q (%s)", name, orUnknown(mimes[e.FileID]))
		}
		if e.Text != "" {
			fmt.Fprintf(&out, " text %q", clipText(e.Text, 300))
		}
		if e.Container != "" {
			fmt.Fprintf(&out, " in %s", e.Container)
		}
		if n := e.agentOf(); n != 0 {
			fmt.Fprintf(&out, " (added by #%d)", n)
		}
		out.WriteString("\n")
	}
	return tool.Clip(strings.TrimSpace(out.String())), nil
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

func checkShapes(list []Shape) error {
	if len(list) == 0 {
		return errors.New("board_add_shapes needs shapes")
	}
	if len(list) > maxShapes {
		return fmt.Errorf("at most %d shapes in one call (this has %d)", maxShapes, len(list))
	}
	for i, s := range list {
		n := i + 1
		for _, v := range []float64{s.X, s.Y, s.X2, s.Y2} {
			if math.IsNaN(v) || math.Abs(v) > maxCoord {
				return fmt.Errorf("shape %d: coordinates are between -%.0f and %.0f", n, maxCoord, maxCoord)
			}
		}
		for _, c := range []string{s.Color, s.Fill} {
			if c != "" && !colorRE.MatchString(c) {
				return fmt.Errorf("shape %d: colour %q is not #rgb or #rrggbb", n, c)
			}
		}
		if utf8.RuneCountInString(s.Text) > maxNote {
			return fmt.Errorf("shape %d: text is at most %d characters", n, maxNote)
		}
		switch s.Type {
		case "rectangle", "ellipse":
			if !(s.Width > 0 && s.Height > 0 && s.Width <= maxSide && s.Height <= maxSide) {
				return fmt.Errorf("shape %d: a %s needs width and height between 0 and %.0f", n, s.Type, maxSide)
			}
		case "arrow":
			if s.X == s.X2 && s.Y == s.Y2 {
				return fmt.Errorf("shape %d: an arrow needs x2, y2 apart from x, y", n)
			}
		case "text":
			if strings.TrimSpace(s.Text) == "" {
				return fmt.Errorf("shape %d: a text needs text", n)
			}
		default:
			return fmt.Errorf("shape %d: type is rectangle, ellipse, arrow or text, not %q", n, s.Type)
		}
	}
	return nil
}

func describeShape(s Shape) string {
	var d string
	switch s.Type {
	case "arrow":
		d = fmt.Sprintf("arrow (%.0f, %.0f) → (%.0f, %.0f)", s.X, s.Y, s.X2, s.Y2)
	case "text":
		d = fmt.Sprintf("text at (%.0f, %.0f)", s.X, s.Y)
	default:
		d = fmt.Sprintf("%s at (%.0f, %.0f) %.0f×%.0f", s.Type, s.X, s.Y, s.Width, s.Height)
	}
	if s.Text != "" {
		d += fmt.Sprintf(" %q", clipText(s.Text, 120))
	}
	if s.Color != "" {
		d += " " + s.Color
	}
	if s.Fill != "" {
		d += " fill " + s.Fill
	}
	return d
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return clipText(s, 80)
}

func clipText(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

func orUnknown(s string) string {
	if s == "" {
		return "not uploaded"
	}
	return s
}

func sizeText(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}
