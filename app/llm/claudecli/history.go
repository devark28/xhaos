package claudecli

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/devark28/xhaos/app/llm"
)

// toolPrefix is what Claude Code puts before the name of every tool an MCP
// server (here, "xhaos") offers.
const toolPrefix = "mcp__xhaos__"

var toolNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// maxToolName is the longest tool name Claude Code accepts (64), less the
// prefix. A longer Xhaos name is shortened with a hash, and mapped back.
const maxToolName = 64 - len(toolPrefix)

// frame is one stream-json input line: a user or assistant message. A user
// frame before the last only restores history; it makes no model call.
type frame struct {
	Type        string         `json:"type"`
	Message     map[string]any `json:"message"`
	ShouldQuery *bool          `json:"shouldQuery,omitempty"`
}

func (f frame) content() []any {
	c, _ := f.Message["content"].([]any)
	return c
}

// names maps Xhaos tool names to the names Claude Code is given, and back.
type names struct{ native, xhaos map[string]string }

func toolNames(tools []llm.Tool) (names, error) {
	n := names{native: map[string]string{}, xhaos: map[string]string{}}
	for _, t := range tools {
		if !toolNamePattern.MatchString(t.Name) {
			return n, fmt.Errorf("tool name %q is not usable with Claude Code", t.Name)
		}
		native := t.Name
		if len(native) > maxToolName {
			sum := sha256.Sum256([]byte(t.Name))
			native = native[:maxToolName-9] + "_" + fmt.Sprintf("%x", sum[:4])
		}
		if other, taken := n.xhaos[native]; taken && other != t.Name {
			return n, fmt.Errorf("tool names %q and %q collide as %q", t.Name, other, native)
		}
		n.native[t.Name], n.xhaos[native] = native, t.Name
	}
	return n, nil
}

// wire is the name Claude Code knows a tool by.
func (n names) wire(name string) string {
	if native, ok := n.native[name]; ok {
		return toolPrefix + native
	}
	return toolPrefix + name
}

// ours maps a name from Claude Code's reply to the Xhaos tool, with or
// without the prefix (Claude Code sometimes drops it from a tool it was
// offered). The second result says whether it is one of ours.
func (n names) ours(name string) (string, bool) {
	bare := strings.TrimPrefix(name, toolPrefix)
	if x, ok := n.xhaos[bare]; ok {
		return x, true
	}
	return bare, false
}

// frames turns a conversation into the stream-json lines that restore it. The
// last must be a user message: Claude Code answers it.
func frames(messages []llm.Message, n names) ([]frame, error) {
	var out []frame
	for _, m := range messages {
		if m.Role == "user" {
			var blocks []any
			for _, r := range m.Results {
				b := map[string]any{"type": "tool_result", "tool_use_id": r.CallID, "content": r.Content}
				if r.IsError {
					b["is_error"] = true
				}
				blocks = append(blocks, b)
			}
			if m.Text != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": m.Text})
			}
			for _, f := range m.Files {
				kind, media := "image", f.Type
				if f.Type == "application/pdf" {
					kind = "document"
				}
				blocks = append(blocks, map[string]any{"type": kind, "source": map[string]any{
					"type": "base64", "media_type": media, "data": base64.StdEncoding.EncodeToString(f.Data)}})
			}
			if len(blocks) == 0 {
				continue
			}
			if k := len(out) - 1; k >= 0 && out[k].Type == "user" {
				out[k].Message["content"] = append(out[k].content(), blocks...) // results first, then the owner's words
				continue
			}
			out = append(out, frame{Type: "user", Message: map[string]any{"role": "user", "content": blocks}})
			continue
		}
		if m.RawKind == Kind && len(m.Raw) > 0 {
			// Our own reply, replayed as Claude Code made it (signed thinking included).
			var prior []map[string]any
			if err := json.Unmarshal(m.Raw, &prior); err != nil {
				return nil, fmt.Errorf("replay assistant message: %w", err)
			}
			for _, msg := range prior {
				// usage is local metadata: after a compaction it would still show the old,
				// larger size, and Claude Code would refuse a request it thinks is too long.
				delete(msg, "usage")
				if m.StripThinking {
					kept := []any{}
					blocks, _ := msg["content"].([]any)
					for _, b := range blocks {
						blk, _ := b.(map[string]any)
						if t, _ := blk["type"].(string); t != "thinking" && t != "redacted_thinking" {
							kept = append(kept, b)
						}
					}
					if len(kept) == 0 {
						continue // the reply was only reasoning
					}
					msg["content"] = kept
				}
				out = append(out, frame{Type: "assistant", Message: msg})
			}
			continue
		}
		var blocks []any
		if m.Text != "" {
			blocks = append(blocks, map[string]any{"type": "text", "text": m.Text})
		}
		for _, c := range m.Calls {
			var input any = map[string]any{}
			if len(c.Input) > 0 {
				if err := json.Unmarshal(c.Input, &input); err != nil {
					return nil, fmt.Errorf("tool call %s: %w", c.ID, err)
				}
			}
			blocks = append(blocks, map[string]any{"type": "tool_use", "id": c.ID, "name": n.wire(c.Name), "input": input})
		}
		if len(blocks) == 0 {
			continue
		}
		out = append(out, frame{Type: "assistant", Message: map[string]any{"role": "assistant", "content": blocks}})
	}
	if len(out) == 0 || out[len(out)-1].Type != "user" {
		return nil, errors.New("the conversation must end with a user message")
	}
	no := false
	for i := range out[:len(out)-1] {
		if out[i].Type == "user" {
			out[i].ShouldQuery = &no
		}
	}
	return out, nil
}

// sessionID is the same for every request of one conversation: Claude Code
// names a scratch directory after it in the prompt, so a new id per request
// would move the start of the prompt and miss the cache every time.
func sessionID(system string, first frame) string {
	raw, _ := json.Marshal([]any{system, first.Message})
	sum := sha256.Sum256(raw)
	b := sum[:16]
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
