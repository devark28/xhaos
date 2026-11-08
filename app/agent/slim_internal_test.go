package agent

import (
	"encoding/json"
	"testing"

	"github.com/devark28/xhaos/app/eventlog"
	"github.com/devark28/xhaos/app/llm"
)

// forReaders and slim change message events only: a tool event (a board
// call's result, which the page's "added … to <board>" line reads) passes
// byte for byte.
func TestForReadersLeavesToolEventsWhole(t *testing.T) {
	tool, _ := json.Marshal(ToolData{CallID: "n", Name: "board_add_note", Effect: "write", Summary: `board #2 "Board 2": hi`, State: ToolDone,
		Output: `Added a note to board #2 "Board 2", at (0, 160), 200×80, as group g (3 elements). It shows on open pages now.`})
	reply, _ := json.Marshal(MessageData{From: FromAgent, Body: "Adding a note.", Raw: json.RawMessage(`{"x":1}`), Calls: []llm.ToolCall{{ID: "n", Name: "board_add_note", Input: json.RawMessage(`{"board":2,"text":"hi"}`)}}})
	result, _ := json.Marshal(MessageData{From: FromTool, Results: []llm.ToolResult{{CallID: "n", Content: "Added a note"}}})
	events := []eventlog.Event{{Seq: 1, Kind: EventMessage, Data: reply}, {Seq: 2, Kind: EventTool, Data: tool}, {Seq: 3, Kind: EventMessage, Data: result}}
	if e, ok := forReaders(events[1]); !ok || string(e.Data) != string(tool) {
		t.Errorf("forReaders changed a tool event: %s", e.Data)
	}
	out := slim(events)
	if len(out) != 2 || out[1].Kind != EventTool || string(out[1].Data) != string(tool) {
		t.Fatalf("slim: %+v", out)
	}
	var m MessageData
	json.Unmarshal(out[0].Data, &m)
	if m.Raw != nil || (len(m.Calls[0].Input) != 0 && string(m.Calls[0].Input) != "null") || m.Calls[0].Name != "board_add_note" {
		t.Errorf("the reply was not slimmed: %s", out[0].Data)
	}
}
