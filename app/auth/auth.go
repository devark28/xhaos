// Package auth guards the server with the owner's passphrase.
//
// The passphrase does two things. It signs the browser in: a successful
// sign-in sets an HttpOnly, SameSite=Strict session cookie, and every
// runtime call and the live event stream require one. And it holds the key
// that encrypts provider API keys and MCP secrets: Argon2id turns the
// passphrase into a key-encryption key, which wraps a random data key. Only
// the wrapped key is stored; the data key lives in memory after the first
// sign-in since the server started, so until then the server is locked and
// no agent can call a model.
//
// Changing the passphrase rewraps the same data key, so nothing else is
// re-encrypted. A server that used the older key file (secret.key beside the
// database) keeps using that key: the first passphrase wraps it and the file
// is deleted.
package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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

	"xhaos/app/secretbox"
	"xhaos/app/sqlitedb"
)

const (
	MinLength = 12 // characters

	CookieName = "xhaos_session"
	sessionTTL = 30 * 24 * time.Hour

	// Argon2id cost: about a tenth of a second and 64 MiB per attempt.
	kdfTime    = 3
	kdfMemory  = 64 * 1024 // KiB
	kdfThreads = 4
)

// States, as GET /xhaos/auth reports them.
const (
	StateSetup    = "setup"    // no passphrase yet
	StateLocked   = "locked"   // set, not entered since the server started
	StateUnlocked = "unlocked" // entered; new browsers still sign in
)

var (
	ErrWrong   = errors.New("wrong passphrase")
	ErrShort   = fmt.Errorf("the passphrase needs at least %d characters", MinLength)
	ErrIsSet   = errors.New("a passphrase is already set")
	ErrNotSet  = errors.New("no passphrase is set yet")
	errBadForm = errors.New("bad request")
)

const schema = `
CREATE TABLE IF NOT EXISTS vault (
	id         INTEGER PRIMARY KEY CHECK (id = 1),
	salt       TEXT NOT NULL,
	kdf_time   INTEGER NOT NULL,
	kdf_memory INTEGER NOT NULL,
	kdf_threads INTEGER NOT NULL,
	wrapped    TEXT NOT NULL, -- base64(nonce || AES-GCM(kek, data key))
	updated_at TEXT NOT NULL
);`

// Vault is the passphrase, the box it unlocks, and the signed-in sessions.
type Vault struct {
	db      *sql.DB
	box     *secretbox.Box
	keyFile string // the older key file, migrated on first setup

	login    sync.Mutex           // serializes setup, sign-in and change
	limits   Limiter              // wrong passphrases per client address
	mu       sync.Mutex           // guards sessions
	sessions map[string]time.Time // token -> expiry
	// OnUnlock runs once, after the box first gets its key (resume agents,
	// connect MCP servers).
	OnUnlock func()
	unlocked sync.Once
	now      func() time.Time
}

// Open loads the vault. The box is locked unless no passphrase is set and
// the older key file exists, in which case that key is used until setup.
func Open(db *sql.DB, keyFile string) (*Vault, *secretbox.Box, error) {
	if _, err := db.Exec(schema); err != nil {
		return nil, nil, err
	}
	v := &Vault{db: db, box: secretbox.Locked(), keyFile: keyFile, sessions: map[string]time.Time{}, now: time.Now}
	set, err := v.isSet()
	if err != nil {
		return nil, nil, err
	}
	if !set {
		if key, err := os.ReadFile(keyFile); err == nil {
			if err := v.box.Unlock(key); err != nil {
				return nil, nil, fmt.Errorf("%s: %w", keyFile, err)
			}
		}
	}
	return v, v.box, nil
}

func (v *Vault) isSet() (bool, error) {
	var n int
	err := v.db.QueryRow(`SELECT COUNT(*) FROM vault`).Scan(&n)
	return n > 0, err
}

// State reports setup, locked, or unlocked.
func (v *Vault) State() string {
	if set, _ := v.isSet(); !set {
		return StateSetup
	}
	if v.box.IsLocked() {
		return StateLocked
	}
	return StateUnlocked
}

// Ready runs OnUnlock if the box already has its key (the older key file).
func (v *Vault) Ready() {
	if !v.box.IsLocked() {
		v.fireUnlock()
	}
}

func (v *Vault) fireUnlock() {
	v.unlocked.Do(func() {
		if v.OnUnlock != nil {
			go v.OnUnlock()
		}
	})
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

// store wraps dataKey under pass, replacing any stored wrapping.
func (v *Vault) store(q sqlitedb.Querier, pass string, dataKey []byte) error {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	wrapped, err := seal(derive(pass, salt, kdfTime, kdfMemory, kdfThreads), dataKey)
	if err != nil {
		return err
	}
	_, err = q.Exec(`INSERT INTO vault (id, salt, kdf_time, kdf_memory, kdf_threads, wrapped, updated_at) VALUES (1, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET salt = excluded.salt, kdf_time = excluded.kdf_time, kdf_memory = excluded.kdf_memory,
		kdf_threads = excluded.kdf_threads, wrapped = excluded.wrapped, updated_at = excluded.updated_at`,
		base64.StdEncoding.EncodeToString(salt), kdfTime, kdfMemory, kdfThreads, wrapped, sqlitedb.Stamp(v.now()))
	return err
}

// open unwraps the data key with pass.
func (v *Vault) open(pass string) ([]byte, error) {
	var salt64, wrapped string
	var t, m uint32
	var threads uint8
	err := v.db.QueryRow(`SELECT salt, kdf_time, kdf_memory, kdf_threads, wrapped FROM vault WHERE id = 1`).Scan(&salt64, &t, &m, &threads, &wrapped)
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

func checkLength(pass string) error {
	if len([]rune(pass)) < MinLength {
		return ErrShort
	}
	return nil
}

// Setup sets the first passphrase and signs in. The data key is the older
// key file's if there is one (so saved API keys still decrypt), else new.
func (v *Vault) Setup(pass string) (string, error) {
	if err := checkLength(pass); err != nil {
		return "", err
	}
	v.login.Lock()
	defer v.login.Unlock()
	if set, err := v.isSet(); err != nil || set {
		if err == nil {
			err = ErrIsSet
		}
		return "", err
	}
	key, err := os.ReadFile(v.keyFile)
	legacy := err == nil
	if !legacy {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return "", err
		}
	}
	if err := v.store(v.db, pass, key); err != nil {
		return "", err
	}
	if err := v.box.Unlock(key); err != nil {
		return "", err
	}
	if legacy {
		// The key is wrapped now; the plain copy must not outlive it.
		if err := os.Remove(v.keyFile); err != nil {
			return "", fmt.Errorf("passphrase set, but the old key file could not be removed: %w", err)
		}
	}
	v.fireUnlock()
	return v.newSession(), nil
}

// SignIn checks pass, unlocks the box on the first sign-in since start, and
// returns a session token. (The HTTP layer limits wrong guesses per client.)
func (v *Vault) SignIn(pass string) (string, error) {
	v.login.Lock()
	defer v.login.Unlock()
	key, err := v.open(pass)
	if err != nil {
		return "", err
	}
	if v.box.IsLocked() {
		if err := v.box.Unlock(key); err != nil {
			return "", err
		}
		v.fireUnlock()
	}
	return v.newSession(), nil
}

// Verify checks the passphrase again, for actions that ask for it on top of
// a session (revealing a secret, trusting one to an agent). Wrong guesses
// are slowed down like sign-ins (one count for every client: these calls
// come from signed-in pages, not addresses).
func (v *Vault) Verify(pass string) error {
	const who = "verify"
	if d := v.limits.Wait(who, v.now()); d > 0 {
		return fmt.Errorf("too many wrong passphrases; try again in %ds", int(d.Seconds())+1)
	}
	if _, err := v.open(pass); err != nil {
		v.limits.Fail(who, v.now())
		if errors.Is(err, ErrNotSet) {
			return err
		}
		return ErrWrong
	}
	v.limits.Clear(who)
	return nil
}

// Change rewraps the data key under a new passphrase. Other sessions stay.
func (v *Vault) Change(old, next string) error {
	if err := checkLength(next); err != nil {
		return err
	}
	v.login.Lock()
	defer v.login.Unlock()
	key, err := v.open(old)
	if err != nil {
		return err
	}
	return v.store(v.db, next, key)
}

func (v *Vault) newSession() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	token := base64.RawURLEncoding.EncodeToString(b)
	v.mu.Lock()
	v.sessions[token] = v.now().Add(sessionTTL)
	v.mu.Unlock()
	return token
}

// Valid reports whether token is a live session.
func (v *Vault) Valid(token string) bool {
	if token == "" {
		return false
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	for t, exp := range v.sessions {
		if v.now().After(exp) {
			delete(v.sessions, t)
		}
	}
	for t := range v.sessions {
		if subtle.ConstantTimeCompare([]byte(t), []byte(token)) == 1 {
			return true
		}
	}
	return false
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
// own passphrase instead of a session, and need the server unlocked.
const LinkPrefix = "/xhaos/channel/"

// Middleware serves /xhaos/auth and requires a session for everything that
// is not public. guarded handles /xhaos/ paths behind the session check
// (the event stream); links handles LinkPrefix, which checks its own
// passphrases.
func (v *Vault) Middleware(guarded, links http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path
			if path == "/xhaos/auth" || strings.HasPrefix(path, "/xhaos/auth/") {
				v.serveAuth(w, r)
				return
			}
			if links != nil && strings.HasPrefix(path, LinkPrefix) {
				if v.State() != StateUnlocked {
					reply(w, http.StatusServiceUnavailable, map[string]string{"error": "Xhaos is locked: sign in on its page first", "state": v.State()})
					return
				}
				links.ServeHTTP(w, r)
				return
			}
			if public(path) {
				next.ServeHTTP(w, r)
				return
			}
			if !v.Valid(token(r)) {
				reply(w, http.StatusUnauthorized, map[string]string{"error": "sign in first", "state": v.State()})
				return
			}
			if strings.HasPrefix(path, "/xhaos/") {
				guarded.ServeHTTP(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (v *Vault) serveAuth(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/xhaos/auth" {
		reply(w, http.StatusOK, map[string]any{"state": v.State(), "signedIn": v.Valid(token(r)), "minLength": MinLength})
		return
	}
	if r.Method != http.MethodPost || !sameOrigin(r) || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		reply(w, http.StatusForbidden, map[string]string{"error": errBadForm.Error()})
		return
	}
	var in struct{ Passphrase, Next string }
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
	switch r.URL.Path {
	case "/xhaos/auth/setup":
		t, err := v.Setup(in.Passphrase)
		if err != nil {
			fail(err)
			return
		}
		v.setCookie(w, r, t, int(sessionTTL/time.Second))
	case "/xhaos/auth/signin", "/xhaos/auth/change":
		addr := ClientAddr(r)
		if d := v.limits.Wait(addr, v.now()); d > 0 {
			secs := max(int((d+time.Second-1)/time.Second), 1)
			w.Header().Set("Retry-After", fmt.Sprint(secs))
			reply(w, http.StatusTooManyRequests, map[string]string{"error": fmt.Sprintf("too many wrong passphrases; try again in %d s", secs)})
			return
		}
		if r.URL.Path == "/xhaos/auth/change" {
			if !v.Valid(token(r)) {
				reply(w, http.StatusUnauthorized, map[string]string{"error": "sign in first"})
				return
			}
			if err := v.Change(in.Passphrase, in.Next); err != nil {
				if errors.Is(err, ErrWrong) {
					v.limits.Fail(addr, v.now())
				}
				fail(err)
				return
			}
			break
		}
		t, err := v.SignIn(in.Passphrase)
		if err != nil {
			if errors.Is(err, ErrWrong) {
				v.limits.Fail(addr, v.now())
				slog.Warn("auth: wrong passphrase", "from", addr)
			}
			fail(err)
			return
		}
		v.limits.Clear(addr)
		slog.Warn("auth: signed in", "from", addr) // shown by default: a new browser got in
		v.setCookie(w, r, t, int(sessionTTL/time.Second))
	case "/xhaos/auth/signout":
		v.SignOut(token(r))
		v.setCookie(w, r, "", -1)
	default:
		reply(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	reply(w, http.StatusOK, map[string]any{"state": v.State()})
}
