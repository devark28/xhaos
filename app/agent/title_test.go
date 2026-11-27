package agent_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/devark28/xhaos/app/agent"
	"github.com/devark28/xhaos/app/apperr"
	"github.com/devark28/xhaos/app/eventlog"
)

func TestCleanProposal(t *testing.T) {
	for _, bad := range []string{"", "   ", "two\nlines", "tab\there", "bell\a", strings.Repeat("x", agent.MaxTitle+1), "line sep"} {
		if _, err := agent.CleanProposal(bad); err == nil {
			t.Errorf("CleanProposal(%q) accepted", bad)
		}
	}
	if got, err := agent.CleanProposal("  Fix the login form  "); err != nil || got != "Fix the login form" {
		t.Fatalf("CleanProposal = %q, %v", got, err)
	}
	if _, err := agent.CleanProposal(strings.Repeat("é", agent.MaxTitle)); err != nil {
		t.Fatalf("%d characters (multi-byte) refused: %v", agent.MaxTitle, err)
	}
}

func agentNow(t *testing.T, s interface {
	Snapshot(string) (agent.Snapshot, error)
}, id int64) agent.Agent {
	t.Helper()
	snap, err := s.Snapshot(ws)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range snap.Agents {
		if a.ID == id {
			return a
		}
	}
	t.Fatalf("no agent #%d", id)
	return agent.Agent{}
}

func kinds(events []eventlog.Event) []string {
	var out []string
	for _, e := range events {
		out = append(out, e.Kind)
	}
	return out
}

func TestProposalWaitsAndIsReplaced(t *testing.T) {
	s, published, _ := open(t)
	a := mustCreate(t, s, agent.CreateRequest{IdempotencyKey: "a", Title: "Old task"})
	store := agent.NewStore(s.DB(), func(e eventlog.Event) { *published = append(*published, e) })
	*published = nil

	p, err := store.ProposeTitle(ws, a.ID, "First idea", "the task changed")
	if err != nil || p.Applied || p.From != "Old task" || p.Replaced != "" {
		t.Fatalf("first proposal = %+v, %v", p, err)
	}
	got := agentNow(t, s.Agents, a.ID)
	if got.Title != "Old task" || got.TitleProposal == nil || got.TitleProposal.Title != "First idea" || got.TitleProposal.Reason != "the task changed" {
		t.Fatalf("after proposing: %+v %+v", got, got.TitleProposal)
	}
	if p, err = store.ProposeTitle(ws, a.ID, "Second idea", ""); err != nil || p.Replaced != "First idea" {
		t.Fatalf("second proposal = %+v, %v", p, err)
	}
	if got = agentNow(t, s.Agents, a.ID); got.TitleProposal.Title != "Second idea" {
		t.Fatalf("not replaced: %+v", got.TitleProposal)
	}
	// The same title again, or the current one, is refused (for the model).
	if _, err := store.ProposeTitle(ws, a.ID, "Second idea", ""); err == nil {
		t.Fatal("the pending title was proposed again")
	}
	if _, err := store.ProposeTitle(ws, a.ID, "Old task", ""); err == nil {
		t.Fatal("the current title was proposed")
	}
	if _, err := store.ProposeTitle(ws, a.ID, "bad\ntitle", ""); err == nil {
		t.Fatal("a two-line title was accepted")
	}
	if k := kinds(*published); strings.Join(k, ",") != "agent.title_proposal,agent.title_proposal" {
		t.Fatalf("events = %v", k)
	}
}

func TestProposalsAreRateLimited(t *testing.T) {
	s, _, _ := open(t)
	a := mustCreate(t, s, agent.CreateRequest{IdempotencyKey: "a", Title: "Task"})
	store := agent.NewStore(s.DB(), nil)
	for i := range 4 {
		if _, err := store.ProposeTitle(ws, a.ID, "Idea "+string(rune('A'+i)), ""); err != nil {
			t.Fatalf("proposal %d: %v", i+1, err)
		}
	}
	if _, err := store.ProposeTitle(ws, a.ID, "Idea E", ""); err == nil || !strings.Contains(err.Error(), "too many") {
		t.Fatalf("fifth proposal in an hour: %v", err)
	}
}

func TestOwnerRenamesOrKeeps(t *testing.T) {
	s, published, _ := open(t)
	a := mustCreate(t, s, agent.CreateRequest{IdempotencyKey: "a", Title: "Old task"})
	store := agent.NewStore(s.DB(), nil)
	decide := func(key string, rename bool, title string) ([]eventlog.Event, error) {
		return s.Agents.DecideTitle(agent.TitleDecisionRequest{IdempotencyKey: key, WorkspaceID: ws, AgentID: a.ID, Rename: rename, Title: title})
	}
	if _, err := decide("none", true, ""); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("deciding with nothing proposed: %v", err)
	}
	store.ProposeTitle(ws, a.ID, "Kept idea", "")
	if _, err := decide("keep", false, "Kept idea"); err != nil {
		t.Fatal(err)
	}
	if got := agentNow(t, s.Agents, a.ID); got.Title != "Old task" || got.TitleProposal != nil {
		t.Fatalf("after keep: %+v", got)
	}

	store.ProposeTitle(ws, a.ID, "New task", "focus moved")
	// A click on a proposal that was replaced meanwhile does not apply the new one.
	if _, err := decide("stale", true, "Kept idea"); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("stale decision: %v", err)
	}
	*published = nil
	events, err := decide("rename", true, "New task")
	if err != nil {
		t.Fatal(err)
	}
	if k := kinds(events); strings.Join(k, ",") != "agent.title_proposal,agent.title,agent.message" || len(*published) != 3 {
		t.Fatalf("events = %v, published %d", k, len(*published))
	}
	var d agent.TitleData
	json.Unmarshal(events[1].Data, &d)
	if d != (agent.TitleData{Title: "New task", From: "Old task", By: agent.RenameByOwner}) {
		t.Fatalf("title event = %+v", d)
	}
	var note agent.MessageData
	json.Unmarshal(events[2].Data, &note)
	if note.From != agent.FromSystem || note.Body != "Renamed from “Old task” to “New task”: proposed by the agent, approved by you." {
		t.Fatalf("history note = %+v", note)
	}
	if got := agentNow(t, s.Agents, a.ID); got.Title != "New task" || got.TitleProposal != nil {
		t.Fatalf("after rename: %+v", got)
	}
	// The note is part of the agent's history (its transcript).
	snap, _ := s.Agents.Snapshot(ws)
	found := false
	for _, e := range snap.Transcript {
		found = found || e.Kind == agent.EventMessage && strings.Contains(string(e.Data), "Renamed from")
	}
	if !found {
		t.Fatal("the rename is not in the transcript")
	}
	// Retrying the same request returns the same events without new ones.
	if again, err := decide("rename", true, "New task"); err != nil || len(again) != 3 || len(*published) != 3 {
		t.Fatalf("retry = %v, %v, published %d", again, err, len(*published))
	}
}

func TestDangerAndSettingApplyAtOnce(t *testing.T) {
	s, _, _ := open(t)
	a := mustCreate(t, s, agent.CreateRequest{IdempotencyKey: "a", Title: "Old task"})
	b := mustCreate(t, s, agent.CreateRequest{IdempotencyKey: "b", Title: "Other task"})
	store := agent.NewStore(s.DB(), nil)

	// Dangerously allowed: applied at once, replacing a pending proposal.
	store.ProposeTitle(ws, a.ID, "Pending idea", "")
	if err := store.SetDanger(ws, a.ID, &agent.Danger{Until: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	p, err := store.ProposeTitle(ws, a.ID, "Dangerous rename", "")
	if err != nil || !p.Applied || p.By != agent.RenameByDanger || p.Replaced != "Pending idea" {
		t.Fatalf("danger proposal = %+v, %v", p, err)
	}
	if got := agentNow(t, s.Agents, a.ID); got.Title != "Dangerous rename" || got.TitleProposal != nil {
		t.Fatalf("after danger rename: %+v", got)
	}

	// The setting is off by default; on, every agent renames itself.
	if st, _ := s.Agents.Settings(ws); st.SelfRename {
		t.Fatal("self-rename is on by default")
	}
	if p, _ := store.ProposeTitle(ws, b.ID, "Asked first", ""); p.Applied {
		t.Fatal("applied without danger or the setting")
	}
	if st, err := s.Agents.SaveSettings(agent.SaveSettingsRequest{WorkspaceID: ws, AgentSettings: agent.AgentSettings{SelfRename: true}}); err != nil || !st.SelfRename {
		t.Fatalf("save settings = %+v, %v", st, err)
	}
	p, err = store.ProposeTitle(ws, b.ID, "Renamed by setting", "")
	if err != nil || !p.Applied || p.By != agent.RenameBySetting {
		t.Fatalf("setting proposal = %+v, %v", p, err)
	}
	if got := agentNow(t, s.Agents, b.ID); got.Title != "Renamed by setting" || got.TitleProposal != nil {
		t.Fatalf("after setting rename: %+v", got)
	}
	notes := 0
	evs, _ := s.Events.EventsAfter(ws, 0, 0)
	for _, e := range evs {
		var m agent.MessageData
		if e.Kind == agent.EventMessage && json.Unmarshal(e.Data, &m) == nil {
			switch {
			case strings.Contains(m.Body, "“Old task” to “Dangerous rename”: proposed by the agent, applied automatically (dangerously allow is on)"):
				notes++
			case strings.Contains(m.Body, "“Other task” to “Renamed by setting”: proposed by the agent, applied automatically (your setting"):
				notes++
			}
		}
	}
	if notes != 2 {
		t.Fatalf("history notes = %d, want 2", notes)
	}
	if _, err := s.Agents.SaveSettings(agent.SaveSettingsRequest{WorkspaceID: "nope"}); err == nil {
		t.Fatal("settings saved for a missing workspace")
	}
}
