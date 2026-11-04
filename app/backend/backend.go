// Package backend assembles the feature services over one database. It is the
// single place that knows migration order and wiring; transports (Wails
// today) bind the client-facing services it returns.
package backend

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"xhaos/app/attachment"
	"xhaos/app/btw"
	"xhaos/app/channel"
	"xhaos/app/llm/openai"
	"xhaos/app/mcp"
	"xhaos/app/project"
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
	Root     string             // a folder to register as a project at startup (optional)
	// DisableRuns stops owner messages from starting model turns (storage tests).
	DisableRuns bool
	// Version names this server to MCP servers it connects to.
	Version string
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
	Projects   *project.Service
	MCP        *mcp.Service
	Channels   *channel.Service
	Files      *attachment.Service
	mcp        *mcp.Manager
}

// Open opens the database at path (the API key encryption key is kept beside
// it as secret.key), migrates every feature, and wires the runner.
func Open(path string, opts Options) (*Services, error) {
	db, err := sqlitedb.Open(path)
	if err != nil {
		return nil, err
	}
	s, err := assemble(db, filepath.Dir(path), opts)
	if err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func assemble(db *sql.DB, dataDir string, opts Options) (*Services, error) {
	steps := []struct {
		name    string
		migrate func(*sql.DB) error
	}{
		{"workspace", workspace.Migrate},
		{"eventlog", eventlog.Migrate},
		{"idempotency", idempotency.Migrate},
		{"project", project.Migrate},
		{"mcp", mcp.Migrate},
		{"attachment", attachment.Migrate},
	}
	for _, step := range steps {
		if err := step.migrate(db); err != nil {
			return nil, fmt.Errorf("migrate %s: %w", step.name, err)
		}
	}
	box, err := secretbox.Open(filepath.Join(dataDir, "secret.key"))
	if err != nil {
		return nil, err
	}
	adapters := opts.Adapters
	if adapters == nil {
		adapters = []llm.Adapter{anthropic.Adapter{}, openai.Adapter{}}
	}
	providers, err := provider.NewStore(db, box, adapters, map[string][]string{anthropic.Kind: anthropic.DefaultModels})
	if err != nil {
		return nil, err
	}
	agents, err := agent.NewService(db, opts.Publish)
	if err != nil {
		return nil, err
	}
	if err := channel.Migrate(db); err != nil { // its tables refer to agents
		return nil, fmt.Errorf("migrate channel: %w", err)
	}
	// Root, when given, is registered as a project. Nothing is registered
	// implicitly: a server started from a home folder or an empty VPS must
	// not turn that folder into a project.
	if opts.Root != "" {
		root, err := filepath.Abs(opts.Root)
		if err != nil {
			return nil, err
		}
		if err := project.Ensure(db, workspace.DefaultID, root); err != nil {
			return nil, fmt.Errorf("project %s: %w", root, err)
		}
	}
	files, err := attachment.NewStore(db, filepath.Join(dataDir, "attachments"))
	if err != nil {
		return nil, err
	}
	agents.Files = files
	store := agent.NewStore(db, opts.Publish)
	runner := run.New(store, providers, opts.Notify)
	runner.Files = files
	tools := mcp.NewManager(db, box, opts.Version)
	channels := channel.NewService(db, agents, store, opts.Publish)
	runner.Extras = []run.ExternalTools{tools, channels.Tools()}
	runner.OnFinal = channels.OnFinal
	runner.ProjectRoot = func(workspaceID string, projectID int64) (string, error) {
		p, err := project.Get(db, workspaceID, projectID)
		return p.Path, err
	}
	tools.Start() // connect enabled MCP servers in the background
	if !opts.DisableRuns {
		agents.Wake = runner.Kick
		agents.Halt = runner.Stop
		channels.Wake = runner.Kick
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
		Tools:      tool.NewService(),
		Projects:   project.NewService(db),
		MCP:        mcp.NewService(tools),
		Channels:   channels,
		Files:      attachment.NewService(files),
		mcp:        tools,
	}, nil
}

// Close stops running turns, then closes the database.
func (s *Services) Close() error {
	s.runner.Close()
	s.mcp.Close()
	return s.db.Close()
}

// MCPManager is the live MCP connections (for tests and the runner).
func (s *Services) MCPManager() *mcp.Manager { return s.mcp }

// DB is the shared database handle (for measurements and tests).
func (s *Services) DB() *sql.DB { return s.db }
