package agent

import (
	"database/sql"
	"fmt"

	"github.com/devark28/xhaos/app/apperr"
	"github.com/devark28/xhaos/app/eventlog"
	"github.com/devark28/xhaos/app/idempotency"
	"github.com/devark28/xhaos/app/policy"
	"github.com/devark28/xhaos/app/sqlitedb"
	"github.com/devark28/xhaos/app/workspace"
)

// AgentSettings are a workspace's settings for its agents.
type AgentSettings struct {
	// SelfRename lets agents rename themselves without asking.
	SelfRename bool `json:"selfRename"`
	// DefaultPolicy is the policy of new top-level agents created without
	// one: from the page, or by an MCP client that leaves access, review or
	// commit out. Subagents still inherit their parent's. Commit "allowed"
	// is never a default: it stays the owner's choice, agent by agent.
	DefaultPolicy policy.Policy `json:"defaultPolicy"`
}

// SaveSettingsRequest sets a workspace's AgentSettings. A DefaultPolicy left
// empty keeps the stored one.
type SaveSettingsRequest struct {
	WorkspaceID string `json:"workspaceId"`
	AgentSettings
}

// Settings returns a workspace's agent settings: self-rename off and
// policy.Default until changed.
func (s *Service) Settings(workspaceID string) (AgentSettings, error) {
	return AgentSettings{SelfRename: selfRename(s.db, workspaceID), DefaultPolicy: defaultPolicy(s.db, workspaceID)}, nil
}

// SaveSettings stores a workspace's agent settings.
func (s *Service) SaveSettings(req SaveSettingsRequest) (AgentSettings, error) {
	if _, err := workspace.Get(s.db, req.WorkspaceID); err != nil {
		return AgentSettings{}, err
	}
	p := req.DefaultPolicy
	if p == (policy.Policy{}) {
		p = defaultPolicy(s.db, req.WorkspaceID)
	}
	if err := p.Validate(); err != nil {
		return AgentSettings{}, err
	}
	if p.Commit == "allowed" {
		return AgentSettings{}, fmt.Errorf("%w: commits without review cannot be the default; allow them on one agent instead", apperr.Invalid)
	}
	on := 0
	if req.SelfRename {
		on = 1
	}
	if _, err := s.db.Exec(`INSERT INTO agent_settings (workspace_id, self_rename, default_access, default_review, default_commit) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (workspace_id) DO UPDATE SET self_rename = excluded.self_rename, default_access = excluded.default_access,
			default_review = excluded.default_review, default_commit = excluded.default_commit`,
		req.WorkspaceID, on, p.Access, p.Review, p.Commit); err != nil {
		return AgentSettings{}, err
	}
	return s.Settings(req.WorkspaceID)
}

func selfRename(q sqlitedb.Querier, workspaceID string) bool {
	var on int
	err := q.QueryRow(`SELECT self_rename FROM agent_settings WHERE workspace_id = ?`, workspaceID).Scan(&on)
	return err == nil && on == 1
}

// defaultPolicy is the workspace's policy for new top-level agents:
// policy.Default until the owner sets one.
func defaultPolicy(q sqlitedb.Querier, workspaceID string) policy.Policy {
	var p policy.Policy
	err := q.QueryRow(`SELECT default_access, default_review, default_commit FROM agent_settings WHERE workspace_id = ?`, workspaceID).Scan(&p.Access, &p.Review, &p.Commit)
	if err != nil || p.Validate() != nil {
		return policy.Default()
	}
	return p
}

// EventPolicy records the owner changing an agent's policy (data: PolicyData).
const EventPolicy = "agent.policy"

// PolicyData is an agent.policy event: the policy in place now and the one
// it replaced.
type PolicyData struct {
	Policy policy.Policy `json:"policy"`
	From   policy.Policy `json:"from"`
}

// PolicyRequest sets one agent's policy (SetPolicy).
type PolicyRequest struct {
	IdempotencyKey string        `json:"idempotencyKey"`
	WorkspaceID    string        `json:"workspaceId"`
	AgentID        int64         `json:"agentId"`
	Policy         policy.Policy `json:"policy"`
}

// PolicyText is a policy in words, as the page's chip shows it:
// "rw · manual · commits review".
func PolicyText(p policy.Policy) string {
	return fmt.Sprintf("%s · %s · commits %s", p.Access, p.Review, p.Commit)
}

// SetPolicy changes an agent's policy. It is the owner's alone: bound for
// the page, not offered to MCP clients or agents. The owner may set any valid
// policy, a subagent's above its parent's too (Within limits only what agents
// grant their subagents). Calls already waiting for the owner stay as they
// are; the gate uses the new policy from the next tool call on. The change is
// noted in the agent's history.
func (s *Service) SetPolicy(req PolicyRequest) (Agent, error) {
	if err := req.Policy.Validate(); err != nil {
		return Agent{}, err
	}
	var out Agent
	var events []eventlog.Event
	replayed, err := idempotency.Run(s.db, "agent.SetPolicy", req.IdempotencyKey, req, &out, func(tx *sql.Tx) error {
		a, err := get(tx, req.WorkspaceID, req.AgentID)
		if err != nil {
			return err
		}
		if a.Status == StatusTerminated {
			return fmt.Errorf("%w: agent #%d is terminated", apperr.Conflict, a.ID)
		}
		out = a
		if a.Policy == req.Policy {
			return nil
		}
		if _, err := tx.Exec(`UPDATE agents SET access = ?, review = ?, commit_policy = ? WHERE workspace_id = ? AND id = ?`,
			req.Policy.Access, req.Policy.Review, req.Policy.Commit, req.WorkspaceID, a.ID); err != nil {
			return err
		}
		at := s.now()
		ev, err := eventlog.Append(tx, req.WorkspaceID, a.ID, EventPolicy, PolicyData{Policy: req.Policy, From: a.Policy}, at)
		if err != nil {
			return err
		}
		body := fmt.Sprintf("Policy changed from %s to %s by you. Calls already waiting for you stay as they are; the new policy applies from the next tool call.",
			PolicyText(a.Policy), PolicyText(req.Policy))
		note, err := eventlog.Append(tx, req.WorkspaceID, a.ID, EventMessage, MessageData{From: FromSystem, Body: body}, at)
		if err != nil {
			return err
		}
		events = append(events, ev, note)
		out.Policy = req.Policy
		return nil
	})
	if err != nil {
		return Agent{}, err
	}
	if !replayed {
		s.emit(events)
	}
	return out, nil
}
