package agent

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/devark28/xhaos/app/apperr"
	"github.com/devark28/xhaos/app/eventlog"
	"github.com/devark28/xhaos/app/idempotency"
	"github.com/devark28/xhaos/app/sqlitedb"
)

// An agent may propose a new title for itself (propose_title, package run).
// The proposal never blocks the agent: it waits on the agent as pending
// until the owner renames or keeps, or it is applied at once while the
// agent is dangerously allowed or the owner's setting lets agents rename
// themselves. A new proposal replaces a pending one.

// EventTitleProposal records a proposal (data: TitleProposal) or its end
// (data: null): decided, replaced by a rename, or kept.
const EventTitleProposal = "agent.title_proposal"

// Who applied a rename the agent proposed (TitleData.By).
const (
	RenameByOwner   = "owner"   // the owner (or an MCP client for them: TitleData.Via)
	RenameByDanger  = "danger"  // automatically: the agent is dangerously allowed
	RenameBySetting = "setting" // automatically: the owner's setting allows it
)

// MaxTitle is the longest title an agent may propose, in characters.
const MaxTitle = 80

// Proposals an agent may make per hour (applied or not).
const (
	proposalsPerHour = 4
	proposalWindow   = time.Hour
)

// TitleProposal is a pending title the agent proposed.
type TitleProposal struct {
	Title  string    `json:"title"`
	Reason string    `json:"reason,omitempty"`
	At     time.Time `json:"at"`
}

// TitleData is an agent.title event: the title in place now. A rename the
// agent proposed also says what it replaced and who applied it.
type TitleData struct {
	Title string `json:"title"`
	From  string `json:"from,omitempty"`
	By    string `json:"by,omitempty"`  // RenameBy*; empty for the model's first title
	Via   string `json:"via,omitempty"` // the MCP client (token name) that decided for the owner
}

// CleanProposal checks a proposed title: one line of 1 to MaxTitle
// characters, no control characters. It returns it trimmed.
func CleanProposal(title string) (string, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return "", errors.New("the title is empty")
	}
	if n := utf8.RuneCountInString(title); n > MaxTitle {
		return "", fmt.Errorf("the title is %d characters; keep it to %d", n, MaxTitle)
	}
	for _, r := range title {
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			return "", errors.New("the title must be one line with no control characters")
		}
	}
	return title, nil
}

// renameNote is the history line of an applied rename.
func renameNote(d TitleData) string {
	by := "approved by you"
	switch {
	case d.By == RenameByDanger:
		by = "applied automatically (dangerously allow is on)"
	case d.By == RenameBySetting:
		by = "applied automatically (your setting lets agents rename themselves)"
	case d.Via != "":
		by = fmt.Sprintf("approved via MCP (%s)", d.Via)
	}
	return fmt.Sprintf("Renamed from “%s” to “%s”: proposed by the agent, %s.", d.From, d.Title, by)
}

// applyTitle renames a to title, clears its proposal, and records the rename
// in its history (the title event, then a note for the owner).
func applyTitle(tx *sql.Tx, a Agent, title, by, via string, at time.Time) ([]eventlog.Event, error) {
	if _, err := tx.Exec(`UPDATE agents SET title = ?, title_auto = 0, title_proposal = '', title_reason = '', title_proposed_at = '' WHERE workspace_id = ? AND id = ?`,
		title, a.WorkspaceID, a.ID); err != nil {
		return nil, err
	}
	d := TitleData{Title: title, From: a.Title, By: by, Via: via}
	ev, err := eventlog.Append(tx, a.WorkspaceID, a.ID, EventTitle, d, at)
	if err != nil {
		return nil, err
	}
	note, err := eventlog.Append(tx, a.WorkspaceID, a.ID, EventMessage, MessageData{From: FromSystem, Body: renameNote(d), Via: via}, at)
	if err != nil {
		return nil, err
	}
	return []eventlog.Event{ev, note}, nil
}

// clearProposal drops a's pending proposal, if any (a null proposal event).
func clearProposal(tx *sql.Tx, a Agent, at time.Time) ([]eventlog.Event, error) {
	if a.TitleProposal == nil {
		return nil, nil
	}
	if _, err := tx.Exec(`UPDATE agents SET title_proposal = '', title_reason = '', title_proposed_at = '' WHERE workspace_id = ? AND id = ?`, a.WorkspaceID, a.ID); err != nil {
		return nil, err
	}
	ev, err := eventlog.Append(tx, a.WorkspaceID, a.ID, EventTitleProposal, nil, at)
	if err != nil {
		return nil, err
	}
	return []eventlog.Event{ev}, nil
}

// ---- the agent's side (Store) -------------------------------------------------

// Proposed is what became of a proposal.
type Proposed struct {
	// Applied: the agent was renamed at once; By says why (RenameByDanger or
	// RenameBySetting). Otherwise the proposal waits for the owner.
	Applied bool   `json:"applied"`
	By      string `json:"by,omitempty"`
	From    string `json:"from"`
	// Replaced is the pending proposal this one took the place of.
	Replaced string `json:"replaced,omitempty"`
}

// proposals counts each agent's recent proposals (in memory: a restart
// forgives them).
type proposals struct {
	mu   sync.Mutex
	recs map[key][]time.Time
}

type key struct {
	ws string
	id int64
}

// take records a proposal at now, or reports that the agent made too many.
func (p *proposals) take(k key, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.recs == nil {
		p.recs = map[key][]time.Time{}
	}
	var kept []time.Time
	for _, t := range p.recs[k] {
		if now.Sub(t) < proposalWindow {
			kept = append(kept, t)
		}
	}
	if len(kept) >= proposalsPerHour {
		p.recs[k] = kept
		return false
	}
	p.recs[k] = append(kept, now)
	return true
}

// ProposeTitle records the agent's proposal to rename itself. It never
// waits: while the agent is dangerously allowed, or the workspace's setting
// lets agents rename themselves, the rename is applied at once; otherwise
// the proposal is pending (replacing an earlier one) for the owner.
// Errors are for the model.
func (st *Store) ProposeTitle(workspaceID string, id int64, title, reason string) (Proposed, error) {
	title, err := CleanProposal(title)
	if err != nil {
		return Proposed{}, err
	}
	reason = oneLine(reason, 200)
	var out Proposed
	tx, err := st.db.Begin()
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	a, err := get(tx, workspaceID, id)
	if err != nil {
		return out, err
	}
	if a.Status == StatusTerminated {
		return out, errors.New("this agent is terminated")
	}
	out.From = a.Title
	if title == a.Title {
		return out, fmt.Errorf("“%s” is already your title", a.Title)
	}
	if p := a.TitleProposal; p != nil {
		if p.Title == title {
			return out, fmt.Errorf("“%s” is already proposed and waiting for the owner; keep working", title)
		}
		out.Replaced = p.Title
	}
	now := st.now()
	if !st.proposals.take(key{workspaceID, id}, now) {
		return out, fmt.Errorf("too many title proposals: at most %d an hour. Keep working; propose again later only if your task has really changed", proposalsPerHour)
	}
	var events []eventlog.Event
	switch {
	case a.DangerActive(now):
		out.Applied, out.By = true, RenameByDanger
	case selfRename(tx, workspaceID):
		out.Applied, out.By = true, RenameBySetting
	}
	if out.Applied {
		if events, err = applyTitle(tx, a, title, out.By, "", now); err != nil {
			return out, err
		}
		if a.TitleProposal != nil {
			// The replaced proposal ends with the rename.
			ev, err := eventlog.Append(tx, workspaceID, id, EventTitleProposal, nil, now)
			if err != nil {
				return out, err
			}
			events = append([]eventlog.Event{ev}, events...)
		}
	} else {
		p := TitleProposal{Title: title, Reason: reason, At: now}
		if _, err := tx.Exec(`UPDATE agents SET title_proposal = ?, title_reason = ?, title_proposed_at = ? WHERE workspace_id = ? AND id = ?`,
			p.Title, p.Reason, sqlitedb.Stamp(now), workspaceID, id); err != nil {
			return out, err
		}
		ev, err := eventlog.Append(tx, workspaceID, id, EventTitleProposal, p, now)
		if err != nil {
			return out, err
		}
		events = append(events, ev)
	}
	if err := tx.Commit(); err != nil {
		return out, err
	}
	for _, e := range events {
		st.publish(e)
	}
	return out, nil
}

// oneLine keeps s to one trimmed line of at most n characters.
func oneLine(s string, n int) string {
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsControl(r) || r == ' ' || r == ' ' }), " ")
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		s = strings.TrimSpace(string(r[:n])) + "…"
	}
	return s
}

// ---- the owner's side (Service) ------------------------------------------------

// TitleDecisionRequest renames an agent to its pending proposal, or keeps
// its title. Title, when set, must be the proposal's (it guards against a
// proposal that was replaced while the owner looked at the old one).
type TitleDecisionRequest struct {
	IdempotencyKey string `json:"idempotencyKey"`
	WorkspaceID    string `json:"workspaceId"`
	AgentID        int64  `json:"agentId"`
	Rename         bool   `json:"rename"`
	Title          string `json:"title,omitempty"`
	Via            string `json:"-"` // see TitleData.Via
}

// DecideTitle applies or declines an agent's pending title proposal. It
// does not wake the agent: the agent never waited for it.
func (s *Service) DecideTitle(req TitleDecisionRequest) ([]eventlog.Event, error) {
	var out []eventlog.Event
	replayed, err := idempotency.Run(s.db, "agent.DecideTitle", req.IdempotencyKey, req, &out, func(tx *sql.Tx) error {
		a, err := get(tx, req.WorkspaceID, req.AgentID)
		if err != nil {
			return err
		}
		if a.Status == StatusTerminated {
			return fmt.Errorf("%w: agent #%d is terminated", apperr.Conflict, a.ID)
		}
		p := a.TitleProposal
		if p == nil {
			return fmt.Errorf("%w: agent #%d has no title proposal waiting", apperr.Conflict, a.ID)
		}
		if t := strings.TrimSpace(req.Title); t != "" && t != p.Title {
			return fmt.Errorf("%w: agent #%d now proposes “%s”, not “%s”", apperr.Conflict, a.ID, p.Title, t)
		}
		at := s.now()
		if req.Rename {
			ev, err := eventlog.Append(tx, req.WorkspaceID, a.ID, EventTitleProposal, nil, at)
			if err != nil {
				return err
			}
			more, err := applyTitle(tx, a, p.Title, RenameByOwner, req.Via, at)
			out = append([]eventlog.Event{ev}, more...)
			return err
		}
		out, err = clearProposal(tx, a, at)
		return err
	})
	if err != nil {
		return nil, err
	}
	if !replayed {
		s.emit(out)
	}
	return out, nil
}
