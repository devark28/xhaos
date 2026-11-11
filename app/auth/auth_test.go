package auth

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devark28/xhaos/app/secretbox"
	"github.com/devark28/xhaos/app/sqlitedb"
	"github.com/devark28/xhaos/app/workspace"
)

const pass = "correct horse battery"

func open(t *testing.T, dir string) (*Vault, *secretbox.Keyring) {
	t.Helper()
	db, err := sqlitedb.Open(filepath.Join(dir, "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := workspace.Migrate(db); err != nil {
		t.Fatal(err)
	}
	v, keys, err := Open(db, filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	return v, keys
}

const home = workspace.DefaultID

func TestWorkspacesHaveTheirOwnPassphraseAndKey(t *testing.T) {
	dir := t.TempDir()
	v, keys := open(t, dir)
	if v.State(home) != StateSetup || !keys.Box(home).IsLocked() {
		t.Fatalf("fresh: %s", v.State(home))
	}
	if _, err := v.Setup("Home", "short"); err != ErrShort {
		t.Fatalf("short = %v", err)
	}
	if _, err := v.Setup("Home", pass); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Setup("Again", pass); err != ErrIsSet {
		t.Fatalf("second setup = %v", err)
	}
	if cards := v.Cards(); len(cards) != 1 || cards[0].Name != "Home" || cards[0].State != StateUnlocked {
		t.Fatalf("cards = %+v", cards)
	}
	sealedHome, _ := keys.Box(home).Seal("home secret")
	// A second workspace: only with an existing workspace's passphrase.
	if _, _, err := v.Create("Client", "client passphrase here", "a stranger's guess"); err == nil {
		t.Fatal("created without authorization")
	}
	if _, _, err := v.Create("home", "client passphrase here", pass); err == nil {
		t.Fatal("two workspaces with the same name")
	}
	client, tok, err := v.Create("Client", "client passphrase here", pass)
	if err != nil || tok == "" {
		t.Fatal(err)
	}
	if ws, ok := v.Session(tok); !ok || ws != client.ID {
		t.Fatalf("the new session belongs to %q", ws)
	}
	sealedClient, _ := keys.Box(client.ID).Seal("client secret")
	if _, err := keys.Box(client.ID).Unseal(sealedHome); err == nil {
		t.Fatal("one workspace's key opens another's values")
	}
	// After a restart: everything locked; each passphrase opens only its own.
	v2, keys2 := open(t, dir)
	if v2.State(home) != StateLocked || v2.State(client.ID) != StateLocked {
		t.Fatal("not locked after restart")
	}
	if _, err := v2.SignIn(client.ID, pass); err != ErrWrong {
		t.Fatalf("home's passphrase on the client workspace = %v", err)
	}
	started := make(chan string, 4)
	v2.OnUnlock = func(ws string) { started <- ws }
	ctok, err := v2.SignIn(client.ID, "client passphrase here")
	if err != nil {
		t.Fatal(err)
	}
	if got := <-started; got != client.ID {
		t.Fatalf("unlocked %q", got)
	}
	if v2.State(home) != StateLocked {
		t.Fatal("signing in to one workspace unlocked another")
	}
	if got, err := keys2.Box(client.ID).Unseal(sealedClient); err != nil || got != "client secret" {
		t.Fatalf("client value = %q %v", got, err)
	}
	// Change: the same key, a new passphrase.
	if err := v2.Change(client.ID, "nope nope nope", "client passphrase two"); err != ErrWrong {
		t.Fatalf("change with a wrong passphrase = %v", err)
	}
	if err := v2.Change(client.ID, "client passphrase here", "client passphrase two"); err != nil {
		t.Fatal(err)
	}
	if err := v2.Verify(client.ID, "client passphrase two"); err != nil {
		t.Fatal(err)
	}
	// Lock: the key goes, its sessions end; the other workspace is untouched.
	htok, _ := v2.SignIn(home, pass)
	v2.Lock(client.ID)
	if v2.State(client.ID) != StateLocked || !keys2.Box(client.ID).IsLocked() {
		t.Fatal("lock kept the key")
	}
	if _, ok := v2.Session(ctok); ok {
		t.Fatal("a session of a locked workspace still works")
	}
	if _, ok := v2.Session(htok); !ok || v2.State(home) != StateUnlocked {
		t.Fatal("locking one workspace touched another")
	}
}

// Before workspaces, one passphrase guarded the server: its vault becomes the
// first workspace's, so nothing is re-entered.
func TestSingleVaultBecomesTheFirstWorkspace(t *testing.T) {
	dir := t.TempDir()
	db, _ := sqlitedb.Open(filepath.Join(dir, "x.db"))
	workspace.Migrate(db)
	key := make([]byte, 32)
	rand.Read(key)
	salt := make([]byte, 16)
	rand.Read(salt)
	wrapped, _ := seal(derive(pass, salt, kdfTime, kdfMemory, kdfThreads), key)
	db.Exec(`CREATE TABLE vault (id INTEGER PRIMARY KEY CHECK (id = 1), salt TEXT NOT NULL, kdf_time INTEGER NOT NULL, kdf_memory INTEGER NOT NULL, kdf_threads INTEGER NOT NULL, wrapped TEXT NOT NULL, updated_at TEXT NOT NULL)`)
	db.Exec(`INSERT INTO vault VALUES (1, ?, ?, ?, ?, ?, '2026-01-01T00:00:00Z')`, base64.StdEncoding.EncodeToString(salt), kdfTime, kdfMemory, kdfThreads, wrapped)
	db.Close()
	v, keys := open(t, dir)
	if v.State(home) != StateLocked {
		t.Fatalf("state = %s", v.State(home))
	}
	if _, err := v.SignIn(home, pass); err != nil {
		t.Fatal(err)
	}
	old := secretbox.Locked()
	old.Unlock(key)
	sealed, _ := old.Seal("sk-old")
	if got, _ := keys.Box(home).Unseal(sealed); got != "sk-old" {
		t.Fatal("the old key did not carry over")
	}
}

// A server that used secret.key keeps its key: the first passphrase wraps it
// and deletes the file.
func TestOldKeyFileIsWrappedAndRemoved(t *testing.T) {
	dir := t.TempDir()
	key := make([]byte, 32)
	rand.Read(key)
	keyFile := filepath.Join(dir, "secret.key")
	os.WriteFile(keyFile, key, 0o600)
	old := secretbox.Locked()
	old.Unlock(key)
	sealed, _ := old.Seal("sk-old")
	v, keys := open(t, dir)
	if v.State(home) != StateSetup || keys.Box(home).IsLocked() {
		t.Fatal("the key file should keep the server usable until setup")
	}
	if _, err := v.Setup("", pass); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(keyFile); !os.IsNotExist(err) {
		t.Fatal("key file still on disk")
	}
	v3, keys3 := open(t, dir)
	v3.SignIn(home, pass)
	if got, _ := keys3.Box(home).Unseal(sealed); got != "sk-old" {
		t.Fatalf("old secret: %q", got)
	}
}

// The middleware: sign-in, and a session reaching only its own workspace,
// whatever form the call takes.
func TestMiddlewareKeepsSessionsInTheirWorkspace(t *testing.T) {
	v, _ := open(t, t.TempDir())
	var ran atomic.Int32
	app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { ran.Add(1); w.Write([]byte("app")) })
	events := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("events:" + WorkspaceOf(r.Context()))) })
	srv := httptest.NewServer(v.Middleware(events, nil, nil, nil)(app))
	defer srv.Close()
	do := func(method, path, body string, hdr map[string]string) (*http.Response, string) {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		for k, val := range hdr {
			req.Header.Set(k, val)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var b bytes.Buffer
		b.ReadFrom(res.Body)
		res.Body.Close()
		return res, b.String()
	}
	jsonHdr := map[string]string{"Content-Type": "application/json"}
	cookieOf := func(res *http.Response) string {
		for _, c := range res.Cookies() {
			if c.Name == CookieName {
				if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
					t.Fatalf("cookie %+v", c)
				}
				return c.Value
			}
		}
		return ""
	}
	if res, _ := do("GET", "/index.js", "", nil); res.StatusCode != 200 {
		t.Fatalf("static file: %d", res.StatusCode)
	}
	for _, p := range []string{"/wails/runtime", "/xhaos/events"} {
		if res, _ := do("POST", p, "{}", nil); res.StatusCode != 401 {
			t.Fatalf("%s without a session: %d", p, res.StatusCode)
		}
	}
	if res, _ := do("POST", "/xhaos/auth/setup", `{"passphrase":"`+pass+`"}`, map[string]string{"Content-Type": "application/json", "Origin": "https://evil.example"}); res.StatusCode != 403 {
		t.Fatalf("cross-origin setup: %d", res.StatusCode)
	}
	res, _ := do("POST", "/xhaos/auth/setup", `{"name":"Home","passphrase":"`+pass+`"}`, jsonHdr)
	homeCookie := cookieOf(res)
	res, body := do("POST", "/xhaos/auth/create", `{"name":"Client","passphrase":"client passphrase here","authorizer":"`+pass+`"}`, jsonHdr)
	clientCookie := cookieOf(res)
	if res.StatusCode != 200 || clientCookie == "" {
		t.Fatalf("create: %d %s", res.StatusCode, body)
	}
	clientID, _ := v.Session(clientCookie)
	// The picker lists both workspaces, by name only, to anyone.
	_, body = do("GET", "/xhaos/auth", "", nil)
	var st struct {
		Workspaces []Card
		SignedIn   bool
	}
	json.Unmarshal([]byte(body), &st)
	if len(st.Workspaces) != 2 || st.SignedIn || strings.Contains(body, "salt") || strings.Contains(body, "wrapped") {
		t.Fatalf("status: %s", body)
	}
	home := map[string]string{"Cookie": CookieName + "=" + homeCookie}
	if _, body := do("GET", "/xhaos/events", "", home); body != "events:default" {
		t.Fatalf("events carry the session's workspace: %q", body)
	}
	call := func(hdr map[string]string, args string) int {
		r, _ := do("POST", "/wails/runtime", `{"object":1,"method":2,"args":`+args+`}`, hdr)
		return r.StatusCode
	}
	before := ran.Load()
	for _, ok := range []string{`["default"]`, `[{"workspaceId":"default","name":"x"}]`, `["default", 5]`, `[]`, `["some text"]`} {
		if code := call(home, ok); code != 200 {
			t.Errorf("own workspace %s = %d", ok, code)
		}
	}
	for _, bad := range []string{`["` + clientID + `"]`, `[{"workspaceId":"` + clientID + `"}]`, `[{"a":{"b":[{"workspaceId":"` + clientID + `"}]}}]`, `[{"WorkspaceID":"anything else"}]`} {
		if code := call(home, bad); code != 403 {
			t.Errorf("another workspace %s = %d", bad, code)
		}
	}
	// The shape the Wails client really sends: the method's arguments one
	// level down, beside the call id and method name.
	wails := func(args string) string {
		return `{"call-id":"c1","methodName":"github.com/devark28/xhaos/app/memory.Service.Memories","args":` + args + `}`
	}
	if code := call(home, wails(`["default"]`)); code != 200 {
		t.Errorf("own workspace, Wails shape = %d", code)
	}
	for _, bad := range []string{wails(`["` + clientID + `"]`), wails(`[{"workspaceId":"` + clientID + `"}]`)} {
		if code := call(home, bad); code != 403 {
			t.Errorf("another workspace, Wails shape %s = %d", bad, code)
		}
	}
	if ran.Load() != before+6 {
		t.Fatalf("refused calls reached the app (%d runs)", ran.Load()-before)
	}
	// A call split into chunks is checked whole, before its last piece.
	chunked := func(args string) int {
		whole := `{"object":1,"method":2,"args":` + args + `}`
		mid := len(whole) / 2
		code := 0
		for i, part := range []string{whole[:mid], whole[mid:]} {
			r, _ := do("POST", "/wails/runtime", part, map[string]string{"Cookie": home["Cookie"], "x-wails-chunk-id": fmt.Sprint(len(args)), "x-wails-chunk-index": fmt.Sprint(i), "x-wails-chunk-total": "2"})
			code = r.StatusCode
		}
		return code
	}
	if code := chunked(`[{"workspaceId":"default","data":"` + strings.Repeat("A", 200) + `"}]`); code != 200 {
		t.Errorf("own chunked call = %d", code)
	}
	if code := chunked(`[{"workspaceId":"` + clientID + `","data":"` + strings.Repeat("B", 201) + `"}]`); code != 403 {
		t.Errorf("chunked call to another workspace = %d", code)
	}
	// Args in the URL instead of the body.
	if r, _ := do("POST", "/wails/runtime?object=1&method=2&args="+`%5B%22`+clientID+`%22%5D`, "", home); r.StatusCode != 403 {
		t.Errorf("query-string call to another workspace = %d", r.StatusCode)
	}
	// Lock from a session: its workspace locks, the session ends.
	client := map[string]string{"Cookie": CookieName + "=" + clientCookie, "Content-Type": "application/json"}
	if r, _ := do("POST", "/xhaos/auth/lock", `{}`, client); r.StatusCode != 200 || v.State(clientID) != StateLocked {
		t.Fatalf("lock: %d %s", r.StatusCode, v.State(clientID))
	}
	if r, _ := do("POST", "/wails/runtime", `{"args":[]}`, client); r.StatusCode != 401 {
		t.Fatalf("after lock: %d", r.StatusCode)
	}
	if res, _ := do("POST", "/xhaos/auth/signin", `{"workspace":"`+clientID+`","passphrase":"`+pass+`"}`, jsonHdr); res.StatusCode != 401 {
		t.Fatalf("home's passphrase for the client: %d", res.StatusCode)
	}
}

// Wrong guesses are refused at once after a few, per client, never queued;
// a right passphrase from another client is not held up.
func TestWrongGuessesAreLimitedPerClientWithoutWaiting(t *testing.T) {
	var l Limiter
	now := time.Now()
	for i := 0; i < freeTries; i++ {
		if l.Wait("a", now) != 0 {
			t.Fatalf("attempt %d had to wait", i)
		}
		l.Fail("a", now)
	}
	if d := l.Wait("a", now); d != time.Second {
		t.Fatalf("after %d failures: wait %v", freeTries, d)
	}
	if l.Wait("b", now) != 0 {
		t.Fatal("another client was limited")
	}
	l.Fail("a", now.Add(time.Second))
	if d := l.Wait("a", now.Add(time.Second)); d != 2*time.Second {
		t.Fatalf("next wait %v", d)
	}
	if l.Wait("a", now.Add(20*time.Minute)) != 0 {
		t.Fatal("the count did not expire")
	}
	l.Clear("a")
	if l.Wait("a", now) != 0 {
		t.Fatal("clear did not reset")
	}
}

// Behind a trusted proxy the visitor's address and HTTPS come from its
// headers; from anyone else those headers are ignored.
func TestTrustedProxies(t *testing.T) {
	defer TrustProxies(nil)
	req := func(remote, xff, proto string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		if proto != "" {
			r.Header.Set("X-Forwarded-Proto", proto)
		}
		return r
	}
	TrustProxies(nil)
	if a := ClientAddr(req("127.0.0.1:5000", "203.0.113.9", "https")); a != "127.0.0.1" {
		t.Fatalf("untrusted proxy headers were used: %s", a)
	}
	if secure(req("127.0.0.1:5000", "", "https")) {
		t.Fatal("an untrusted X-Forwarded-Proto made the cookie secure")
	}
	TrustProxies([]netip.Prefix{netip.MustParsePrefix("127.0.0.1/32"), netip.MustParsePrefix("10.0.0.0/8")})
	cases := []struct{ remote, xff, want string }{
		{"127.0.0.1:5000", "203.0.113.9", "203.0.113.9"},
		// A client's own (forged) entry comes first; the proxy appends the real one.
		{"127.0.0.1:5000", "1.2.3.4, 203.0.113.9", "203.0.113.9"},
		// Two trusted hops: skip both.
		{"127.0.0.1:5000", "203.0.113.9, 10.1.2.3", "203.0.113.9"},
		{"127.0.0.1:5000", "", "127.0.0.1"},
		{"198.51.100.7:5000", "203.0.113.9", "198.51.100.7"},
	}
	for _, c := range cases {
		if got := ClientAddr(req(c.remote, c.xff, "")); got != c.want {
			t.Errorf("%s via %q = %s, want %s", c.remote, c.xff, got, c.want)
		}
	}
	if !secure(req("127.0.0.1:5000", "", "https")) || secure(req("198.51.100.7:5000", "", "https")) {
		t.Fatal("X-Forwarded-Proto handling")
	}
}
