// Package auth guards the server with passphrases: one per workspace.
//
// A workspace's passphrase does two things. It signs a browser in to that
// workspace: a session cookie (HttpOnly, SameSite=Strict) bound to it, which
// every runtime call and the live event stream require, and which reaches
// only that workspace's data (Middleware checks every call). And it holds the
// workspace's key: Argon2id turns the passphrase into a key-encryption key,
// which wraps the workspace's own random data key; that key encrypts its
// provider keys, MCP secrets, secrets vault and search key. Only wrapped keys
// are stored. After a restart every workspace is locked until someone signs
// in to it; its agents resume then.
//
// Changing a passphrase rewraps the same data key, so nothing else is
// re-encrypted. A server that used the older key file (secret.key beside the
// database) keeps using that key for its first workspace.
package auth

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/devark28/xhaos/app/secretbox"
	"github.com/devark28/xhaos/app/sqlitedb"
	"github.com/devark28/xhaos/app/workspace"
)

const (
	MinLength  = 12 // characters
	CookieName = "xhaos_session"
	sessionTTL = 30 * 24 * time.Hour
	// Argon2id cost: about a tenth of a second and 64 MiB per attempt.
	kdfTime    = 3
	kdfMemory  = 64 * 1024 // KiB
	kdfThreads = 4
)

// States of a workspace, as GET /xhaos/auth reports them.
const (
	StateSetup    = "setup"    // no passphrase yet (only the first workspace, before first use)
	StateLocked   = "locked"   // set, not entered since the server started (or locked again)
	StateUnlocked = "unlocked" // entered: its key is in memory, its agents run
)

var (
	ErrWrong   = errors.New("wrong passphrase")
	ErrShort   = fmt.Errorf("the passphrase needs at least %d characters", MinLength)
	ErrIsSet   = errors.New("a passphrase is already set")
	ErrNotSet  = errors.New("no passphrase is set yet")
	errBadForm = errors.New("bad request")
)

const schema = `
CREATE TABLE IF NOT EXISTS vaults (
	workspace_id TEXT PRIMARY KEY REFERENCES workspaces(id),
	salt         TEXT NOT NULL,
	kdf_time     INTEGER NOT NULL,
	kdf_memory   INTEGER NOT NULL,
	kdf_threads  INTEGER NOT NULL,
	wrapped      TEXT NOT NULL, -- base64(nonce || AES-GCM(kek, data key))
	updated_at   TEXT NOT NULL
);`

type session struct {
	workspace string
	expires   time.Time
}

type Vault struct {
	db      *sql.DB
	keys    *secretbox.Keyring
	keyFile string // the older key file, used by the first workspace

	login    sync.Mutex // serializes setup, sign-in, create and change
	limits   Limiter    // wrong passphrases per client address
	mu       sync.Mutex // guards sessions and chunks
	sessions map[string]session
	chunks   map[string]*pending // chunked runtime calls being checked (Middleware)
	// OnUnlock runs when a workspace's key becomes available (its agents can
	// resume, its MCP servers connect).
	OnUnlock func(workspaceID string)
	now      func() time.Time
}

// Open migrates the vaults and returns the vault with the keyring its
// workspaces' keys go into (all locked until signed in).
func Open(db *sql.DB, keyFile string) (*Vault, *secretbox.Keyring, error) {
	if _, err := db.Exec(schema); err != nil {
		return nil, nil, err
	}
	// The single-passphrase vault of earlier versions becomes the first
	// workspace's.
	var old int
	if db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'vault'`).Scan(&old); old > 0 {
		if _, err := db.Exec(`INSERT OR IGNORE INTO vaults (workspace_id, salt, kdf_time, kdf_memory, kdf_threads, wrapped, updated_at)
			SELECT ?, salt, kdf_time, kdf_memory, kdf_threads, wrapped, updated_at FROM vault WHERE id = 1`, workspace.DefaultID); err != nil {
			return nil, nil, fmt.Errorf("migrate vault: %w", err)
		}
		if _, err := db.Exec(`DROP TABLE vault`); err != nil {
			return nil, nil, err
		}
	}
	v := &Vault{db: db, keys: secretbox.NewKeyring(), keyFile: keyFile, sessions: map[string]session{}, chunks: map[string]*pending{}, now: time.Now}
	if set, _ := v.isSet(workspace.DefaultID); !set {
		if key, err := os.ReadFile(keyFile); err == nil {
			if err := v.keys.Box(workspace.DefaultID).Unlock(key); err != nil {
				return nil, nil, fmt.Errorf("%s: %w", keyFile, err)
			}
		}
	}
	return v, v.keys, nil
}

func (v *Vault) isSet(ws string) (bool, error) {
	var n int
	err := v.db.QueryRow(`SELECT COUNT(*) FROM vaults WHERE workspace_id = ?`, ws).Scan(&n)
	return n > 0, err
}

// anySet reports whether any workspace has a passphrase (first setup is
// done).
func (v *Vault) anySet() bool {
	var n int
	_ = v.db.QueryRow(`SELECT COUNT(*) FROM vaults`).Scan(&n)
	return n > 0
}

// State is a workspace's state.
func (v *Vault) State(ws string) string {
	if set, _ := v.isSet(ws); !set {
		return StateSetup
	}
	if v.keys.Box(ws).IsLocked() {
		return StateLocked
	}
	return StateUnlocked
}

// Ready starts the workspaces whose key is already available (the older
// key file).
func (v *Vault) Ready() {
	if !v.keys.Box(workspace.DefaultID).IsLocked() {
		v.fireUnlock(workspace.DefaultID)
	}
}

func (v *Vault) fireUnlock(ws string) {
	if v.OnUnlock != nil {
		go v.OnUnlock(ws)
	}
}

// Card is a workspace on the picker: its name and state, never anything else.
type Card struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	State string `json:"state"`
}

// Cards lists the workspaces for the picker.
func (v *Vault) Cards() []Card {
	list, err := workspace.NewService(v.db).Workspaces()
	if err != nil {
		return nil
	}
	out := []Card{}
	for _, w := range list {
		state := v.State(w.ID)
		// Before first setup only the first workspace exists; after it, a
		// workspace without a passphrase cannot be entered.
		if state == StateSetup && v.anySet() {
			continue
		}
		out = append(out, Card{ID: w.ID, Name: w.Name, State: state})
	}
	return out
}

func derive(pass string, salt []byte, t, m uint32, threads uint8) []byte {
	return argon2.IDKey([]byte(pass), salt, t, m, threads, 32)
}

func seal(key, plain []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(aead.Seal(nonce, nonce, plain, nil)), nil
}

func unseal(key []byte, sealed string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	n := aead.NonceSize()
	if len(raw) < n {
		return nil, ErrWrong
	}
	plain, err := aead.Open(nil, raw[:n], raw[n:], nil)
	if err != nil {
		return nil, ErrWrong // GCM authentication failed: wrong passphrase
	}
	return plain, nil
}

func checkLength(pass string) error {
	if len([]rune(pass)) < MinLength {
		return ErrShort
	}
	return nil
}

// store wraps dataKey under pass for ws, replacing any stored wrapping.
func (v *Vault) store(q sqlitedb.Querier, ws, pass string, dataKey []byte) error {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	wrapped, err := seal(derive(pass, salt, kdfTime, kdfMemory, kdfThreads), dataKey)
	if err != nil {
		return err
	}
	_, err = q.Exec(`INSERT INTO vaults (workspace_id, salt, kdf_time, kdf_memory, kdf_threads, wrapped, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(workspace_id) DO UPDATE SET salt = excluded.salt, kdf_time = excluded.kdf_time, kdf_memory = excluded.kdf_memory,
		kdf_threads = excluded.kdf_threads, wrapped = excluded.wrapped, updated_at = excluded.updated_at`,
		ws, base64.StdEncoding.EncodeToString(salt), kdfTime, kdfMemory, kdfThreads, wrapped, sqlitedb.Stamp(v.now()))
	return err
}

// open unwraps ws's data key with pass.
func (v *Vault) open(ws, pass string) ([]byte, error) {
	var salt64, wrapped string
	var t, m uint32
	var threads uint8
	err := v.db.QueryRow(`SELECT salt, kdf_time, kdf_memory, kdf_threads, wrapped FROM vaults WHERE workspace_id = ?`, ws).Scan(&salt64, &t, &m, &threads, &wrapped)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotSet
	}
	if err != nil {
		return nil, err
	}
	salt, err := base64.StdEncoding.DecodeString(salt64)
	if err != nil {
		return nil, err
	}
	return unseal(derive(pass, salt, t, m, threads), wrapped)
}

func (v *Vault) unlock(ws string, key []byte) error {
	box := v.keys.Box(ws)
	if !box.IsLocked() {
		return nil
	}
	if err := box.Unlock(key); err != nil {
		return err
	}
	v.fireUnlock(ws)
	return nil
}

// Setup sets the first passphrase (first use of the server): it names the
// first workspace and unlocks it.
func (v *Vault) Setup(name, pass string) (string, error) {
	if err := checkLength(pass); err != nil {
		return "", err
	}
	v.login.Lock()
	defer v.login.Unlock()
	if v.anySet() {
		return "", ErrIsSet
	}
	ws := workspace.DefaultID
	key, err := os.ReadFile(v.keyFile)
	legacy := err == nil
	if !legacy {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return "", err
		}
	}
	if err := v.store(v.db, ws, pass, key); err != nil {
		return "", err
	}
	if name = strings.TrimSpace(name); name != "" {
		_ = workspace.Rename(v.db, ws, name)
	}
	if err := v.unlock(ws, key); err != nil {
		return "", err
	}
	if legacy {
		if err := os.Remove(v.keyFile); err != nil {
			return "", fmt.Errorf("passphrase set, but the old key file could not be removed: %w", err)
		}
	}
	return v.newSession(ws), nil
}

// SignIn signs in to ws: it unlocks the workspace if it was locked.
func (v *Vault) SignIn(ws, pass string) (string, error) {
	v.login.Lock()
	defer v.login.Unlock()
	key, err := v.open(ws, pass)
	if err != nil {
		return "", err
	}
	if err := v.unlock(ws, key); err != nil {
		return "", err
	}
	return v.newSession(ws), nil
}

// Create makes a workspace with its own passphrase and key. authorizer must
// be the passphrase of some existing workspace: someone who merely reaches
// the page cannot create workspaces.
func (v *Vault) Create(name, pass, authorizer string) (workspace.Workspace, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 60 {
		return workspace.Workspace{}, "", errors.New("a workspace needs a name (up to 60 characters)")
	}
	if err := checkLength(pass); err != nil {
		return workspace.Workspace{}, "", err
	}
	v.login.Lock()
	defer v.login.Unlock()
	if !v.authorized(authorizer) {
		return workspace.Workspace{}, "", fmt.Errorf("%w: give the passphrase of an existing workspace to create one", ErrWrong)
	}
	for _, c := range v.Cards() {
		if strings.EqualFold(c.Name, name) {
			return workspace.Workspace{}, "", fmt.Errorf("there is already a workspace named %q", name)
		}
	}
	id := make([]byte, 6)
	_, _ = rand.Read(id)
	w, err := workspace.Create(v.db, "w"+hex.EncodeToString(id), name)
	if err != nil {
		return workspace.Workspace{}, "", err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return workspace.Workspace{}, "", err
	}
	if err := v.store(v.db, w.ID, pass, key); err != nil {
		return workspace.Workspace{}, "", err
	}
	if err := v.unlock(w.ID, key); err != nil {
		return workspace.Workspace{}, "", err
	}
	return w, v.newSession(w.ID), nil
}

// authorized reports whether pass opens any workspace's vault.
func (v *Vault) authorized(pass string) bool {
	rows, err := v.db.Query(`SELECT workspace_id FROM vaults`)
	if err != nil {
		return false
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		if _, err := v.open(id, pass); err == nil {
			return true
		}
	}
	return false
}

// Verify checks ws's passphrase again, for actions that ask for it on top
// of a session (revealing a secret, trusting one, dangerously allow).
// Wrong guesses are slowed down like sign-ins.
func (v *Vault) Verify(ws, pass string) error {
	who := "verify " + ws
	if d := v.limits.Wait(who, v.now()); d > 0 {
		return fmt.Errorf("too many wrong passphrases; try again in %ds", int(d.Seconds())+1)
	}
	if _, err := v.open(ws, pass); err != nil {
		v.limits.Fail(who, v.now())
		if errors.Is(err, ErrNotSet) {
			return err
		}
		return ErrWrong
	}
	v.limits.Clear(who)
	return nil
}

// Change rewraps ws's data key under a new passphrase. Sessions stay.
func (v *Vault) Change(ws, old, next string) error {
	if err := checkLength(next); err != nil {
		return err
	}
	v.login.Lock()
	defer v.login.Unlock()
	key, err := v.open(ws, old)
	if err != nil {
		return err
	}
	return v.store(v.db, ws, next, key)
}

// Lock forgets ws's key and ends its sessions: its agents stop calling
// models until someone signs in to it again.
func (v *Vault) Lock(ws string) {
	v.keys.Box(ws).Lock()
	v.mu.Lock()
	for t, s := range v.sessions {
		if s.workspace == ws {
			delete(v.sessions, t)
		}
	}
	v.mu.Unlock()
}

func (v *Vault) newSession(ws string) string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	token := base64.RawURLEncoding.EncodeToString(b)
	v.mu.Lock()
	v.sessions[token] = session{workspace: ws, expires: v.now().Add(sessionTTL)}
	v.mu.Unlock()
	return token
}

// Session returns the workspace a session token belongs to.
func (v *Vault) Session(token string) (string, bool) {
	if token == "" {
		return "", false
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	for t, s := range v.sessions {
		if v.now().After(s.expires) {
			delete(v.sessions, t)
		}
	}
	for t, s := range v.sessions {
		if subtle.ConstantTimeCompare([]byte(t), []byte(token)) == 1 {
			return s.workspace, true
		}
	}
	return "", false
}

// SignOut ends one session.
func (v *Vault) SignOut(token string) {
	v.mu.Lock()
	delete(v.sessions, token)
	v.mu.Unlock()
}

// ---- limiting wrong guesses -------------------------------------------------------

// Limiter counts wrong passphrases (or channel link passphrases) per client address. After freeTries in a
// row, each further attempt must wait (1s, 2s, 4s… up to a minute) and is
// refused at once until then, never queued. A right passphrase, or a quiet
// quarter of an hour, clears the count.
type Limiter struct {
	mu   sync.Mutex
	seen map[string]*strikes
}

type strikes struct {
	n    int
	last time.Time
}

const freeTries = 3

func (l *Limiter) Wait(addr string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.seen[addr]
	if s == nil || now.Sub(s.last) > 15*time.Minute || s.n < freeTries {
		return 0
	}
	delay := min(time.Second<<min(s.n-freeTries, 6), time.Minute)
	return max(s.last.Add(delay).Sub(now), 0)
}

func (l *Limiter) Fail(addr string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.seen == nil {
		l.seen = map[string]*strikes{}
	}
	s := l.seen[addr]
	if s == nil || now.Sub(s.last) > 15*time.Minute {
		s = &strikes{}
		l.seen[addr] = s
	}
	s.n++
	s.last = now
}

func (l *Limiter) Clear(addr string) {
	l.mu.Lock()
	delete(l.seen, addr)
	l.mu.Unlock()
}

// trusted are reverse proxies (TrustProxies) whose X-Forwarded-For and
// X-Forwarded-Proto headers are believed. Others' headers are ignored: any
// client can send them.
var trusted []netip.Prefix

// TrustProxies sets the reverse proxies in front of the server (call once,
// at startup).
func TrustProxies(prefixes []netip.Prefix) { trusted = prefixes }

func fromTrusted(addr string) bool {
	ip, err := netip.ParseAddr(addr)
	if err != nil {
		return false
	}
	for _, p := range trusted {
		if p.Contains(ip.Unmap()) {
			return true
		}
	}
	return false
}

func remote(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ClientAddr is the visitor's address: the connection's, or, behind a
// trusted proxy, the nearest address in X-Forwarded-For that is not one of
// the trusted proxies (each proxy appends the address it saw).
func ClientAddr(r *http.Request) string {
	addr := remote(r)
	if !fromTrusted(addr) {
		return addr
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		hop := strings.TrimSpace(hops[i])
		if hop == "" {
			continue
		}
		if !fromTrusted(hop) {
			return hop
		}
	}
	return addr
}

// secure reports whether the browser reached us over HTTPS: directly, or
// through a trusted proxy that says so.
func secure(r *http.Request) bool {
	return r.TLS != nil || (fromTrusted(remote(r)) && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"))
}

// ---- HTTP ----------------------------------------------------------------------

// sameOrigin rejects requests a browser sends on behalf of another site.
func sameOrigin(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		if err != nil || !strings.EqualFold(u.Host, r.Host) {
			return false
		}
	}
	return true
}

func token(r *http.Request) string {
	if c, err := r.Cookie(CookieName); err == nil {
		return c.Value
	}
	return ""
}

func (v *Vault) setCookie(w http.ResponseWriter, r *http.Request, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: secure(r),
	})
}

func reply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// public paths: the page itself and the runtime's scripts. Everything that
// reads or changes state (runtime calls, the event stream) needs a session.
func public(path string) bool {
	switch {
	case path == "/wails/runtime.js", path == "/wails/transport.js", path == "/wails/custom.js":
		return true
	case strings.HasPrefix(path, "/wails/"), strings.HasPrefix(path, "/xhaos/"):
		return false
	}
	return true // static frontend files: code, no data
}

// LinkPrefix is where published channels take messages from outside
// (/xhaos/channel/<workspace>/<channel>). Those requests carry the channel's
// own passphrase instead of a session, and need that workspace unlocked.
const LinkPrefix = "/xhaos/channel/"

type ctxKey struct{}

// WorkspaceOf is the signed-in workspace of a request that passed
// Middleware ("" otherwise).
func WorkspaceOf(ctx context.Context) string {
	ws, _ := ctx.Value(ctxKey{}).(string)
	return ws
}

// Middleware serves /xhaos/auth and requires a session for everything that
// is not public. A session reaches only its own workspace: every runtime
// call is checked (checkCall) before Wails runs it, and the request carries
// the workspace (WorkspaceOf) for the event stream. guarded handles /xhaos/
// paths behind the session check; links handles LinkPrefix, which checks
// its own passphrases.
func (v *Vault) Middleware(guarded, links http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path
			if path == "/xhaos/auth" || strings.HasPrefix(path, "/xhaos/auth/") {
				v.serveAuth(w, r)
				return
			}
			if links != nil && strings.HasPrefix(path, LinkPrefix) {
				ws, _, _ := strings.Cut(strings.TrimPrefix(path, LinkPrefix), "/")
				if v.State(ws) != StateUnlocked {
					reply(w, http.StatusServiceUnavailable, map[string]string{"error": "this workspace is locked: sign in to it on Xhaos's page first"})
					return
				}
				links.ServeHTTP(w, r)
				return
			}
			if public(path) {
				next.ServeHTTP(w, r)
				return
			}
			ws, ok := v.Session(token(r))
			if !ok {
				reply(w, http.StatusUnauthorized, map[string]string{"error": "sign in first"})
				return
			}
			r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, ws))
			if strings.HasPrefix(path, "/wails/") && r.Method == http.MethodPost {
				if why := v.checkCall(r, token(r), ws); why != "" {
					slog.Warn("auth: call refused", "workspace", ws, "why", why)
					reply(w, http.StatusForbidden, map[string]string{"error": why})
					return
				}
			}
			if strings.HasPrefix(path, "/xhaos/") {
				guarded.ServeHTTP(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ---- the workspace check on runtime calls --------------------------------------

// pending holds the pieces of a large runtime call (Wails splits those into
// chunks): the call is checked once whole, before its last piece goes on.
type pending struct {
	parts map[int][]byte
	total int
	at    time.Time
}

const maxCall = 96 << 20

// checkCall reads a runtime call and returns why it reaches outside ws, or
// "". Every service method that touches a workspace's data names the
// workspace, as an argument or as a workspaceId field; a call may name only
// the session's. (cmd/xhaos tests that every bound method does.)
func (v *Vault) checkCall(r *http.Request, tok, ws string) string {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxCall+1))
	if err != nil || len(body) > maxCall {
		return "call too large"
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	if id := r.Header.Get("x-wails-chunk-id"); id != "" {
		var index, total int
		fmt.Sscan(r.Header.Get("x-wails-chunk-index"), &index)
		fmt.Sscan(r.Header.Get("x-wails-chunk-total"), &total)
		if total <= 0 || total > 4096 || index < 0 || index >= total {
			return "bad chunk"
		}
		key := tok + "|" + id
		v.mu.Lock()
		for k, p := range v.chunks {
			if v.now().Sub(p.at) > 2*time.Minute {
				delete(v.chunks, k)
			}
		}
		p := v.chunks[key]
		if p == nil {
			p = &pending{parts: map[int][]byte{}, total: total, at: v.now()}
			v.chunks[key] = p
		}
		p.parts[index] = body
		size := 0
		for _, b := range p.parts {
			size += len(b)
		}
		done := len(p.parts) == p.total
		if done || size > maxCall {
			delete(v.chunks, key)
		}
		v.mu.Unlock()
		if size > maxCall {
			return "call too large"
		}
		if !done {
			return "" // a piece: stored by Wails, run only once whole (checked below)
		}
		var whole []byte
		for i := 0; i < p.total; i++ {
			whole = append(whole, p.parts[i]...)
		}
		return v.checkArgs(whole, ws)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		args := r.URL.Query().Get("args")
		return v.checkArgs([]byte(`{"args":`+orNull(args)+`}`), ws)
	}
	return v.checkArgs(body, ws)
}

func orNull(s string) string {
	if strings.TrimSpace(s) == "" {
		return "null"
	}
	return s
}

func (v *Vault) checkArgs(raw []byte, ws string) string {
	var call struct {
		Args json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal(raw, &call); err != nil {
		return "unreadable call"
	}
	if len(call.Args) == 0 || string(call.Args) == "null" {
		return ""
	}
	var decoded any
	if err := json.Unmarshal(call.Args, &decoded); err != nil {
		return "unreadable call"
	}
	// A bound-method call carries its arguments one level down:
	// {"call-id": …, "methodName": …, "args": [the method's arguments]}.
	var args []any
	switch x := decoded.(type) {
	case []any:
		args = x
	case map[string]any:
		if inner, ok := x["args"].([]any); ok {
			args = inner
		}
		if why := named(x, ws); why != "" {
			return why
		}
	default:
		args = []any{x}
	}
	for _, a := range args {
		// A plain string argument that names another workspace.
		if s, ok := a.(string); ok && s != ws && v.isWorkspace(s) {
			return "this session belongs to another workspace"
		}
		if why := named(a, ws); why != "" {
			return why
		}
	}
	return ""
}

// named finds a workspaceId field (at any depth) that is not ws.
func named(a any, ws string) string {
	switch x := a.(type) {
	case map[string]any:
		for k, val := range x {
			if strings.EqualFold(k, "workspaceId") || strings.EqualFold(k, "workspace_id") {
				if s, ok := val.(string); ok && s != "" && s != ws {
					return "this session belongs to another workspace"
				}
			}
			if why := named(val, ws); why != "" {
				return why
			}
		}
	case []any:
		for _, val := range x {
			if why := named(val, ws); why != "" {
				return why
			}
		}
	}
	return ""
}

func (v *Vault) isWorkspace(id string) bool {
	if len(id) > 64 {
		return false
	}
	var n int
	_ = v.db.QueryRow(`SELECT COUNT(*) FROM workspaces WHERE id = ?`, id).Scan(&n)
	return n > 0
}

// ---- sign-in endpoints -----------------------------------------------------------

func (v *Vault) status(r *http.Request) map[string]any {
	out := map[string]any{"setup": !v.anySet(), "workspaces": v.Cards(), "minLength": MinLength, "signedIn": false}
	if ws, ok := v.Session(token(r)); ok {
		out["signedIn"], out["workspace"] = true, ws
		if w, err := workspace.Get(v.db, ws); err == nil {
			out["workspaceName"] = w.Name
		}
		out["state"] = v.State(ws)
	} else if !v.anySet() {
		out["state"] = StateSetup
	} else {
		out["state"] = StateLocked
	}
	return out
}

func (v *Vault) serveAuth(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/xhaos/auth" {
		reply(w, http.StatusOK, v.status(r))
		return
	}
	if r.Method != http.MethodPost || !sameOrigin(r) || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		reply(w, http.StatusForbidden, map[string]string{"error": errBadForm.Error()})
		return
	}
	var in struct{ Workspace, Name, Passphrase, Next, Authorizer string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil {
		reply(w, http.StatusBadRequest, map[string]string{"error": errBadForm.Error()})
		return
	}
	fail := func(err error) {
		status := http.StatusBadRequest
		if errors.Is(err, ErrWrong) {
			status = http.StatusUnauthorized
		}
		reply(w, status, map[string]string{"error": err.Error()})
	}
	addr := ClientAddr(r)
	limited := func() bool {
		if d := v.limits.Wait(addr, v.now()); d > 0 {
			secs := max(int((d+time.Second-1)/time.Second), 1)
			w.Header().Set("Retry-After", fmt.Sprint(secs))
			reply(w, http.StatusTooManyRequests, map[string]string{"error": fmt.Sprintf("too many wrong passphrases; try again in %d s", secs)})
			return true
		}
		return false
	}
	wrong := func(err error) {
		if errors.Is(err, ErrWrong) {
			v.limits.Fail(addr, v.now())
			slog.Warn("auth: wrong passphrase", "from", addr)
		}
		fail(err)
	}
	sessionWS, signedIn := v.Session(token(r))
	switch r.URL.Path {
	case "/xhaos/auth/setup":
		t, err := v.Setup(in.Name, in.Passphrase)
		if err != nil {
			fail(err)
			return
		}
		v.setCookie(w, r, t, int(sessionTTL/time.Second))
	case "/xhaos/auth/signin":
		if limited() {
			return
		}
		ws := in.Workspace
		if cards := v.Cards(); ws == "" && len(cards) == 1 {
			ws = cards[0].ID // one workspace: no need to name it
		}
		t, err := v.SignIn(ws, in.Passphrase)
		if err != nil {
			wrong(err)
			return
		}
		v.limits.Clear(addr)
		slog.Warn("auth: signed in", "workspace", ws, "from", addr) // shown by default: a new browser got in
		v.SignOut(token(r))
		v.setCookie(w, r, t, int(sessionTTL/time.Second))
	case "/xhaos/auth/create":
		if limited() {
			return
		}
		ws, t, err := v.Create(in.Name, in.Passphrase, in.Authorizer)
		if err != nil {
			wrong(err)
			return
		}
		v.limits.Clear(addr)
		slog.Warn("auth: workspace created", "workspace", ws.ID, "name", ws.Name, "from", addr)
		v.SignOut(token(r))
		v.setCookie(w, r, t, int(sessionTTL/time.Second))
	case "/xhaos/auth/change":
		if limited() {
			return
		}
		if !signedIn {
			reply(w, http.StatusUnauthorized, map[string]string{"error": "sign in first"})
			return
		}
		if err := v.Change(sessionWS, in.Passphrase, in.Next); err != nil {
			wrong(err)
			return
		}
	case "/xhaos/auth/signout":
		v.SignOut(token(r))
		v.setCookie(w, r, "", -1)
	case "/xhaos/auth/lock":
		if !signedIn {
			reply(w, http.StatusUnauthorized, map[string]string{"error": "sign in first"})
			return
		}
		v.Lock(sessionWS)
		slog.Warn("auth: workspace locked", "workspace", sessionWS, "from", addr)
		v.setCookie(w, r, "", -1)
	default:
		reply(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	reply(w, http.StatusOK, map[string]any{"ok": true})
}
