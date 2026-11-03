package agent_test

import (
	"errors"
	"path/filepath"
	"testing"

	"xhaos/app/agent"
	"xhaos/app/apperr"
	"xhaos/app/backend"
	"xhaos/app/eventlog"
	"xhaos/app/policy"
	"xhaos/app/workspace"
)

const ws = workspace.DefaultID

func open(t *testing.T) (*backend.Services, *[]eventlog.Event, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "xhaos.db")
	var published []eventlog.Event
	s, err := backend.Open(path, backend.Options{DisableRuns: true, Publish: func(e eventlog.Event) { published = append(published, e) }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, &published, path
}

func mustCreate(t *testing.T, s *backend.Services, req agent.CreateRequest) agent.Agent {
	t.Helper()
	if req.WorkspaceID == "" {
		req.WorkspaceID = ws
	}
	a, err := s.Agents.Create(req)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestCreateAppendsSequencedEvents(t *testing.T) {
	s, published, _ := open(t)
	a := mustCreate(t, s, agent.CreateRequest{IdempotencyKey: "k1", Title: "Build backend"})
	b := mustCreate(t, s, agent.CreateRequest{IdempotencyKey: "k2", Title: "Review", ParentID: a.ID})
	if a.ID != 1 || b.ID != 2 || b.ParentID != 1 || a.Status != agent.StatusIdle || a.Policy != policy.Default() {
		t.Fatalf("unexpected agents: %+v %+v", a, b)
	}
	if len(*published) != 2 || (*published)[0].Seq != 1 || (*published)[1].Seq != 2 {
		t.Fatalf("unexpected published events: %+v", *published)
	}
	snap, err := s.Agents.Snapshot(ws)
	if err != nil || snap.Seq != 2 || len(snap.Agents) != 2 {
		t.Fatalf("snapshot = %+v, %v", snap, err)
	}
	evs, err := s.Events.EventsAfter(ws, 1, 0)
	if err != nil || len(evs) != 1 || evs[0].Seq != 2 || evs[0].Kind != agent.EventCreated || evs[0].AgentID != 2 {
		t.Fatalf("EventsAfter = %+v, %v", evs, err)
	}
}

func TestIdempotentRetryReturnsSameResultWithoutNewEvents(t *testing.T) {
	s, published, _ := open(t)
	req := agent.CreateRequest{IdempotencyKey: "same", WorkspaceID: ws, Title: "Once"}
	first := mustCreate(t, s, req)
	again := mustCreate(t, s, req)
	if first.ID != again.ID || len(*published) != 1 {
		t.Fatalf("retry created a duplicate: %+v %+v, %d events", first, again, len(*published))
	}
	req.Title = "Different"
	if _, err := s.Agents.Create(req); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("reused key with different request: err = %v, want Conflict", err)
	}
	if _, err := s.Agents.Create(agent.CreateRequest{WorkspaceID: ws, Title: "No key"}); !errors.Is(err, apperr.Invalid) {
		t.Fatalf("missing key: err = %v, want Invalid", err)
	}
}

func TestSubagentCannotExceedParentPolicy(t *testing.T) {
	s, _, _ := open(t)
	parent := mustCreate(t, s, agent.CreateRequest{IdempotencyKey: "p", Title: "Reviewer",
		Policy: &policy.Policy{Access: "ro", Review: "manual", Commit: "blocked"}})
	_, err := s.Agents.Create(agent.CreateRequest{IdempotencyKey: "c1", WorkspaceID: ws, ParentID: parent.ID,
		Title: "Writer", Policy: &policy.Policy{Access: "rw", Review: "manual", Commit: "blocked"}})
	if !errors.Is(err, apperr.Invalid) {
		t.Fatalf("escalating child: err = %v, want Invalid", err)
	}
	child := mustCreate(t, s, agent.CreateRequest{IdempotencyKey: "c2", ParentID: parent.ID, Title: "Inherits"})
	if child.Policy != parent.Policy {
		t.Fatalf("child policy = %+v, want parent's %+v", child.Policy, parent.Policy)
	}
}

func TestMessageIsAllOrNothingAndRetrySafe(t *testing.T) {
	s, published, _ := open(t)
	a := mustCreate(t, s, agent.CreateRequest{IdempotencyKey: "a", Title: "A"})
	req := agent.MessageRequest{IdempotencyKey: "m1", WorkspaceID: ws, AgentIDs: []int64{a.ID, a.ID}, Body: "hi"}
	evs, err := s.Agents.Message(req)
	if err != nil || len(evs) != 1 || evs[0].Kind != agent.EventMessage || evs[0].Seq != 2 {
		t.Fatalf("Message = %+v, %v", evs, err)
	}
	if again, err := s.Agents.Message(req); err != nil || len(again) != 1 || again[0].Seq != 2 || len(*published) != 2 {
		t.Fatalf("retry = %+v, %v; published %d, want 2", again, err, len(*published))
	}
	if _, err := s.Agents.Message(agent.MessageRequest{IdempotencyKey: "m2", WorkspaceID: ws, AgentIDs: []int64{a.ID, 99}, Body: "hi"}); !errors.Is(err, apperr.NotFound) {
		t.Fatalf("unknown recipient: err = %v, want NotFound", err)
	}
	if evs, _ := s.Events.EventsAfter(ws, 0, 0); len(evs) != 2 {
		t.Fatalf("got %d events after failed batch, want 2 (no partial write)", len(evs))
	}
}

func TestStatePersistsAcrossReopen(t *testing.T) {
	s, _, path := open(t)
	mustCreate(t, s, agent.CreateRequest{IdempotencyKey: "a", Title: "Survives restart"})
	s.Close()
	reopened, err := backend.Open(path, backend.Options{DisableRuns: true})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	snap, err := reopened.Agents.Snapshot(ws)
	if err != nil || snap.Seq != 1 || len(snap.Agents) != 1 || snap.Agents[0].Title != "Survives restart" {
		t.Fatalf("after reopen: %+v, %v", snap, err)
	}
}

func TestCreateWithPromptRecordsFirstMessageAndSnapshotCarriesTranscript(t *testing.T) {
	s, published, _ := open(t)
	a := mustCreate(t, s, agent.CreateRequest{IdempotencyKey: "p", Prompt: "\n  Fix the composer\nDetails follow."})
	if a.Title != "Fix the composer" {
		t.Fatalf("derived title = %q", a.Title)
	}
	if len(*published) != 2 || (*published)[1].Kind != agent.EventMessage {
		t.Fatalf("want created+message events, got %+v", *published)
	}
	snap, err := s.Agents.Snapshot(ws)
	if err != nil || snap.Seq != 2 || len(snap.Transcript) != 1 || snap.Transcript[0].AgentID != a.ID {
		t.Fatalf("snapshot = %+v, %v", snap, err)
	}
	if _, err := s.Agents.Create(agent.CreateRequest{IdempotencyKey: "e", WorkspaceID: ws}); !errors.Is(err, apperr.Invalid) {
		t.Fatalf("no title and no prompt: err = %v, want Invalid", err)
	}
}
