// Package btw answers the user's side questions about the workspace ("what
// is #12 doing?") from a read-only digest of every agent, plus the
// workspace's channels and memories. Nothing is written:
// no agent sees the question or the answer, and no transcript changes. Only
// the user's composer reaches it; agents have no tool for it and talk to
// each other through channels.
package btw

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"xhaos/app/agent"
	"xhaos/app/apperr"
	"xhaos/app/llm"
	"xhaos/app/provider"
	"xhaos/app/usage"
)

const (
	tailItems = 8   // recent transcript items per agent
	itemRunes = 400 // per item
	timeout   = 2 * time.Minute
)

type AskRequest struct {
	WorkspaceID string `json:"workspaceId"`
	Question    string `json:"question"`
	Provider    string `json:"provider"`
	Model       string `json:"model"`
}

type Answer struct {
	Text string `json:"text"`
}

type Service struct {
	agents    *agent.Service
	providers *provider.Store
	// Context, when set, adds workspace sections to the digest (channels,
	// memories), each already rendered as text.
	Context func(workspaceID string) string
	// Usage, when set, counts the tokens of each question.
	Usage *usage.Recorder
}

func NewService(agents *agent.Service, providers *provider.Store) *Service {
	return &Service{agents: agents, providers: providers}
}

const system = `You answer the user's side questions about the agents in their Xhaos workspace.
You see a read-only digest of each agent (title, project, status, model, waiting tool calls, and its most recent transcript items, cut short), then the workspace's channels between agents and its shared memories.
Answer briefly and concretely, citing agents as #ID and channels as channel #ID. If the digest does not show something, say so instead of guessing.
You cannot act on agents or message them; the user does that from the composer.`

func (s *Service) Ask(req AskRequest) (Answer, error) {
	q := strings.TrimSpace(req.Question)
	if q == "" {
		return Answer{}, fmt.Errorf("%w: ask a question after /btw", apperr.Invalid)
	}
	if req.Provider == "" || req.Model == "" {
		return Answer{}, fmt.Errorf("%w: choose a model first", apperr.Invalid)
	}
	snap, err := s.agents.Snapshot(req.WorkspaceID)
	if err != nil {
		return Answer{}, err
	}
	p, creds, err := s.providers.Credentials(req.WorkspaceID, req.Provider)
	if err != nil {
		return Answer{}, err
	}
	adapter := s.providers.Adapter(p.Kind)
	if adapter == nil {
		return Answer{}, fmt.Errorf("%w: provider kind %q has no adapter", apperr.Invalid, p.Kind)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	digest := Digest(snap)
	if s.Context != nil {
		if extra := s.Context(req.WorkspaceID); extra != "" {
			digest += "\n\n" + extra
		}
	}
	turn := llm.Turn{Model: req.Model, System: system, Messages: []llm.Message{{Role: "user", Text: digest + "\n\nQuestion: " + q}}}
	res, err := adapter.Stream(ctx, creds, turn, func(string) {})
	_ = s.Usage.Record(req.WorkspaceID, 0, req.Provider, p.Kind, req.Model, usage.Btw, res)
	if err != nil {
		return Answer{}, err
	}
	return Answer{Text: res.Text}, nil
}

// Digest renders the snapshot as plain text for the model.
func Digest(snap agent.Snapshot) string {
	if len(snap.Agents) == 0 {
		return "The workspace has no agents."
	}
	tails := map[int64][]string{}
	waiting := map[int64]map[string]agent.ToolData{}
	for _, e := range snap.Transcript {
		var line string
		switch e.Kind {
		case agent.EventMessage:
			var m agent.MessageData
			if json.Unmarshal(e.Data, &m) != nil || m.From == agent.FromTool || m.From == agent.FromCompaction || (m.Body == "" && len(m.Calls) == 0) {
				continue // compaction summaries are for the model; the real messages are here
			}
			from := m.From
			switch from {
			case agent.FromOwner:
				from = "user"
			case agent.FromChannel:
				from = fmt.Sprintf("from agent #%d via channel #%d", m.Sender, m.Channel)
			}
			line = from + ": " + m.Body
			if m.Body == "" {
				line = from + ": (tool calls only)"
			}
			for _, f := range m.Attachments {
				line += fmt.Sprintf(" [attached %s]", f.Name)
			}
		case agent.EventTool:
			var d agent.ToolData
			if json.Unmarshal(e.Data, &d) != nil {
				continue
			}
			if waiting[e.AgentID] == nil {
				waiting[e.AgentID] = map[string]agent.ToolData{}
			}
			waiting[e.AgentID][d.CallID] = d
			if d.State == agent.ToolPending || d.State == agent.ToolAllowed {
				continue
			}
			line = fmt.Sprintf("tool %s %s: %s", d.Name, d.State, d.Summary)
			if d.Reason != "" {
				line += " (" + d.Reason + ")"
			}
		default:
			continue
		}
		tails[e.AgentID] = append(tails[e.AgentID], cut(line))
	}
	var b strings.Builder
	for _, a := range snap.Agents {
		fmt.Fprintf(&b, "## Agent #%d: %s\n", a.ID, a.Title)
		if a.ParentID != 0 {
			fmt.Fprintf(&b, "Subagent of #%d. ", a.ParentID)
		}
		if a.ProjectID != 0 {
			fmt.Fprintf(&b, "Project %d. ", a.ProjectID)
		}
		if a.Role != "" {
			fmt.Fprintf(&b, "Role: %s. ", a.Role)
		}
		fmt.Fprintf(&b, "Status: %s. Model: %s. Created %s.\n", a.Status, orNone(a.Model), a.CreatedAt.Format(time.RFC3339))
		for _, d := range waiting[a.ID] {
			if d.State == agent.ToolPending {
				fmt.Fprintf(&b, "Waiting for the user's approval: %s %s\n", d.Name, cut(d.Summary))
			}
		}
		if a.Status == agent.StatusTerminated {
			b.WriteString("\n")
			continue
		}
		t := tails[a.ID]
		if len(t) > tailItems {
			fmt.Fprintf(&b, "(%d earlier items omitted)\n", len(t)-tailItems)
			t = t[len(t)-tailItems:]
		}
		for _, line := range t {
			b.WriteString("- " + line + "\n")
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

func cut(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > itemRunes {
		return string(r[:itemRunes]) + "…"
	}
	return s
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
