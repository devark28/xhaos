package classifier

import (
	"context"
	"os"
	"path/filepath"
)

// Service is the page's view of the classifier: server-wide settings and the
// health check. Every method is server-wide (no workspace's data).
type Service struct{ c *Classifier }

// Health is the model server's state, for the settings' indicator.
type Health struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"` // why it is not OK
}

// Enabled reports whether a model server is configured (-classifier-url).
// The page hides the classifier's settings when it is not.
func (s *Service) Enabled() bool { return s.c.client != nil }

// On reports the server-wide switch. It is on by default, with or without a
// model server: it also switches the built-in list.
func (s *Service) On() (bool, error) { return s.c.on() }

// SetOn turns the classifier on or off for every agent.
func (s *Service) SetOn(on bool) error { return s.c.putSetting("on", on) }

// Health checks the model server (GET {url}/health).
func (s *Service) Health() Health {
	if s.c.client == nil {
		return Health{Detail: "no classifier is configured: start the server with -classifier-url"}
	}
	if err := s.c.client.Health(context.Background()); err != nil {
		return Health{Detail: err.Error()}
	}
	return Health{OK: true}
}

// DeveloperLog reports whether every decision is written to the developer
// log (the Developer setting). It is off by default.
func (s *Service) DeveloperLog() (bool, error) { return s.c.developer() }

// SetDeveloperLog turns the developer log on or off.
func (s *Service) SetDeveloperLog(on bool) error { return s.c.putSetting("developer", on) }

// DeleteLog removes the developer log.
func (s *Service) DeleteLog() error {
	if s.c.cfg.Debug == "" {
		return nil
	}
	s.c.logMu.Lock()
	defer s.c.logMu.Unlock()
	err := os.Remove(filepath.Join(s.c.cfg.Debug, logFile))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
