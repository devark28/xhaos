package agent_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/devark28/xhaos/app/agent"
	"github.com/devark28/xhaos/app/apperr"
	"github.com/devark28/xhaos/app/eventlog"
	"github.com/devark28/xhaos/app/llm"
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
		before = page.Before
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

// A restored agent is idle and takes messages again; its subagents stay
// terminated, and one comes back only after its parent.
func TestRestore(t *testing.T) {
	s, _, _ := open(t)
	parent := mustCreate(t, s, agent.CreateRequest{IdempotencyKey: "p", Prompt: "Parent"})
	child := mustCreate(t, s, agent.CreateRequest{IdempotencyKey: "c", ParentID: parent.ID, Title: "Child"})
	if _, err := s.Agents.Terminate(agent.ControlRequest{IdempotencyKey: "t", WorkspaceID: ws, AgentID: parent.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Agents.Message(agent.MessageRequest{IdempotencyKey: "m0", WorkspaceID: ws, AgentIDs: []int64{parent.ID}, Body: "hi"}); err == nil {
		t.Fatal("a terminated agent took a message")
	}
	if _, err := s.Agents.Restore(agent.ControlRequest{IdempotencyKey: "r0", WorkspaceID: ws, AgentID: child.ID}); !errors.Is(err, apperr.Conflict) || !strings.Contains(err.Error(), "restore #1 first") {
		t.Fatalf("restoring the child first: %v", err)
	}
	ev, err := s.Agents.Restore(agent.ControlRequest{IdempotencyKey: "r1", WorkspaceID: ws, AgentID: parent.ID})
	if err != nil || ev.Kind != agent.EventStatus || !strings.Contains(string(ev.Data), agent.StatusIdle) {
		t.Fatalf("restore = %+v %v", ev, err)
	}
	if again, err := s.Agents.Restore(agent.ControlRequest{IdempotencyKey: "r1", WorkspaceID: ws, AgentID: parent.ID}); err != nil || again.Seq != ev.Seq {
		t.Fatalf("a retried restore: %+v %v, want the same event", again, err)
	}
	if _, err := s.Agents.Restore(agent.ControlRequest{IdempotencyKey: "r2", WorkspaceID: ws, AgentID: parent.ID}); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("restoring an agent that isn't terminated: %v", err)
	}
	snap, _ := s.Agents.Snapshot(ws)
	status := map[int64]string{}
	for _, a := range snap.Agents {
		status[a.ID] = a.Status
	}
	if status[parent.ID] != agent.StatusIdle || status[child.ID] != agent.StatusTerminated {
		t.Fatalf("statuses = %v", status)
	}
	if _, ok := snap.Older[parent.ID]; ok && len(snap.Transcript) == 0 {
		t.Errorf("the restored agent's history is not in the snapshot")
	}
	if _, err := s.Agents.Message(agent.MessageRequest{IdempotencyKey: "m1", WorkspaceID: ws, AgentIDs: []int64{parent.ID}, Body: "carry on"}); err != nil {
		t.Fatalf("message after restore: %v", err)
	}
	if _, err := s.Agents.Restore(agent.ControlRequest{IdempotencyKey: "r3", WorkspaceID: ws, AgentID: child.ID}); err != nil {
		t.Fatalf("restoring the child after its parent: %v", err)
	}
}

// Pages get a reply's text, not what the model replays: tool results are
// left out, replies lose their native copy and their calls' arguments.
func TestSnapshotSlimsReplies(t *testing.T) {
	s, _, _ := open(t)
	store := agent.NewStore(s.DB(), nil)
	a := mustCreate(t, s, agent.CreateRequest{IdempotencyKey: "a", Prompt: "Write it"})
	big := strings.Repeat("x", 20000)
	reply := agent.MessageData{From: agent.FromAgent, Body: "Writing the file.", Provider: "anthropic", Raw: json.RawMessage(`{"thinking":"` + big + `"}`),
		Calls: []llm.ToolCall{{ID: "c1", Name: "write_file", Input: json.RawMessage(`{"content":"` + big + `"}`)}}}
	for _, m := range []agent.MessageData{reply, {From: agent.FromTool, Body: big}} {
		if err := store.Append(ws, a.ID, m); err != nil {
			t.Fatal(err)
		}
	}
	snap, err := s.Agents.Snapshot(ws)
	if err != nil {
		t.Fatal(err)
	}
	var got []agent.MessageData
	size := 0
	for _, e := range snap.Transcript {
		var m agent.MessageData
		json.Unmarshal(e.Data, &m)
		got = append(got, m)
		size += len(e.Data)
	}
	if len(got) != 2 || got[1].From != agent.FromAgent {
		t.Fatalf("transcript = %+v, want the prompt and the reply (no tool result)", got)
	}
	r := got[1]
	if r.Body != "Writing the file." || !empty(r.Raw) || len(r.Calls) != 1 || r.Calls[0].Name != "write_file" || !empty(r.Calls[0].Input) {
		t.Errorf("reply as sent = %+v", r)
	}
	if size > 1000 {
		t.Errorf("snapshot carries %d bytes of transcript for a short exchange", size)
	}
	// The model still has everything.
	msgs, _ := store.Messages(ws, a.ID)
	if last := msgs[len(msgs)-1]; last.From != agent.FromTool || len(msgs[len(msgs)-2].Raw) < 20000 {
		t.Errorf("the log lost data: %d messages", len(msgs))
	}
}

func empty(j json.RawMessage) bool { return len(j) == 0 || string(j) == "null" }

// A finished board call's result (the agent.tool event's Output) is what the
// page builds its "added … to <board>" line from. Slimming touches message
// events only, so the result comes back whole in the snapshot and when the
// call has paged out of it, while the reply's call input and the tool result
// message are slimmed away as before.
func TestSlimKeepsToolResults(t *testing.T) {
	s, _, _ := open(t)
	store := agent.NewStore(s.DB(), nil)
	a := mustCreate(t, s, agent.CreateRequest{IdempotencyKey: "a", Prompt: "Draw the plan"})
	input := json.RawMessage(`{"board":2,"path":"shots/plot.png","caption":"Latency"}`)
	if err := store.Append(ws, a.ID, agent.MessageData{From: agent.FromAgent, Body: "Adding the image.", Raw: json.RawMessage(`{"native":true}`), Calls: []llm.ToolCall{{ID: "img", Name: "board_add_image", Input: input}}}); err != nil {
		t.Fatal(err)
	}
	output := `Added image plot.png to board #2 "Board 2", at (240, 0), 120×80, as group g (3 elements). It shows on open pages now.`
	done := agent.ToolData{CallID: "img", Name: "board_add_image", Effect: "write", Summary: `board #2 "Board 2": plot.png`, State: agent.ToolDone, Output: output}
	if err := store.RecordTool(ws, a.ID, done); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(ws, a.ID, agent.MessageData{From: agent.FromTool, Results: []llm.ToolResult{{CallID: "img", Content: output}}}); err != nil {
		t.Fatal(err)
	}
	check := func(where string, events []eventlog.Event) {
		t.Helper()
		var found bool
		for _, e := range events {
			switch e.Kind {
			case agent.EventTool:
				var d agent.ToolData
				if err := json.Unmarshal(e.Data, &d); err != nil {
					t.Fatal(err)
				}
				if d.CallID == "img" && d.State == agent.ToolDone {
					found = true
					if d != done {
						t.Errorf("%s: the done event changed: %+v", where, d)
					}
				}
			case agent.EventMessage:
				var m agent.MessageData
				json.Unmarshal(e.Data, &m)
				if m.From == agent.FromTool {
					t.Errorf("%s: the tool result message was not dropped", where)
				}
				for _, c := range m.Calls {
					if (len(c.Input) != 0 && string(c.Input) != "null") || m.Raw != nil { // a dropped input reads back as null
						t.Errorf("%s: the reply kept its call input or native copy", where)
					}
				}
			}
		}
		if !found {
			t.Errorf("%s: the board call's done event is missing", where)
		}
	}
	snap, err := s.Agents.Snapshot(ws)
	if err != nil {
		t.Fatal(err)
	}
	check("snapshot", snap.Transcript)
	// Enough replies after it that the call pages out of the snapshot.
	for i := range agent.TranscriptPage + 5 {
		if err := store.Append(ws, a.ID, agent.MessageData{From: agent.FromAgent, Body: fmt.Sprintf("later %d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	snap, _ = s.Agents.Snapshot(ws)
	for _, e := range snap.Transcript {
		if e.Kind == agent.EventTool {
			t.Fatalf("the call is still in the snapshot; the test needs it paged out")
		}
	}
	page, err := s.Agents.TranscriptBefore(ws, a.ID, snap.Older[a.ID], 0)
	if err != nil {
		t.Fatal(err)
	}
	check("older page", page.Events)
}
