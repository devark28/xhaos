package health

import (
	"testing"
	"time"
)

func TestStatusReportsVersionAndUptime(t *testing.T) {
	start := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	s := &Service{version: "test", started: start, now: func() time.Time { return start.Add(90 * time.Second) }}
	got := s.Status()
	if got.Service != "xhaos" || got.Version != "test" || got.UptimeSeconds != 90 || !got.StartedAt.Equal(start) {
		t.Fatalf("unexpected status: %+v", got)
	}
}
