//go:build server

// Command xhaos runs the headless Xhaos backend as a Wails v3 server:
// it serves the frontend over HTTP, exposes Go services through the Wails
// runtime, and pushes live events over WebSocket. Browser tabs are clients;
// closing them does not stop this process.
//
// Run with scripts/server.sh (tag: server; pure Go, cgo off). Release builds
// add the embed tag to build the page in (scripts/release.sh).
package main

import (
	"flag"
	"fmt"
	"io/fs"
	"log"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/devark28/xhaos/app/auth"
	"github.com/devark28/xhaos/app/backend"
	"github.com/devark28/xhaos/app/egress"
	"github.com/devark28/xhaos/app/eventlog"
	"github.com/devark28/xhaos/app/health"
	"github.com/devark28/xhaos/app/live"
	"github.com/devark28/xhaos/app/run"
	"github.com/devark28/xhaos/app/sandbox"
	"github.com/devark28/xhaos/app/serve"
	"github.com/devark28/xhaos/frontend"
)

// version is set by release builds (-ldflags -X main.version=…).
var version = "0.1.0-dev"

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
	sandbox.MaybeRun()    // started as the command sandbox helper: never returns
	egress.MaybeConnect() // started as ssh's ProxyCommand in a sealed command: never returns
	pageDir := flag.String("frontend", "", "serve the page from this directory (default: the page built into release binaries, else ./frontend)")
	dbPath := flag.String("db", defaultDBPath(), "SQLite database path")
	verbose := flag.Bool("verbose", false, "log every request and connection")
	root := flag.String("root", "", "register this folder as a project at startup (optional; add projects in the Files tab)")
	sandboxMode := flag.String("sandbox", sandbox.Auto, "confine agents' commands with Landlock: auto (when the kernel supports it), on (refuse commands without it), off")
	publicHosts := flag.String("public-host", "", "domain names a reverse proxy serves this server under (comma-separated, e.g. xhaos.example.com); needed when it listens on localhost behind the proxy")
	trustedProxies := flag.String("trusted-proxy", "", "reverse proxies whose X-Forwarded-For and X-Forwarded-Proto headers are trusted (comma-separated addresses or CIDRs, e.g. 127.0.0.1,::1)")
	flag.Parse()
	var proxies []netip.Prefix
	for _, p := range strings.Split(*trustedProxies, ",") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		if !strings.Contains(p, "/") {
			if a, err := netip.ParseAddr(p); err == nil {
				p = netip.PrefixFrom(a, a.BitLen()).String()
			}
		}
		prefix, err := netip.ParsePrefix(p)
		if err != nil {
			log.Fatalf("-trusted-proxy: %v", err)
		}
		proxies = append(proxies, prefix.Masked())
	}
	auth.TrustProxies(proxies)
	var hosts []string
	for _, h := range strings.Split(*publicHosts, ",") {
		if h = strings.TrimSpace(h); h != "" {
			hosts = append(hosts, h)
		}
	}
	if m := *sandboxMode; m != sandbox.Auto && m != sandbox.On && m != sandbox.Off {
		log.Fatalf("-sandbox must be auto, on or off")
	}

	page, from, err := pageFiles(*pageDir)
	if err != nil {
		log.Fatal(err)
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
		Publish:    func(e eventlog.Event) { hub.Publish(e.WorkspaceID, EventName, e) },
		Notify:     func(d run.Delta) { hub.PublishLossy(d.WorkspaceID, DeltaEventName, d) },
	})
	if err != nil {
		log.Fatal(err)
	}
	// A stream carries only its session's workspace.
	hub.Workspace = func(r *http.Request) string { return auth.WorkspaceOf(r.Context()) }
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
		// The Host check on a localhost bind accepts the public names too.
		Server:     application.ServerOptions{Host: "localhost", Port: 5180, WebSocketOriginPatterns: hosts},
		Services:   bindings(services),
		OnShutdown: func() { services.Close() },
		Assets: application.AssetOptions{
			Handler: noCache(application.AssetFileServerFS(page)),
			// Sign-in, and a session for every runtime call and the event
			// stream; responses compressed, static files revalidated by ETag.
			Middleware: middleware(services.Vault.Middleware(guarded, services.Channels.Links())),
		},
	})

	host, port := os.Getenv("WAILS_SERVER_HOST"), os.Getenv("WAILS_SERVER_PORT")
	if host == "" {
		host = "localhost"
	}
	if port == "" {
		port = "5180"
	}
	log.Printf("xhaos %s: http://%s:%s (db %s, page %s)", version, host, port, *dbPath, from)
	if cards := services.Vault.Cards(); len(cards) == 1 && cards[0].State == auth.StateSetup {
		log.Printf("no passphrase yet: open the page to set one (it encrypts API keys and signs browsers in)")
	} else {
		log.Printf("%d workspace(s), locked: each one's agents resume once someone signs in to it", len(cards))
	}
	if err := wailsApp.Run(); err != nil {
		log.Fatal(err)
	}
}

// pageFiles picks where the page comes from: a directory given with
// -frontend, the page built into a release binary, or ./frontend.
func pageFiles(dir string) (fs.FS, string, error) {
	if dir == "" && frontend.Files != nil {
		return frontend.Files, "built in", nil
	}
	if dir == "" {
		dir = "frontend"
	}
	if _, err := os.Stat(filepath.Join(dir, "index.html")); err != nil {
		return nil, "", fmt.Errorf("frontend not found (run from the repo root, pass -frontend, or use a release binary): %v", err)
	}
	return os.DirFS(dir), dir, nil
}

// middleware puts compression around everything (event streams and the
// WebSocket pass through untouched) and ETags on the static files: the page,
// its bundle, fonts, and Wails's runtime script. Calls, live streams and
// per-user data are never tagged.
func middleware(authed func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	dynamic := func(p string) bool {
		return strings.HasPrefix(p, "/xhaos/") || p == "/wails/runtime" || p == "/wails/events" || p == "/health"
	}
	return func(next http.Handler) http.Handler {
		h := serve.Compress(authed(serve.ETag(dynamic, next)))
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Runtime calls may run long (transcribing minutes of speech, a
			// slow model answer): the server's 30-second write timeout would
			// cut their answer off, which the page reads as an empty result.
			if r.URL.Path == "/wails/runtime" {
				_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(callTimeout))
			}
			h.ServeHTTP(w, r)
		})
	}
}

// callTimeout is how long a runtime call may take to answer.
const callTimeout = 10 * time.Minute

// noCache makes browsers revalidate frontend files on every load. They are
// served straight from disk during development, so a heuristically cached
// stylesheet or script would otherwise mix old and new code. With the ETags
// (middleware), revalidating an unchanged file is one 304 with no body.
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
		application.NewService(s.Danger),
		application.NewService(s.Skills),
		application.NewService(s.Web),
	}
}
