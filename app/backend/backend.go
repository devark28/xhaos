// Package backend assembles the feature services over one database. It is the
// single place that knows migration order and wiring; transports (Wails
// today) bind the client-facing services it returns.
package backend

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"xhaos/app/btw"
	"xhaos/app/tool"

	"xhaos/app/agent"
	"xhaos/app/eventlog"
	"xhaos/app/idempotency"
	"xhaos/app/llm"
	"xhaos/app/llm/anthropic"
	"xhaos/app/provider"
	"xhaos/app/run"
	"xhaos/app/secretbox"
	"xhaos/app/sqlitedb"
	"xhaos/app/workspace"
)

// Options configures Open. Zero values are fine for tests.
type Options struct {
	Publish  eventlog.Publisher // committed events
	Notify   run.Notifier       // live, unpersisted reply deltas
	Adapters []llm.Adapter      // nil: the real adapters (Anthropic)
	Root     string             // directory agent tools work in; empty: the current directory
	// DisableRuns stops owner messages from starting model turns (storage tests).
	DisableRuns bool
}

// Services are the client-facing feature services plus server internals.
type Services struct {
	db         *sql.DB
	runner     *run.Runner
	Workspaces *workspace.Service
	Agents     *agent.Service
	Events     *eventlog.Service
	Providers  *provider.Service
	Btw        *btw.Service
	Tools      *tool.Service
}

// Open opens the database at path (the API key encryption key is kept beside
// it as secret.key), migrates every feature, and wires the runner.
func Open(path string, opts Options) (*Services, error) {
	db, err := sqlitedb.Open(path)
	if err != nil {
		return nil, err
	}
	s, err := assemble(db, filepath.Join(filepath.Dir(path), "secret.key"), opts)
	if err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func assemble(db *sql.DB, keyPath string, opts Options) (*Services, error) {
	steps := []struct {
		name    string
		migrate func(*sql.DB) error
	}{
		{"workspace", workspace.Migrate},
		{"eventlog", eventlog.Migrate},
		{"idempotency", idempotency.Migrate},
	}
	for _, step := range steps {
		if err := step.migrate(db); err != nil {
			return nil, fmt.Errorf("migrate %s: %w", step.name, err)
		}
	}
	box, err := secretbox.Open(keyPath)
	if err != nil {
		return nil, err
	}
	adapters := opts.Adapters
	if adapters == nil {
		adapters = []llm.Adapter{anthropic.Adapter{}}
	}
	providers, err := provider.NewStore(db, box, adapters, map[string][]string{anthropic.Kind: anthropic.DefaultModels})
	if err != nil {
		return nil, err
	}
	agents, err := agent.NewService(db, opts.Publish)
	if err != nil {
		return nil, err
	}
	root := opts.Root
	if root == "" {
		if root, err = os.Getwd(); err != nil {
			return nil, err
		}
	}
	if root, err = filepath.Abs(root); err != nil {
		return nil, err
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("workspace root %s is not a directory", root)
	}
	runner := run.New(agent.NewStore(db, opts.Publish), providers, opts.Notify, root)
	if !opts.DisableRuns {
		agents.Wake = runner.Kick
		agents.Halt = runner.Stop
		runner.Resume()
	}
	return &Services{
		db:         db,
		runner:     runner,
		Workspaces: workspace.NewService(db),
		Agents:     agents,
		Events:     eventlog.NewService(db),
		Providers:  provider.NewService(providers),
		Btw:        btw.NewService(agents, providers),
		Tools:      tool.NewService(root),
	}, nil
}

// Close stops running turns, then closes the database.
func (s *Services) Close() error {
	s.runner.Close()
	return s.db.Close()
}
