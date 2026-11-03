// Package health reports backend liveness. It has no transport dependency;
// cmd/xhaos binds it to Wails.
package health

import "time"

// Status is the health DTO returned to clients.
type Status struct {
	Service       string    `json:"service"`
	Version       string    `json:"version"`
	StartedAt     time.Time `json:"startedAt"`
	UptimeSeconds int64     `json:"uptimeSeconds"`
}

// Service reports process health.
type Service struct {
	version string
	started time.Time
	now     func() time.Time
}

// New returns a Service that measures uptime from now.
func New(version string) *Service {
	return &Service{version: version, started: time.Now(), now: time.Now}
}

// Status returns the current health status.
func (s *Service) Status() Status {
	return Status{
		Service:       "xhaos",
		Version:       s.version,
		StartedAt:     s.started.UTC(),
		UptimeSeconds: int64(s.now().Sub(s.started) / time.Second),
	}
}
