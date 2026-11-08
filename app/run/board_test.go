package run_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devark28/xhaos/app/agent"
	"github.com/devark28/xhaos/app/backend"
	"github.com/devark28/xhaos/app/board"
	"github.com/devark28/xhaos/app/llm"
	"github.com/devark28/xhaos/app/llm/sim"
	"github.com/devark28/xhaos/app/policy"
	"github.com/devark28/xhaos/app/provider"
	"github.com/devark28/xhaos/app/tool"
)

// An agent's board calls go through the policy gate like file writes: under
// manual review each waits for the owner (the image's card can show it),
// under auto review they run; either way the additions reach open pages as
// live notices, and the owner's drawing is left alone.
func TestBoardToolsThroughTheGate(t *testing.T) {
	root := t.TempDir()
	var pic bytes.Buffer
	png.Encode(&pic, image.NewRGBA(image.Rect(0, 0, 40, 30)))
	os.MkdirAll(filepath.Join(root, "shots"), 0o755)
	os.WriteFile(filepath.Join(root, "shots", "p.png"), pic.Bytes(), 0o644)
	var mu sync.Mutex
	var notes []board.Notice
	h := setupIn(t, root, func(turn llm.Turn) (llm.Result, error) {
		turn.Model = "scripted-fast"
		return sim.Adapter{}.Stream(context.Background(), llm.Credentials{}, turn, func(string) {})
	}, func(o *backend.Options) {
		o.BoardLive = func(_ string, n board.Notice) { mu.Lock(); notes = append(notes, n); mu.Unlock() }
	})
	h.s.Providers.Save(provider.SaveRequest{WorkspaceID: ws, ID: "f", Kind: "fake", APIKey: "k", Models: []string{"m1"}})
	b, err := h.s.Boards.Create(board.CreateRequest{WorkspaceID: ws, Name: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	owner := json.RawMessage(`{"id":"own","type":"rectangle","x":0,"y":0,"width":300,"height":200,"version":2,"versionNonce":9,"isDeleted":false,"index":"a0"}`)
	if _, err := h.s.Boards.Save(board.SaveRequest{WorkspaceID: ws, ID: b.ID, Source: "page", Elements: []json.RawMessage{owner}}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	notes = nil // the owner's own save
	mu.Unlock()

	manual := policy.Policy{Access: "rw", Review: "manual", Commit: "review"}
	a, _ := h.s.Agents.Create(agent.CreateRequest{IdempotencyKey: "a", WorkspaceID: ws, Prompt: "Put the plan on the board with the image shots/p.png", Provider: "f", Model: "m1", Policy: &manual})
	h.waitStatus(t, a.ID, agent.StatusAttention)
	pending := map[string]agent.ToolData{}
	for id, d := range h.tools(t, a.ID) {
		if d.State == agent.ToolPending {
			pending[id] = d
		}
	}
	names := map[string]bool{}
	for _, d := range pending {
		names[d.Name] = true
		if d.Effect != tool.Write || d.Preview == "" {
			t.Errorf("%s: effect %q preview %q", d.Name, d.Effect, d.Preview)
		}
	}
	if len(pending) != 3 || !names["board_add_note"] || !names["board_add_shapes"] || !names["board_add_image"] {
		t.Fatalf("pending: %+v", pending)
	}
	mu.Lock()
	if len(notes) != 0 {
		t.Errorf("a waiting call changed the board: %+v", notes)
	}
	mu.Unlock()
	for id, d := range pending {
		if d.Name == "board_add_image" {
			img, err := h.s.Boards.StagedImage(ws, a.ID, id)
			// A small file is its own preview.
			if err != nil || !bytes.Equal(img.Data, pic.Bytes()) || img.NoPreview || img.Name != "p.png" || img.Size != int64(pic.Len()) || img.FileType != "image/png" {
				t.Errorf("staged image for the card: %+v %v", img, err)
			}
		}
		h.s.Agents.Decide(agent.DecideRequest{IdempotencyKey: "d" + id, WorkspaceID: ws, AgentID: a.ID, CallID: id, Allow: true})
	}
	waitFor(t, func() bool { return h.lastAgentText(a.ID, "Worked on board") })
	for _, d := range h.tools(t, a.ID) {
		if strings.HasPrefix(d.Name, "board_") && d.State != agent.ToolDone {
			t.Errorf("%s: %s %s", d.Name, d.State, d.Output)
		}
	}
	mu.Lock()
	got := len(notes)
	for _, n := range notes {
		if n.ID != b.ID || n.Source != "agent-"+itoa(a.ID) {
			t.Errorf("notice %+v", n)
		}
	}
	mu.Unlock()
	if got != 3 {
		t.Errorf("%d notices, want 3", got)
	}
	loaded, _ := h.s.Boards.Load(ws, b.ID)
	if len(loaded.Images) != 1 {
		t.Errorf("images: %+v", loaded.Images)
	}
	for _, raw := range loaded.Elements {
		var e struct {
			ID      string `json:"id"`
			Version int    `json:"version"`
		}
		json.Unmarshal(raw, &e)
		if e.ID == "own" && e.Version != 2 {
			t.Errorf("the owner's rectangle changed: %s", raw)
		}
	}

	// Auto review: the same calls run without asking.
	auto := policy.Policy{Access: "rw", Review: "auto", Commit: "review"}
	c, _ := h.s.Agents.Create(agent.CreateRequest{IdempotencyKey: "c", WorkspaceID: ws, Prompt: "Sketch the flow on the board", Provider: "f", Model: "m1", Policy: &auto})
	waitFor(t, func() bool { return h.lastAgentText(c.ID, "Worked on board") })
	for _, d := range h.tools(t, c.ID) {
		if d.State != agent.ToolDone {
			t.Errorf("auto: %s %s", d.Name, d.State)
		}
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (h *harness) lastAgentText(id int64, want string) bool {
	snap, _ := h.s.Agents.Snapshot(ws)
	for _, e := range snap.Transcript {
		var m agent.MessageData
		if e.AgentID == id && e.Kind == agent.EventMessage && json.Unmarshal(e.Data, &m) == nil && m.From == agent.FromAgent && strings.Contains(m.Body, want) {
			return true
		}
	}
	return false
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }
