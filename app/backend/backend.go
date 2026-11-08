// Package backend assembles the feature services over one database. It is the
// single place that knows migration order and wiring; transports (Wails
// today) bind the client-facing services it returns.
package backend

import (
	"database/sql"
	"fmt"
	"github.com/devark28/xhaos/app/attachment"
	"github.com/devark28/xhaos/app/auth"
	"github.com/devark28/xhaos/app/board"
	"github.com/devark28/xhaos/app/btw"
	"github.com/devark28/xhaos/app/channel"
	"github.com/devark28/xhaos/app/dictation"
	"github.com/devark28/xhaos/app/llm/openai"
	"github.com/devark28/xhaos/app/llm/sim"
	"github.com/devark28/xhaos/app/mcp"
	"github.com/devark28/xhaos/app/memory"
	"github.com/devark28/xhaos/app/project"
	"github.com/devark28/xhaos/app/tool"
	"github.com/devark28/xhaos/app/usage"
	"path/filepath"
	"strings"

	"github.com/devark28/xhaos/app/agent"
	"github.com/devark28/xhaos/app/danger"
	"github.com/devark28/xhaos/app/egress"
	"github.com/devark28/xhaos/app/eventlog"
	"github.com/devark28/xhaos/app/idempotency"
	"github.com/devark28/xhaos/app/llm"
	"github.com/devark28/xhaos/app/llm/anthropic"
	"github.com/devark28/xhaos/app/prefs"
	"github.com/devark28/xhaos/app/provider"
	"github.com/devark28/xhaos/app/run"
	"github.com/devark28/xhaos/app/sandbox"
	"github.com/devark28/xhaos/app/secret"
	"github.com/devark28/xhaos/app/secretbox"
	"github.com/devark28/xhaos/app/skill"
	"github.com/devark28/xhaos/app/sqlitedb"
	"github.com/devark28/xhaos/app/web"
	"github.com/devark28/xhaos/app/workspace"
)

// Options configures Open. Zero values are fine for tests.
type Options struct {
	Publish   eventlog.Publisher // committed events
	Notify    run.Notifier       // live, unpersisted reply deltas
	BoardLive board.Notifier     // live, unpersisted board saves
	Adapters  []llm.Adapter      // nil: the real adapters (Anthropic)
	Root      string             // a folder to register as a project at startup (optional)
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
	// Dictation is the speech-to-text sidecar (zero Program: dictation off).
	Dictation dictation.Config
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
	Boards     *board.Service
	Usage      *usage.Service
	Prefs      *prefs.Service
	Secrets    *secret.Service
	Danger     *danger.Service
	Skills     *skill.Service
	Web        *web.Service
	Dictation  *dictation.Service
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
		{"board", board.Migrate},
		{"usage", usage.Migrate},
		{"prefs", prefs.Migrate},
		{"dictation", dictation.Migrate},
	}
	for _, step := range steps {
		if err := step.migrate(db); err != nil {
			return nil, fmt.Errorf("migrate %s: %w", step.name, err)
		}
	}
	keyFile := filepath.Join(dataDir, "secret.key")
	var vault *auth.Vault
	var box *secretbox.Keyring // one key per workspace
	var err error
	if opts.Passphrase {
		vault, box, err = auth.Open(db, keyFile)
	} else {
		var shared *secretbox.Box
		if shared, err = secretbox.Open(keyFile); err == nil {
			box = secretbox.Shared(shared)
		}
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
	if err := web.Migrate(db); err != nil {
		return nil, fmt.Errorf("migrate web: %w", err)
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
	webs := web.NewService(db, box)
	runner.Extras = append(runner.Extras, web.NewTools(webs))
	boards := board.NewService(db, opts.Publish, opts.BoardLive, filepath.Join(dataDir, "boards"))
	// Agents read images for boards from their project (or its $TMPDIR).
	boardTools := board.NewTools(boards, func(a agent.Agent) (string, error) {
		if a.ProjectID == 0 || runner.ProjectRoot == nil {
			return "", fmt.Errorf("agent #%d has no project to read images from", a.ID)
		}
		return runner.ProjectRoot(a.WorkspaceID, a.ProjectID)
	})
	runner.Extras = append(runner.Extras, boardTools)
	risky := danger.NewService(store, store.Get)
	runner.Guard = risky
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
		return append(append(append(append(runner.Catalog(), secret.NewTools(secrets).Catalog()...), skill.NewTools(skills).Catalog()...), append(web.NewTools(webs).Catalog(), channels.Tools().Catalog()...)...), append(memory.NewTools(memories).Catalog(), boardTools.Catalog()...)...)
	}
	runner.OnFinal = channels.OnFinal
	spend := usage.NewService(db)
	channels.Models = func(ws, id string) ([]string, error) {
		list, err := providers.List(ws) // no secrets read
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
	// Both need a workspace's secrets: with passphrases, each workspace
	// starts when someone first signs in to it.
	ready := func(ws string) {
		tools.Start(ws) // connect its enabled MCP servers in the background
		if !opts.DisableRuns {
			runner.Resume(ws)
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
	} else if list, err := workspace.NewService(db).Workspaces(); err == nil {
		for _, w := range list {
			ready(w.ID)
		}
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
		Boards:     boards,
		Usage:      spend,
		Prefs:      prefs.NewService(db),
		Secrets:    secrets,
		Danger:     risky,
		Skills:     skills,
		Web:        webs,
		Dictation:  dictation.NewService(db, opts.Dictation),
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
