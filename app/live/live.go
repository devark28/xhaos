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

type Hub struct {
	mu      sync.Mutex
	clients map[chan message]struct{}
}

func NewHub() *Hub { return &Hub{clients: map[chan message]struct{}{}} }

// Publish sends data, as JSON, to every connected client under name. A
// client too far behind is dropped: it reconnects and catches up from the
// event log, so no committed event is lost to it.
func (h *Hub) Publish(name string, data any) { h.send(name, data, false) }

// PublishLossy sends data that may be skipped (streamed reply text): a
// client whose queue is full misses this message but stays connected. The
// saved reply replaces the streamed text anyway.
func (h *Hub) PublishLossy(name string, data any) { h.send(name, data, true) }

func (h *Hub) send(name string, data any, lossy bool) {
	raw, err := json.Marshal(data)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		select {
		case c <- message{name, raw}:
		default:
			if lossy {
				continue
			}
			delete(h.clients, c)
			close(c)
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
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{}) // a stream outlives the server's write timeout
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	c := make(chan message, buffer)
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		if _, ok := h.clients[c]; ok {
			delete(h.clients, c)
			close(c)
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
		case m, ok := <-c:
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
