package history

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/devark28/xhaos/app/agent"
	"github.com/devark28/xhaos/app/llm"
	"github.com/devark28/xhaos/app/tool"
)

// Tools is the agent-side tool, history_search (a function, not a method, so
// binding Service to the page does not expose it).
type Tools struct{ s *Service }

// NewTools is the agent's toolset for the history.
func NewTools(s *Service) *Tools { return &Tools{s: s} }

const toolName = "history_search"

// Owns reports whether the tool is history_search.
func (t *Tools) Owns(name string) bool { return name == toolName }

// Catalog describes the tool for the Tools panel. Reading the history is a
// read: it changes nothing.
func (t *Tools) Catalog() []tool.Info {
	return []tool.Info{{Name: toolName, Description: specDescription, Effect: tool.Read, Source: "history"}}
}

const specDescription = "Search the past sessions (transcripts of earlier work on this project) for past decisions, context and discussion. Returns the closest passages with their date and session. Searches this agent's project only, unless the owner allows the whole workspace."

// Specs is the tool's schema for the model.
func (t *Tools) Specs(string) []llm.Tool {
	return []llm.Tool{{
		Name:        toolName,
		Description: specDescription,
		Schema: llm.ToolSchema{
			Properties: map[string]any{
				"query":   map[string]any{"type": "string", "description": "What to look for, in plain words."},
				"project": map[string]any{"type": "string", "description": "A project's name. Only with the owner's workspace-wide allowance; otherwise the agent's own project is searched."},
				"limit":   map[string]any{"type": "integer", "description": "How many passages to return (1 to 10; default 5)."},
			},
			Required: []string{"query"},
		},
	}}
}

type input struct {
	Query   string `json:"query"`
	Project string `json:"project"`
	Limit   int    `json:"limit"`
}

// Prepare checks the input; the search itself runs in Execute.
func (t *Tools) Prepare(_ string, _ agent.Agent, c llm.ToolCall) (tool.Call, error) {
	call := tool.Call{ID: c.ID, Name: c.Name, Effect: tool.Read, Args: c.Input}
	var in input
	dec := json.NewDecoder(strings.NewReader(string(c.Input)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return call, fmt.Errorf("invalid input for %s: %v", c.Name, err)
	}
	if strings.TrimSpace(in.Query) == "" {
		return call, errors.New("history_search needs a query")
	}
	if in.Limit < 0 || in.Limit > 10 {
		return call, errors.New("history_search: limit is 1 to 10")
	}
	call.Summary = "history: " + Excerpt(in.Query, 60)
	return call, nil
}

// Execute runs the search. The project is the agent's own unless the owner has
// allowed the whole workspace; another project named by the agent is refused
// without that allowance.
func (t *Tools) Execute(ctx context.Context, ws string, a agent.Agent, call tool.Call) (string, error) {
	var in input
	if err := json.Unmarshal(call.Args, &in); err != nil {
		return "", err
	}
	all, err := t.s.allProjects()
	if err != nil {
		return "", err
	}
	var projects []int64
	switch {
	case in.Project != "":
		id, err := t.s.projectID(ws, in.Project)
		if err != nil {
			return "", err
		}
		if id != a.ProjectID && !all {
			return "", fmt.Errorf("project %q is not this agent's project; the owner allows searching other projects in Settings", in.Project)
		}
		projects = []int64{id}
	case all:
		projects = nil // the whole workspace
	case a.ProjectID != 0:
		projects = []int64{a.ProjectID}
	default:
		return "", errors.New("this agent has no project to search; the owner can allow the whole workspace in Settings")
	}
	limit := in.Limit
	if limit == 0 {
		limit = 5
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	results, err := t.s.search(ctx, ws, in.Query, projects, limit)
	if err != nil {
		return "", err
	}
	if len(results) == 0 {
		return "No matching passages in the history.", nil
	}
	var b strings.Builder
	for i, r := range results {
		fmt.Fprintf(&b, "[%d] %s · %s · %s (%s)\n%s\n\n", i+1, r.ProjectName, r.At.Format("2006-01-02"), r.Session, r.Role, r.Excerpt)
	}
	return strings.TrimSpace(b.String()), nil
}
