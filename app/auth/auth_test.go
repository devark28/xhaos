package auth

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"xhaos/app/secretbox"
	"xhaos/app/sqlitedb"
)

const pass = "correct horse battery"

func open(t *testing.T, dir string) (*Vault, *secretbox.Box) {
	t.Helper()
	db, err := sqlitedb.Open(filepath.Join(dir, "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	v, box, err := Open(db, filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	return v, box
}

func TestSetupLockSignInAndChange(t *testing.T) {
	dir := t.TempDir()
	v, box := open(t, dir)
	var unlocked atomic.Int32
	v.OnUnlock = func() { unlocked.Add(1) }
	if v.State() != StateSetup || !box.IsLocked() {
		t.Fatalf("fresh: %s, locked %v", v.State(), box.IsLocked())
	}
	if _, err := v.Setup("short"); err != ErrShort {
		t.Fatalf("short passphrase: %v", err)
	}
	if _, err := v.Setup(pass); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Setup(pass); err != ErrIsSet {
		t.Fatalf("second setup: %v", err)
	}
	sealed, err := box.Seal("sk-secret")
	if err != nil {
		t.Fatal(err)
	}

	// A restart: locked until the passphrase is entered.
	v2, box2 := open(t, dir)
	v2.OnUnlock = func() { unlocked.Add(1) }
	if v2.State() != StateLocked {
		t.Fatalf("after restart: %s", v2.State())
	}
	if _, err := box2.Unseal(sealed); err != secretbox.ErrLocked {
		t.Fatalf("unseal while locked: %v", err)
	}
	if _, err := v2.SignIn("wrong passphrase!"); err != ErrWrong {
		t.Fatalf("wrong: %v", err)
	}
	tok, err := v2.SignIn(pass)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := box2.Unseal(sealed); got != "sk-secret" || !v2.Valid(tok) || v2.Valid("forged") {
		t.Fatalf("after sign-in: %q valid %v", got, v2.Valid(tok))
	}
	if err := v2.Change("nope nope nope", "new passphrase here"); err != ErrWrong {
		t.Fatalf("change with a wrong old passphrase: %v", err)
	}
	if err := v2.Change(pass, "new passphrase here"); err != nil {
		t.Fatal(err)
	}
	// The same data key, rewrapped: old secrets still open.
	v3, box3 := open(t, dir)
	if _, err := v3.SignIn(pass); err != ErrWrong {
		t.Fatalf("old passphrase after change: %v", err)
	}
	if _, err := v3.SignIn("new passphrase here"); err != nil {
		t.Fatal(err)
	}
	if got, _ := box3.Unseal(sealed); got != "sk-secret" {
		t.Fatalf("secret after change: %q", got)
	}
	time.Sleep(50 * time.Millisecond) // OnUnlock runs in a goroutine
	if unlocked.Load() != 2 {
		t.Fatalf("OnUnlock ran %d times", unlocked.Load())
	}
	v2.SignOut(tok)
	if v2.Valid(tok) {
		t.Fatal("session survived sign-out")
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

	v, box := open(t, dir)
	if v.State() != StateSetup || box.IsLocked() {
		t.Fatal("the key file should keep the server usable until setup")
	}
	if _, err := v.Setup(pass); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(keyFile); !os.IsNotExist(err) {
		t.Fatal("key file still on disk")
	}
	if _, box2 := open(t, dir); !box2.IsLocked() {
		t.Fatal("not locked after restart")
	}
	v3, box3 := open(t, dir)
	v3.SignIn(pass)
	if got, _ := box3.Unseal(sealed); got != "sk-old" {
		t.Fatalf("old secret: %q", got)
	}
}

func TestMiddlewareGuardsCallsAndEvents(t *testing.T) {
	v, _ := open(t, t.TempDir())
	app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("app")) })
	events := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("events")) })
	srv := httptest.NewServer(v.Middleware(events, nil)(app))
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
	json := map[string]string{"Content-Type": "application/json"}

	if res, _ := do("GET", "/index.js", "", nil); res.StatusCode != 200 {
		t.Fatalf("static file: %d", res.StatusCode)
	}
	for _, p := range []string{"/wails/runtime", "/xhaos/events"} {
		if res, _ := do("POST", p, "{}", nil); res.StatusCode != 401 {
			t.Fatalf("%s without a session: %d", p, res.StatusCode)
		}
	}
	// Another site cannot set the passphrase (or sign in) through the browser.
	if res, _ := do("POST", "/xhaos/auth/setup", `{"passphrase":"`+pass+`"}`, map[string]string{"Content-Type": "application/json", "Origin": "https://evil.example"}); res.StatusCode != 403 {
		t.Fatalf("cross-origin setup: %d", res.StatusCode)
	}
	if res, _ := do("POST", "/xhaos/auth/setup", `{"passphrase":"`+pass+`"}`, map[string]string{"Content-Type": "text/plain"}); res.StatusCode != 403 {
		t.Fatalf("form-encoded setup: %d", res.StatusCode)
	}
	res, _ := do("POST", "/xhaos/auth/setup", `{"passphrase":"`+pass+`"}`, json)
	var cookie *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == CookieName {
			cookie = c
		}
	}
	if res.StatusCode != 200 || cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("setup: %d cookie %+v", res.StatusCode, cookie)
	}
	withCookie := map[string]string{"Cookie": CookieName + "=" + cookie.Value}
	if res, body := do("POST", "/wails/runtime", "{}", withCookie); res.StatusCode != 200 || body != "app" {
		t.Fatalf("runtime with a session: %d %q", res.StatusCode, body)
	}
	if res, body := do("GET", "/xhaos/events", "", withCookie); res.StatusCode != 200 || body != "events" {
		t.Fatalf("events with a session: %d %q", res.StatusCode, body)
	}
	_, body := do("GET", "/xhaos/auth", "", withCookie)
	var st struct {
		State    string
		SignedIn bool
	}
	if err := jsonUnmarshal(body, &st); err != nil || st.State != StateUnlocked || !st.SignedIn {
		t.Fatalf("state: %s", body)
	}
	if res, _ := do("POST", "/xhaos/auth/signin", `{"passphrase":"guess guess guess"}`, json); res.StatusCode != 401 {
		t.Fatalf("wrong sign-in: %d", res.StatusCode)
	}
}

func jsonUnmarshal(s string, v any) error { return json.Unmarshal([]byte(s), v) }

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
