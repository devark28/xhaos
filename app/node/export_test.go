package node

import (
	"time"

	"github.com/coder/websocket"
)

// Kick ends a node's connection the way a network failure would; the node
// reconnects.
func (h *Hub) Kick(id int64) {
	h.mu.Lock()
	c := h.conns[id]
	h.mu.Unlock()
	if c != nil {
		c.c.Close(websocket.StatusGoingAway, "test")
	}
}

// JoinHeld reports whether wrong join codes from addr hold its joins back
// (judged as of just before its last strike, so the delay's length does not
// matter).
func (h *Hub) JoinHeld(addr string) bool {
	return h.joinLimit.Wait(addr, time.Now().Add(-time.Minute)) > 0
}
