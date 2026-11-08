package agent_test

import (
	"fmt"
	"testing"

	"github.com/devark28/xhaos/app/agent"
	"github.com/devark28/xhaos/app/eventlog"
)

// A snapshot carries each agent's last TranscriptPage events; older ones
// come a page at a time, and a call still waiting for the owner always
// comes with the snapshot.
func TestSnapshotPagesTranscripts(t *testing.T) {
	s, _, _ := open(t)
	store := agent.NewStore(s.DB(), nil)
	long := mustCreate(t, s, agent.CreateRequest{IdempotencyKey: "a", Prompt: "Long task"})
	short := mustCreate(t, s, agent.CreateRequest{IdempotencyKey: "b", Prompt: "Short task"})
	// The long agent: a question asked early, still unanswered, then 129
	// replies: 131 transcript events with the prompt.
	if err := store.RecordTool(ws, long.ID, agent.ToolData{CallID: "old", Name: "ask_user", State: agent.ToolPending}); err != nil {
		t.Fatal(err)
	}
	for i := range 129 {
		if err := store.Append(ws, long.ID, agent.MessageData{From: agent.FromAgent, Body: fmt.Sprintf("reply %d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetStatus(ws, long.ID, agent.StatusAttention); err != nil {
		t.Fatal(err)
	}

	snap, err := s.Agents.Snapshot(ws)
	if err != nil {
		t.Fatal(err)
	}
	per := map[int64][]int64{}
	for i, e := range snap.Transcript {
		if i > 0 && e.Seq <= snap.Transcript[i-1].Seq {
			t.Fatalf("transcript out of order at %d", i)
		}
		per[e.AgentID] = append(per[e.AgentID], e.Seq)
	}
	if n := len(per[short.ID]); n != 1 {
		t.Errorf("short agent: %d events, want its prompt", n)
	}
	if _, ok := snap.Older[short.ID]; ok {
		t.Errorf("short agent marked as having older events")
	}
	// The waiting question, then the last 50.
	if n := len(per[long.ID]); n != agent.TranscriptPage+1 {
		t.Fatalf("long agent: %d events, want %d + the waiting call", n, agent.TranscriptPage)
	}
	var waiting eventlog.Event
	for _, e := range snap.Transcript {
		if e.AgentID == long.ID {
			waiting = e
			break
		}
	}
	if waiting.Kind != agent.EventTool {
		t.Fatalf("the waiting call is not in the snapshot: %+v", waiting)
	}
	before := snap.Older[long.ID]
	if before != per[long.ID][1] {
		t.Fatalf("older = %d, want the first of the page sent (%d)", before, per[long.ID][1])
	}

	// Paging back: 50, then the remaining 31 (prompt, question, 29 replies).
	var got int
	for pages := 0; ; pages++ {
		page, err := s.Agents.TranscriptBefore(ws, long.ID, before, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range page.Events {
			if e.Seq >= before || e.AgentID != long.ID {
				t.Fatalf("page event %d (agent %d) not before %d", e.Seq, e.AgentID, before)
			}
		}
		got += len(page.Events)
		if !page.More {
			if pages != 1 || len(page.Events) != 31 {
				t.Errorf("last page: page %d with %d events, want page 1 with 31", pages, len(page.Events))
			}
			break
		}
		before = page.Events[0].Seq
	}
	if got+agent.TranscriptPage != 131 {
		t.Errorf("paged %d + %d events, want 131", got, agent.TranscriptPage)
	}
	// Another workspace's agent IDs reach nothing.
	if page, err := s.Agents.TranscriptBefore("other", long.ID, 1<<40, 0); err != nil || len(page.Events) != 0 {
		t.Errorf("other workspace: %+v %v", page, err)
	}
	// Once answered, the old call is no longer carried.
	if err := store.RecordTool(ws, long.ID, agent.ToolData{CallID: "old", Name: "ask_user", State: agent.ToolDenied}); err != nil {
		t.Fatal(err)
	}
	snap, _ = s.Agents.Snapshot(ws)
	for _, e := range snap.Transcript {
		if e.Seq == waiting.Seq {
			t.Errorf("an answered call is still carried")
		}
	}
}

// A terminated agent's history is left out of the snapshot; its card loads
// it from the end when opened.
func TestSnapshotLeavesOutTerminatedHistory(t *testing.T) {
	s, _, _ := open(t)
	done := mustCreate(t, s, agent.CreateRequest{IdempotencyKey: "a", Prompt: "Finished task"})
	live := mustCreate(t, s, agent.CreateRequest{IdempotencyKey: "b", Prompt: "Ongoing task"})
	if _, err := s.Agents.Terminate(agent.ControlRequest{IdempotencyKey: "t", WorkspaceID: ws, AgentID: done.ID}); err != nil {
		t.Fatal(err)
	}
	snap, err := s.Agents.Snapshot(ws)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range snap.Transcript {
		if e.AgentID == done.ID {
			t.Errorf("the terminated agent's event %d (%s) is in the snapshot", e.Seq, e.Kind)
		}
	}
	if len(snap.Transcript) != 1 || snap.Transcript[0].AgentID != live.ID {
		t.Errorf("snapshot transcript = %+v, want the live agent's prompt", snap.Transcript)
	}
	if snap.Older[done.ID] != snap.Seq+1 {
		t.Fatalf("older[%d] = %d, want %d (from the end)", done.ID, snap.Older[done.ID], snap.Seq+1)
	}
	page, err := s.Agents.TranscriptBefore(ws, done.ID, snap.Older[done.ID], 0)
	if err != nil || page.More || len(page.Events) != 1 {
		t.Fatalf("opening its card: %+v %v, want its prompt", page, err)
	}
}
