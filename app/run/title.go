package run

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/devark28/xhaos/app/agent"
	"github.com/devark28/xhaos/app/llm"
	"github.com/devark28/xhaos/app/tool"
)

const proposeTitle = "propose_title"

// titleTools is propose_title: an agent proposes a new title for itself
// when its task changes. It never waits for the owner: the call returns at
// once and the agent keeps working; the owner renames or keeps it from the
// card (or it applies at once, see agent.Store.ProposeTitle).
type titleTools struct{ r *Runner }

func (titleTools) Owns(name string) bool { return name == proposeTitle }

func (titleTools) Specs(string) []llm.Tool {
	return []llm.Tool{{Name: proposeTitle,
		Description: fmt.Sprintf("Propose a new title for yourself, the one line the user sees for you in their list of agents. Use it only when you start a new task or your focus clearly changes and your current title no longer says what you are doing; not every turn, and never to restate the same task. It returns at once: the user decides later (or it applies at once), so do not wait for it and keep working. title: plain words saying what you are doing, 1 to %d characters, one line; reason: a short why.", agent.MaxTitle),
		Schema:      llm.ToolSchema{Properties: map[string]any{"title": str, "reason": str}, Required: []string{"title"}}}}
}

var str = map[string]any{"type": "string"}

// Catalog describes the tool for the Tools panel.
func (t titleTools) Catalog() []tool.Info {
	var out []tool.Info
	for _, s := range t.Specs("") {
		out = append(out, tool.Info{Name: s.Name, Description: s.Description, Effect: tool.Read, Source: "built-in"})
	}
	return out
}

type titleInput struct {
	Title  string `json:"title"`
	Reason string `json:"reason"`
}

// Prepare checks the title now, so a bad one fails without a call; it needs
// no permission (its effect is read: it changes nothing the agent works on).
func (titleTools) Prepare(_ string, _ agent.Agent, c llm.ToolCall) (tool.Call, error) {
	call := tool.Call{ID: c.ID, Name: c.Name, Effect: tool.Read, Args: c.Input}
	var in titleInput
	dec := json.NewDecoder(strings.NewReader(string(c.Input)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return call, fmt.Errorf("invalid input for %s: %v", c.Name, err)
	}
	title, err := agent.CleanProposal(in.Title)
	if err != nil {
		return call, err
	}
	call.Summary = title
	return call, nil
}

func (t titleTools) Execute(_ context.Context, ws string, a agent.Agent, call tool.Call) (string, error) {
	var in titleInput
	_ = json.Unmarshal(call.Args, &in)
	p, err := t.r.agents.ProposeTitle(ws, a.ID, in.Title, in.Reason)
	if err != nil {
		return "", err
	}
	title := strings.TrimSpace(in.Title)
	if p.Applied {
		why := "dangerously allow is on"
		if p.By == agent.RenameBySetting {
			why = "the user lets agents rename themselves"
		}
		return fmt.Sprintf("Renamed from “%s” to “%s” (applied at once: %s). Keep working.", p.From, title, why), nil
	}
	out := fmt.Sprintf("Proposed “%s” (your title is still “%s”); the user decides when they like. Do not wait for it: keep working.", title, p.From)
	if p.Replaced != "" {
		out += fmt.Sprintf(" It replaces your earlier proposal “%s”.", p.Replaced)
	}
	return out, nil
}
