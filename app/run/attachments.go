package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/devark28/xhaos/app/agent"
	"github.com/devark28/xhaos/app/attachment"
	"github.com/devark28/xhaos/app/llm"
	"github.com/devark28/xhaos/app/tool"
)

// Text attachments up to maxInlineText characters go into the message whole.
// A larger one sends its first previewText characters and a note; the agent
// reads the rest, in pieces it chooses, with read_attachment.
const (
	maxInlineText = 100_000
	previewText   = 20_000
	readDefault   = 50_000
)

const readAttachment = "read_attachment"

// attachTools is read_attachment: an agent reads text files that were sent
// to it (and only those), by character range.
type attachTools struct{ r *Runner }

func (t attachTools) Owns(name string) bool { return name == readAttachment && t.r.Files != nil }

func (t attachTools) Specs(string) []llm.Tool {
	if t.r.Files == nil {
		return nil
	}
	integer := map[string]any{"type": "integer"}
	return []llm.Tool{{Name: readAttachment,
		Description: fmt.Sprintf("Read part of a text file attached to a message you received, by character range. id is the attachment id from the message's note; offset is the first character (0-based), limit how many (default %d, at most %d). The reply says the file's total length and where the next part starts.", readDefault, maxInlineText),
		Schema:      llm.ToolSchema{Properties: map[string]any{"id": map[string]any{"type": "string"}, "offset": integer, "limit": integer}, Required: []string{"id"}}}}
}

// Catalog describes the tool for the Tools panel.
func (t attachTools) Catalog() []tool.Info {
	var out []tool.Info
	for _, s := range t.Specs("") {
		out = append(out, tool.Info{Name: s.Name, Description: s.Description, Effect: tool.Read, Source: "built-in"})
	}
	return out
}

type attachInput struct {
	ID     string `json:"id"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

func (t attachTools) Prepare(_ string, _ agent.Agent, c llm.ToolCall) (tool.Call, error) {
	call := tool.Call{ID: c.ID, Name: c.Name, Effect: tool.Read, Args: c.Input}
	var in attachInput
	dec := json.NewDecoder(strings.NewReader(string(c.Input)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return call, fmt.Errorf("invalid input for %s: %v", c.Name, err)
	}
	if strings.TrimSpace(in.ID) == "" {
		return call, errors.New("read_attachment needs the attachment id")
	}
	if in.Offset < 0 || in.Limit < 0 {
		return call, errors.New("offset and limit cannot be negative")
	}
	call.Summary = fmt.Sprintf("attachment %s from %d", in.ID, in.Offset)
	return call, nil
}

func (t attachTools) Execute(_ context.Context, ws string, a agent.Agent, call tool.Call) (string, error) {
	var in attachInput
	_ = json.Unmarshal(call.Args, &in)
	history, err := t.r.agents.Messages(ws, a.ID)
	if err != nil {
		return "", err
	}
	sent := false
	for _, m := range history {
		if slices.ContainsFunc(m.Attachments, func(f attachment.Ref) bool { return f.ID == in.ID }) {
			sent = true
			break
		}
	}
	if !sent {
		return "", fmt.Errorf("no attachment %q was sent to you", in.ID)
	}
	f, err := t.r.Files.Load(ws, in.ID)
	if err != nil {
		return "", err
	}
	if attachment.Kind(f) != "text" {
		return "", fmt.Errorf("%s is not a text file (%s); images and PDFs come with the message itself", f.Name, f.Type)
	}
	limit := in.Limit
	if limit == 0 {
		limit = readDefault
	}
	limit = min(limit, maxInlineText)
	part, total := runeSlice(string(f.Data), in.Offset, limit)
	end := min(in.Offset+utf8.RuneCountInString(part), total)
	if in.Offset >= total {
		return fmt.Sprintf("%s has %d characters; offset %d is past the end.", f.Name, total, in.Offset), nil
	}
	next := "This is the end of the file."
	if end < total {
		next = fmt.Sprintf("The next part starts at offset %d.", end)
	}
	return fmt.Sprintf("%s, characters %d–%d of %d. %s\n````\n%s\n````", f.Name, in.Offset, end, total, next, part), nil
}

// runeSlice returns limit characters of s from offset, and s's length in
// characters.
func runeSlice(s string, offset, limit int) (string, int) {
	total := utf8.RuneCountInString(s)
	start, i := len(s), 0
	for at := range s {
		if i == offset {
			start = at
			break
		}
		i++
	}
	end, n := len(s), 0
	for at := range s[start:] {
		if n == limit {
			end = start + at
			break
		}
		n++
	}
	return s[start:end], total
}

// inlineText is a text attachment as it goes into a message: whole when it
// is small enough, else its start and how to read the rest.
func inlineText(f attachment.File) string {
	text := string(f.Data)
	total := utf8.RuneCountInString(text)
	if total <= maxInlineText {
		return fmt.Sprintf("Attached file %s:\n````\n%s\n````", f.Name, text)
	}
	head, _ := runeSlice(text, 0, previewText)
	return fmt.Sprintf("Attached file %s (id %s) has %d characters, too many to include whole. Its first %d follow; read the rest with %s (id %q, offset %d), in one or more parts as you need.\n````\n%s\n````",
		f.Name, f.ID, total, previewText, readAttachment, f.ID, previewText, head)
}
