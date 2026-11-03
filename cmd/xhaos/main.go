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

	"xhaos/app/backend"
	"xhaos/app/eventlog"
	"xhaos/app/health"
	"xhaos/app/run"
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
	frontend := flag.String("frontend", "frontend", "directory containing the static frontend (served from disk)")
	dbPath := flag.String("db", defaultDBPath(), "SQLite database path")
	verbose := flag.Bool("verbose", false, "log every request and connection")
	root := flag.String("root", "", "register this folder as a project at startup (optional; add projects in the Files tab)")
	flag.Parse()

	if _, err := os.Stat(filepath.Join(*frontend, "index.html")); err != nil {
		log.Fatalf("frontend not found (run from the repo root or pass -frontend): %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(*dbPath), 0o700); err != nil {
		log.Fatal(err)
	}
	// The publisher needs the Wails app, which needs the services; break the
	// cycle with a variable assigned before Run starts serving.
	var wailsApp *application.App
	services, err := backend.Open(*dbPath, backend.Options{
		Root:    *root,
		Publish: func(e eventlog.Event) { wailsApp.Event.Emit(EventName, e) },
		Notify:  func(d run.Delta) { wailsApp.Event.Emit(DeltaEventName, d) },
	})
	if err != nil {
		log.Fatal(err)
	}

	level := slog.LevelWarn
	if *verbose {
		level = slog.LevelInfo
	}
	wailsApp = application.New(application.Options{
		Name:        "Xhaos",
		Description: "Headless agent orchestration backend",
		LogLevel:    level,
		Logger:      slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})),
		// WAILS_SERVER_HOST / WAILS_SERVER_PORT override these.
		Server: application.ServerOptions{Host: "localhost", Port: 5180},
		Services: []application.Service{
			application.NewService(health.New(version)),
			application.NewService(services.Workspaces),
			application.NewService(services.Agents),
			application.NewService(services.Events),
			application.NewService(services.Providers),
			application.NewService(services.Btw),
			application.NewService(services.Tools),
			application.NewService(services.Projects),
		},
		OnShutdown: func() { services.Close() },
		Assets: application.AssetOptions{
			Handler: noCache(application.AssetFileServerFS(os.DirFS(*frontend))),
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
