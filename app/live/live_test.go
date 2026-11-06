package live

import "testing"

// Reply text never costs a client its stream; a committed event it cannot
// take does (it reconnects and catches up from the log).
func TestLossyMessagesNeverDropAClient(t *testing.T) {
	h := NewHub()
	c := &client{ch: make(chan message, 2), workspace: "a"}
	h.clients[c] = struct{}{}
	for i := 0; i < 10; i++ {
		h.PublishLossy("a", "xhaos:delta", i)
	}
	if h.Clients() != 1 || len(c.ch) != 2 {
		t.Fatalf("after deltas: %d clients, %d queued", h.Clients(), len(c.ch))
	}
	h.Publish("a", "xhaos:event", "e")
	if h.Clients() != 0 {
		t.Fatal("a client that cannot take an event stays connected")
	}
}

// A stream gets only its own workspace's messages.
func TestWorkspacesSeeOnlyTheirOwn(t *testing.T) {
	h := NewHub()
	a := &client{ch: make(chan message, 8), workspace: "a"}
	b := &client{ch: make(chan message, 8), workspace: "b"}
	h.clients[a], h.clients[b] = struct{}{}, struct{}{}
	h.Publish("a", "xhaos:event", "for a")
	h.PublishLossy("b", "xhaos:delta", "for b")
	if len(a.ch) != 1 || len(b.ch) != 1 || string((<-a.ch).data) != `"for a"` || string((<-b.ch).data) != `"for b"` {
		t.Fatal("a message reached another workspace's stream")
	}
}
