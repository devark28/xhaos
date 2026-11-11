package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/devark28/xhaos/app/agent"
	"github.com/devark28/xhaos/app/auth"
	"github.com/devark28/xhaos/app/channel"
	"github.com/devark28/xhaos/app/eventlog"
	"github.com/devark28/xhaos/app/project"
	"github.com/devark28/xhaos/app/provider"
)

// Deps is what the endpoint works with.
type Deps struct {
	Agents   *agent.Service
	Store    *agent.Store
	Channels *channel.Service
	Projects *project.Service
	Events   *eventlog.Service
	// Providers lists a workspace's model providers (no secrets).
	Providers func(ws string) ([]provider.Provider, error)
	// ProjectRoot is a project's folder (for reading git branches).
	ProjectRoot func(ws string, id int64) (string, error)
	// State is a workspace's lock state (auth.StateUnlocked when it may be
	// used); nil: always unlocked (servers without passphrases, tests).
	State func(ws string) string
	// Version names the server to clients.
	Version string
}

// Server is the MCP endpoint (Streamable HTTP, stateless: each request
// stands alone, so a restart or a dropped connection loses nothing).
type Server struct {
	tokens *Service
	d      Deps
	limits auth.Limiter
	sdk    http.Handler

	mu      sync.Mutex
	servers map[string]*sdk.Server // by scope set: a client sees only the tools its token allows
	waits   map[int64]int          // long polls running, per token
	wsWaits map[string]int         // and per workspace

	// MaxWait bounds the long-polling tools (wait_for_events, read_channel).
	MaxWait time.Duration
	// Poll is how often a waiting tool looks again.
	Poll time.Duration
	// LocalRemotes lets an approved push go to a local path (tests only:
	// otherwise only https and ssh remotes).
	LocalRemotes bool
	now          func() time.Time
}

func NewServer(tokens *Service, d Deps) *Server {
	s := &Server{tokens: tokens, d: d, servers: map[string]*sdk.Server{}, waits: map[int64]int{}, wsWaits: map[string]int{}, MaxWait: 50 * time.Second, Poll: 500 * time.Millisecond, now: time.Now}
	inner := sdk.NewStreamableHTTPHandler(s.serverFor, &sdk.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
		// The Host header is checked by the HTTP server (-public-host), and
		// requests carry a bearer token, not a cookie: a rebound DNS name
		// gains nothing.
		DisableLocalhostProtection: true,
		MaxRequestBodyBytes:        1 << 20,
	})
	// The token is checked in ServeHTTP; this hands the SDK its identity,
	// which tools read back from the request (CallToolRequest.Extra).
	s.sdk = sdkauth.RequireBearerToken(func(ctx context.Context, _ string, r *http.Request) (*sdkauth.TokenInfo, error) {
		id, ok := r.Context().Value(idKey{}).(identity)
		if !ok {
			return nil, sdkauth.ErrInvalidToken
		}
		return &sdkauth.TokenInfo{Scopes: id.Scopes, UserID: strconv.FormatInt(id.TokenID, 10), Extra: map[string]any{"identity": id}}, nil
	}, &sdkauth.RequireBearerTokenOptions{AllowMissingExpiration: true})(inner)
	return s
}

type idKey struct{}

// Long polls (wait_for_events, read_channel with a wait) running at once:
// beyond these a call returns at once, refused.
const (
	MaxWaitsPerToken     = 4
	MaxWaitsPerWorkspace = 16
)

// ErrTooManyWaits is what a long poll over the limits returns.
var ErrTooManyWaits = fmt.Errorf("too many waits: this token already has %d waiting calls (or the workspace %d); let one return first", MaxWaitsPerToken, MaxWaitsPerWorkspace)

// startWait counts a long poll; done must be called when it ends.
func (s *Server) startWait(id identity) (done func(), err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.waits[id.TokenID] >= MaxWaitsPerToken || s.wsWaits[id.Workspace] >= MaxWaitsPerWorkspace {
		return nil, ErrTooManyWaits
	}
	s.waits[id.TokenID]++
	s.wsWaits[id.Workspace]++
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.waits[id.TokenID]--; s.waits[id.TokenID] <= 0 {
			delete(s.waits, id.TokenID)
		}
		if s.wsWaits[id.Workspace]--; s.wsWaits[id.Workspace] <= 0 {
			delete(s.wsWaits, id.Workspace)
		}
	}, nil
}

// ServeHTTP checks the bearer token, then serves MCP. A valid token is
// always served; only failures are slowed down, per address, like sign-in
// attempts (so wrong guesses from an address never lock out good tokens,
// even behind a proxy that is not trusted to name the client).
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	secret := ""
	if f := strings.Fields(r.Header.Get("Authorization")); len(f) == 2 && strings.EqualFold(f[0], "Bearer") {
		secret = f[1]
	}
	addr := auth.ClientAddr(r)
	// A guess is refused before the lookup while the address must wait, so
	// the wait limits guessing; a token's own lookup is the same cost.
	if d := s.limits.Wait(addr, s.now()); d > 0 && !s.tokens.known(secret) {
		secs := int(d.Seconds()) + 1
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		replyJSON(w, http.StatusTooManyRequests, map[string]any{"error": fmt.Sprintf("too many wrong tokens; try again in %d s", secs)})
		return
	}
	id, err := s.tokens.lookup(secret)
	if err != nil {
		s.limits.Fail(addr, s.now())
		slog.Warn("mcp: refused token", "from", addr)
		w.Header().Set("WWW-Authenticate", `Bearer realm="xhaos"`)
		replyJSON(w, http.StatusUnauthorized, map[string]any{"error": err.Error() + "; create one in Settings → MCP access"})
		return
	}
	// Long polls outlast the HTTP server's 30-second write timeout.
	_ = http.NewResponseController(w).SetWriteDeadline(s.now().Add(s.MaxWait + 30*time.Second))
	s.sdk.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), idKey{}, id)))
}

func replyJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// serverFor is the MCP server for a request's token: its tools are those
// the token's scopes allow.
func (s *Server) serverFor(r *http.Request) *sdk.Server {
	id, ok := r.Context().Value(idKey{}).(identity)
	if !ok {
		return nil
	}
	key := strings.Join(id.Scopes, " ")
	s.mu.Lock()
	defer s.mu.Unlock()
	if srv := s.servers[key]; srv != nil {
		return srv
	}
	srv := sdk.NewServer(&sdk.Implementation{Name: "xhaos", Title: "Xhaos", Version: s.d.Version}, &sdk.ServerOptions{
		Instructions: instructions,
		// The tool list depends on the token's scopes: never shared.
		SetCacheable: func(_ context.Context, _ sdk.Request, c *sdk.Cacheable) { c.CacheScope, c.TTLMs = "private", 0 },
	})
	for _, t := range s.tools() {
		if t.allowed(id) {
			t.add(srv)
		}
	}
	s.servers[key] = srv
	return srv
}

const instructions = `Xhaos runs AI coding agents for its owner; this token acts for the owner within its scopes.
Agents are numbered (#N). An agent that needs the owner asks a question (ask_user) or waits for an approval; list_questions shows every pending one with its agent's number and title, and answer_question answers it by its id.
Use wait_for_events to wait for something to happen instead of polling: pass the seq it returned last time.
Channels carry messages between agents; post_channel writes into one under this token's name, and open_channel starts one with a single agent.
Everything done here is shown on the owner's page as coming from this token.
If a tool says the workspace is locked, the server restarted and the owner must sign in on the Xhaos page; agents wait until then.`
