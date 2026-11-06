// Package backend assembles the feature services over one database. It is the
// single place that knows migration order and wiring; transports (Wails
// today) bind the client-facing services it returns.
package backend

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"xhaos/app/attachment"
	"xhaos/app/auth"
	"xhaos/app/btw"
	"xhaos/app/channel"
	"xhaos/app/llm/openai"
	"xhaos/app/llm/sim"
	"xhaos/app/mcp"
	"xhaos/app/memory"
	"xhaos/app/project"
	"xhaos/app/tool"
	"xhaos/app/usage"

	"xhaos/app/agent"
	"xhaos/app/danger"
	"xhaos/app/egress"
	"xhaos/app/eventlog"
	"xhaos/app/idempotency"
	"xhaos/app/llm"
	"xhaos/app/llm/anthropic"
	"xhaos/app/prefs"
	"xhaos/app/provider"
	"xhaos/app/run"
	"xhaos/app/sandbox"
	"xhaos/app/secret"
	"xhaos/app/secretbox"
	"xhaos/app/skill"
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
	// Passphrase guards the server and its secrets with the owner's
	// passphrase (package auth): the key that encrypts API keys is unwrapped
	// at sign-in, and agents resume only then. Without it (tests) the key
	// sits in secret.key beside the database.
	Passphrase bool
	// Version names this server to MCP servers it connects to.
	Version string
	// Sandbox is how agents' shell commands are confined: sandbox.Auto,
	// On or Off ("" is Off, for tests).
	Sandbox string
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
	Memories   *memory.Service
	Usage      *usage.Service
	Prefs      *prefs.Service
	Secrets    *secret.Service
	Danger     *danger.Service
	Skills     *skill.Service
	mcp        *mcp.Manager
	egress     *egress.Proxy
	// Vault is the passphrase guard (nil without Options.Passphrase).
	Vault *auth.Vault
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
		{"memory", memory.Migrate},
		{"usage", usage.Migrate},
		{"prefs", prefs.Migrate},
	}
	for _, step := range steps {
		if err := step.migrate(db); err != nil {
			return nil, fmt.Errorf("migrate %s: %w", step.name, err)
		}
	}
	keyFile := filepath.Join(dataDir, "secret.key")
	var vault *auth.Vault
	var box *secretbox.Box
	var err error
	if opts.Passphrase {
		vault, box, err = auth.Open(db, keyFile)
	} else {
		box, err = secretbox.Open(keyFile)
	}
	if err != nil {
		return nil, err
	}
	adapters := opts.Adapters
	if adapters == nil {
		adapters = []llm.Adapter{anthropic.Adapter{}, openai.Adapter{}, sim.Adapter{}}
	}
	providers, err := provider.NewStore(db, box, adapters, map[string][]string{anthropic.Kind: anthropic.DefaultModels, sim.Kind: sim.Models})
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
	if err := secret.Migrate(db); err != nil { // its grants refer to agents
		return nil, fmt.Errorf("migrate secret: %w", err)
	}
	if err := skill.Migrate(db); err != nil {
		return nil, fmt.Errorf("migrate skill: %w", err)
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
	counter := usage.NewRecorder(db)
	runner.Usage = counter
	tools := mcp.NewManager(db, box, opts.Version)
	channels := channel.NewService(db, agents, store, opts.Publish)
	memories := memory.NewService(db, opts.Publish)
	runner.Memories = memories.Index
	runner.Extras = []run.ExternalTools{tools, channels.Tools(), memory.NewTools(memories)}
	side := btw.NewService(agents, providers)
	side.Usage = counter
	side.Context = func(ws string) string {
		return strings.TrimSpace(channels.Digest(ws) + "\n\n## Memories\n" + memories.Index(ws))
	}
	secrets := secret.NewService(db, box, opts.Publish)
	if vault != nil {
		secrets.Verify = vault.Verify
	}
	// Commands given secrets reach the network only through this proxy.
	out, err := egress.Start()
	if err != nil {
		return nil, fmt.Errorf("egress proxy: %w", err)
	}
	runner.Secrets, runner.Egress = secret.NewSealer(secrets), out
	runner.Extras = append(runner.Extras, secret.NewTools(secrets))
	skills, err := skill.NewService(db, filepath.Join(dataDir, "skills"), opts.Publish)
	if err != nil {
		return nil, err
	}
	runner.Skills = skills
	runner.Extras = append(runner.Extras, skill.NewTools(skills))
	risky := danger.NewService(store, store.Get)
	risky.Root = func(a agent.Agent) (string, error) {
		if a.ProjectID == 0 || runner.ProjectRoot == nil {
			return "", nil
		}
		return runner.ProjectRoot(a.WorkspaceID, a.ProjectID)
	}
	if vault != nil {
		risky.Verify = vault.Verify
	}
	catalog := tool.NewService()
	runner.Sandbox = sandbox.Config{Mode: opts.Sandbox, Cache: filepath.Join(dataDir, "sandbox")}
	risky.Sandbox = runner.Sandbox
	catalog.Sandbox = runner.Sandbox
	catalog.More = func() []tool.Info {
		return append(append(append(append(runner.Catalog(), secret.NewTools(secrets).Catalog()...), skill.NewTools(skills).Catalog()...), channels.Tools().Catalog()...), memory.NewTools(memories).Catalog()...)
	}
	runner.OnFinal = channels.OnFinal
	spend := usage.NewService(db)
	channels.Models = func(id string) ([]string, error) {
		list, err := provider.NewService(providers).Providers() // no secrets read
		if err != nil {
			return nil, err
		}
		for _, p := range list {
			if p.ID == id {
				return append([]string(nil), p.Models...), nil
			}
		}
		return nil, fmt.Errorf("provider %q not found", id)
	}
	channels.Price = func(model string) (float64, float64, bool) {
		if p := spend.PriceOf(model); p != nil {
			return p.Input, p.Output, true
		}
		return 0, 0, false
	}
	runner.ProjectRoot = func(workspaceID string, projectID int64) (string, error) {
		p, err := project.Get(db, workspaceID, projectID)
		return p.Path, err
	}
	// Both need the secrets: with a passphrase they wait for the first sign-in.
	ready := func() {
		tools.Start() // connect enabled MCP servers in the background
		if !opts.DisableRuns {
			runner.Resume()
		}
	}
	if !opts.DisableRuns {
		agents.Wake = runner.Kick
		agents.Halt = runner.Stop
		channels.Wake = runner.Kick
	}
	if vault != nil {
		vault.OnUnlock = ready
		vault.Ready() // the older key file: unlocked already
	} else {
		ready()
	}
	return &Services{
		db:         db,
		runner:     runner,
		Workspaces: workspace.NewService(db),
		Agents:     agents,
		Events:     eventlog.NewService(db),
		Providers:  provider.NewService(providers),
		Btw:        side,
		Tools:      catalog,
		Projects:   project.NewService(db),
		MCP:        mcp.NewService(tools),
		Channels:   channels,
		Files:      attachment.NewService(files),
		Memories:   memories,
		Usage:      spend,
		Prefs:      prefs.NewService(db),
		Secrets:    secrets,
		Danger:     risky,
		Skills:     skills,
		mcp:        tools,
		egress:     out,
		Vault:      vault,
	}, nil
}

// Close stops running turns, then closes the database.
func (s *Services) Close() error {
	s.runner.Close()
	s.mcp.Close()
	s.egress.Close()
	eventlog.Close(s.db)
	return s.db.Close()
}

// MCPManager is the live MCP connections (for tests and the runner).
func (s *Services) MCPManager() *mcp.Manager { return s.mcp }

// Egress is the proxy for commands that hold secrets (tests point it at local servers).
func (s *Services) Egress() *egress.Proxy { return s.egress }

// DB is the shared database handle (for measurements and tests).
func (s *Services) DB() *sql.DB { return s.db }
