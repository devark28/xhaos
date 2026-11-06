// Package live streams committed events and reply deltas to signed-in pages
// as Server-Sent Events (GET /xhaos/events). It replaces the Wails event
// WebSocket for Xhaos's own events because that socket cannot be put behind
// the session check; this handler sits behind it.
//
// Delivery is best effort: a client that falls behind is disconnected, and
// its page catches up from the event log (EventsAfter) when it reconnects.
package live

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

const buffer = 512 // messages queued per client before it is dropped

type message struct {
	name string
	data []byte
}

// A client is one page's stream, for one workspace.
type client struct {
	ch        chan message
	workspace string
}

type Hub struct {
	mu      sync.Mutex
	clients map[*client]struct{}
	// Workspace says which workspace a request's session is in; a stream
	// gets only that workspace's messages. Nil: every message (tests).
	Workspace func(*http.Request) string
}

func NewHub() *Hub { return &Hub{clients: map[*client]struct{}{}} }

// Publish sends data, as JSON, to the clients of workspace ws under name. A
// client too far behind is dropped: it reconnects and catches up from the
// event log, so no committed event is lost to it.
func (h *Hub) Publish(ws, name string, data any) { h.send(ws, name, data, false) }

// PublishLossy sends data that may be skipped (streamed reply text): a
// client whose queue is full misses this message but stays connected. The
// saved reply replaces the streamed text anyway.
func (h *Hub) PublishLossy(ws, name string, data any) { h.send(ws, name, data, true) }

func (h *Hub) send(ws, name string, data any, lossy bool) {
	raw, err := json.Marshal(data)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		if c.workspace != "" && c.workspace != ws {
			continue
		}
		select {
		case c.ch <- message{name, raw}:
		default:
			if lossy {
				continue
			}
			delete(h.clients, c)
			close(c.ch)
		}
	}
}

// Clients is the number of connected streams.
func (h *Hub) Clients() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}

func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	c := &client{ch: make(chan message, buffer)}
	if h.Workspace != nil {
		if c.workspace = h.Workspace(r); c.workspace == "" {
			http.Error(w, "no workspace", http.StatusUnauthorized)
			return
		}
	}
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{}) // a stream outlives the server's write timeout
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		if _, ok := h.clients[c]; ok {
			delete(h.clients, c)
			close(c.ch)
		}
		h.mu.Unlock()
	}()
	fmt.Fprint(w, "retry: 1000\n: connected\n\n")
	_ = rc.Flush()
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
		case m, ok := <-c.ch:
			if !ok {
				return
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", m.name, m.data); err != nil {
				return
			}
		}
		if err := rc.Flush(); err != nil {
			return
		}
	}
}
