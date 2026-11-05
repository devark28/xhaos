package live

import "testing"

// Reply text never costs a client its stream; a committed event it cannot
// take does (it reconnects and catches up from the log).
func TestLossyMessagesNeverDropAClient(t *testing.T) {
	h := NewHub()
	c := make(chan message, 2)
	h.clients[c] = struct{}{}
	for i := 0; i < 10; i++ {
		h.PublishLossy("xhaos:delta", i)
	}
	if h.Clients() != 1 || len(c) != 2 {
		t.Fatalf("after deltas: %d clients, %d queued", h.Clients(), len(c))
	}
	h.Publish("xhaos:event", "e")
	if h.Clients() != 0 {
		t.Fatal("a client that cannot take an event stays connected")
	}
}
