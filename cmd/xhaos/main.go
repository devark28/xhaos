//go:build server

// Command xhaos runs the headless Xhaos backend as a Wails v3 server:
// it serves the frontend over HTTP, exposes Go services through the Wails
// runtime, and pushes live events over WebSocket. Browser tabs are clients;
// closing them does not stop this process.
//
// Run with scripts/server.sh (tags: server,purego,libsqlite3; cgo on for SQLite).
package main

import (
	"flag"
	"log"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v3/pkg/application"

	"xhaos/app/auth"
	"xhaos/app/backend"
	"xhaos/app/eventlog"
	"xhaos/app/health"
	"xhaos/app/live"
	"xhaos/app/run"
	"xhaos/app/sandbox"
)

const version = "0.1.0-dev"

// EventName is the Wails event carrying every committed eventlog.Event.
const EventName = "xhaos:event"

// DeltaEventName carries live, unpersisted reply text (run.Delta).
const DeltaEventName = "xhaos:delta"

func defaultDBPath() string {
	dir := os.Getenv("XDG_DATA_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dir, "xhaos", "xhaos.db")
}

func main() {
	sandbox.MaybeRun() // when started as the command sandbox helper, this never returns
	frontend := flag.String("frontend", "frontend", "directory containing the static frontend (served from disk)")
	dbPath := flag.String("db", defaultDBPath(), "SQLite database path")
	verbose := flag.Bool("verbose", false, "log every request and connection")
	root := flag.String("root", "", "register this folder as a project at startup (optional; add projects in the Files tab)")
	sandboxMode := flag.String("sandbox", sandbox.Auto, "confine agents' commands with Landlock: auto (when the kernel supports it), on (refuse commands without it), off")
	flag.Parse()
	if m := *sandboxMode; m != sandbox.Auto && m != sandbox.On && m != sandbox.Off {
		log.Fatalf("-sandbox must be auto, on or off")
	}

	if _, err := os.Stat(filepath.Join(*frontend, "index.html")); err != nil {
		log.Fatalf("frontend not found (run from the repo root or pass -frontend): %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(*dbPath), 0o700); err != nil {
		log.Fatal(err)
	}
	// Live events go out on /xhaos/events (behind the session check), not on
	// the Wails event socket, which any local client could open.
	hub := live.NewHub()
	services, err := backend.Open(*dbPath, backend.Options{
		Root:       *root,
		Version:    version,
		Passphrase: true,
		Sandbox:    *sandboxMode,
		Publish:    func(e eventlog.Event) { hub.Publish(EventName, e) },
		Notify:     func(d run.Delta) { hub.PublishLossy(DeltaEventName, d) },
	})
	if err != nil {
		log.Fatal(err)
	}
	guarded := http.NewServeMux()
	guarded.Handle("/xhaos/events", hub)

	level := slog.LevelWarn
	if *verbose {
		level = slog.LevelInfo
	}
	wailsApp := application.New(application.Options{
		Name:        "Xhaos",
		Description: "Headless agent orchestration backend",
		LogLevel:    level,
		Logger:      slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})),
		// WAILS_SERVER_HOST / WAILS_SERVER_PORT override these.
		Server:     application.ServerOptions{Host: "localhost", Port: 5180},
		Services:   bindings(services),
		OnShutdown: func() { services.Close() },
		Assets: application.AssetOptions{
			Handler: noCache(application.AssetFileServerFS(os.DirFS(*frontend))),
			// Sign-in, and a session for every runtime call and the event stream.
			Middleware: services.Vault.Middleware(guarded, services.Channels.Links()),
		},
	})

	host, port := os.Getenv("WAILS_SERVER_HOST"), os.Getenv("WAILS_SERVER_PORT")
	if host == "" {
		host = "localhost"
	}
	if port == "" {
		port = "5180"
	}
	log.Printf("xhaos %s: http://%s:%s (db %s)", version, host, port, *dbPath)
	switch services.Vault.State() {
	case auth.StateSetup:
		log.Printf("no passphrase yet: open the page to set one (it encrypts API keys and signs browsers in)")
	case auth.StateLocked:
		log.Printf("locked: agents resume once you sign in with the passphrase")
	}
	if err := wailsApp.Run(); err != nil {
		log.Fatal(err)
	}
}

// noCache makes browsers revalidate frontend files on every load. They are
// served straight from disk during development, so a heuristically cached
// stylesheet or script would otherwise mix old and new code.
func noCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		next.ServeHTTP(w, r)
	})
}

// bindings is every service the page can call: each exported method of these
// is reachable from the browser. Agent-side operations (posting to channels,
// spawning, saving memories as an agent) take the acting agent, so they stay
// off this list; channels are bound through their read-only Owner view.
func bindings(s *backend.Services) []application.Service {
	return []application.Service{
		application.NewService(health.New(version)),
		application.NewService(s.Workspaces),
		application.NewService(s.Agents),
		application.NewService(s.Events),
		application.NewService(s.Providers),
		application.NewService(s.Btw),
		application.NewService(s.Tools),
		application.NewService(s.Projects),
		application.NewService(s.MCP),
		application.NewService(s.Channels.Owner()),
		application.NewService(s.Files),
		application.NewService(s.Memories),
		application.NewService(s.Usage),
		application.NewService(s.Prefs),
		application.NewService(s.Secrets),
	}
}
