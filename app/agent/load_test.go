package agent_test

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/devark28/xhaos/app/agent"
	"github.com/devark28/xhaos/app/backend"
)

// TestConcurrentWriters measures the event log under many agents writing at
// once (set XHAOS_LOAD=1 to run; it is a measurement, not a check).
func TestConcurrentWriters(t *testing.T) {
	if os.Getenv("XHAOS_LOAD") == "" {
		t.Skip("set XHAOS_LOAD=1")
	}
	s, err := backend.Open(filepath.Join(t.TempDir(), "x.db"), backend.Options{Root: t.TempDir(), DisableRuns: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	store := agent.NewStore(s.DB(), nil)
	const agents, writes = 50, 200
	ids := make([]int64, agents)
	for i := range ids {
		a, err := s.Agents.Create(agent.CreateRequest{IdempotencyKey: fmt.Sprint(i), WorkspaceID: ws, Title: "a"})
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = a.ID
	}
	body := string(make([]byte, 2000)) // a 2 KB reply
	var mu sync.Mutex
	var lat []time.Duration
	var wg sync.WaitGroup
	start := time.Now()
	for _, id := range ids {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			for j := 0; j < writes; j++ {
				t0 := time.Now()
				if err := store.Append(ws, id, agent.MessageData{From: agent.FromAgent, Body: body}); err != nil {
					t.Error(err)
					return
				}
				d := time.Since(t0)
				mu.Lock()
				lat = append(lat, d)
				mu.Unlock()
			}
		}(id)
	}
	wg.Wait()
	total := time.Since(start)
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	snap, _ := s.Agents.Snapshot(ws)
	t.Logf("%d agents x %d writes = %d events in %s: %.0f writes/s; latency p50 %s p99 %s max %s; events stored %d",
		agents, writes, len(lat), total.Round(time.Millisecond), float64(len(lat))/total.Seconds(),
		lat[len(lat)/2].Round(time.Microsecond), lat[len(lat)*99/100].Round(time.Microsecond), lat[len(lat)-1].Round(time.Microsecond), len(snap.Transcript))
}
