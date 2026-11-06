package skill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"xhaos/app/agent"
	"xhaos/app/llm"
	"xhaos/app/tool"
)

// Tools is skill_read, for agents. A function rather than a method of
// Service, so binding Service to the page does not expose it.
type Tools struct{ s *Service }

func NewTools(s *Service) *Tools { return &Tools{s: s} }

func (t *Tools) Owns(name string) bool { return name == "skill_read" }

func (t *Tools) Specs(string) []llm.Tool {
	str := map[string]any{"type": "string"}
	return []llm.Tool{{Name: "skill_read",
		Description: "Load a skill listed in your instructions: its SKILL.md (with the list of its files), or one of its files. Read a skill before doing the kind of work it covers.",
		Schema:      llm.ToolSchema{Properties: map[string]any{"name": str, "file": str}, Required: []string{"name"}}}}
}

func (t *Tools) Catalog() []tool.Info {
	var out []tool.Info
	for _, s := range t.Specs("") {
		out = append(out, tool.Info{Name: s.Name, Description: s.Description, Effect: tool.Read, Source: "built-in"})
	}
	return out
}

type input struct {
	Name string `json:"name"`
	File string `json:"file"`
}

func (t *Tools) Prepare(_ string, _ agent.Agent, c llm.ToolCall) (tool.Call, error) {
	call := tool.Call{ID: c.ID, Name: c.Name, Effect: tool.Read, Args: c.Input}
	var in input
	dec := json.NewDecoder(strings.NewReader(string(c.Input)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return call, fmt.Errorf("invalid input for skill_read: %v", err)
	}
	if strings.TrimSpace(in.Name) == "" {
		return call, errors.New("skill_read needs the skill's name")
	}
	call.Summary = in.Name
	if in.File != "" {
		call.Summary += " / " + in.File
	}
	return call, nil
}

func (t *Tools) Execute(_ context.Context, ws string, a agent.Agent, call tool.Call) (string, error) {
	var in input
	_ = json.Unmarshal(call.Args, &in)
	return t.s.Read(ws, a.ProjectID, strings.TrimSpace(in.Name), strings.TrimSpace(in.File))
}
