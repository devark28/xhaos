package secret_test

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/devark28/xhaos/app/agent"
	"github.com/devark28/xhaos/app/backend"
	"github.com/devark28/xhaos/app/secret"
)

const ws = "default"

func open(t *testing.T) *backend.Services {
	t.Helper()
	s, err := backend.Open(filepath.Join(t.TempDir(), "x.db"), backend.Options{Root: t.TempDir(), DisableRuns: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestVaultNeverShowsValues(t *testing.T) {
	s := open(t)
	v, err := s.Secrets.Save(secret.SaveRequest{WorkspaceID: ws, Name: "GITHUB_TOKEN", Description: "push to my repos", Value: "ghp_supersecret123", Hosts: []string{"github.com", " API.github.com ", "github.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(v.Hosts, ",") != "github.com,api.github.com" || v.Description != "push to my repos" {
		t.Fatalf("view = %+v", v)
	}
	var stored string
	s.DB().QueryRow(`SELECT value FROM secrets`).Scan(&stored)
	if stored == "" || strings.Contains(stored, "ghp_supersecret123") {
		t.Fatalf("stored in the clear: %q", stored)
	}
	list, _ := s.Secrets.Secrets(ws)
	for _, raw := range []string{func() string { b, _ := jsonOf(list); return b }(), eventsText(t, s)} {
		if strings.Contains(raw, "ghp_supersecret") {
			t.Fatalf("value leaked: %s", raw)
		}
	}
	// An empty value keeps the old one; a new secret needs one.
	if _, err := s.Secrets.Save(secret.SaveRequest{WorkspaceID: ws, Name: "GITHUB_TOKEN", Description: "changed", Hosts: []string{"github.com"}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Secrets.Reveal(secret.RevealRequest{WorkspaceID: ws, Name: "GITHUB_TOKEN"}); got != "ghp_supersecret123" {
		t.Fatalf("reveal = %q", got)
	}
	if _, err := s.Secrets.Save(secret.SaveRequest{WorkspaceID: ws, Name: "NEW_ONE"}); err == nil {
		t.Fatal("saved a new secret without a value")
	}
	for _, bad := range []string{"lower", "PATH", "LD_PRELOAD", "XHAOS_X", "HTTPS_PROXY", "1ABC", ""} {
		if _, err := s.Secrets.Save(secret.SaveRequest{WorkspaceID: ws, Name: bad, Value: "x"}); err == nil {
			t.Errorf("accepted name %q", bad)
		}
	}
	for _, bad := range []string{"http://github.com", "github.com/path", "*", "localhost", "127.0.0.1", "::1", "0.0.0.0"} {
		if _, err := s.Secrets.Save(secret.SaveRequest{WorkspaceID: ws, Name: "X", Value: "x", Hosts: []string{bad}}); err == nil {
			t.Errorf("accepted host %q", bad)
		}
	}
	if _, err := s.Secrets.Save(secret.SaveRequest{WorkspaceID: ws, Name: "LAN_TOKEN", Value: "x", Hosts: []string{"192.168.1.47", "fd00::5"}}); err != nil {
		t.Errorf("LAN addresses: %v", err)
	}
	// SSH keys: [user@]host[:port], the whole private key, without a passphrase.
	key := genKey(t, "")
	if v, err := s.Secrets.Save(secret.SaveRequest{WorkspaceID: ws, Name: "DEPLOY_SSH", Kind: secret.KindSSH, Value: key, Hosts: []string{"deploy@192.168.1.47", "git@github.com", "nas.home:2222"}}); err != nil || v.Kind != secret.KindSSH {
		t.Fatalf("ssh key = %+v %v", v, err)
	}
	if got, _ := s.Secrets.Reveal(secret.RevealRequest{WorkspaceID: ws, Name: "DEPLOY_SSH"}); got != key {
		t.Errorf("a key without a passphrase is kept as it was: %q", got)
	}
	for _, bad := range []string{"*.github.com", "deploy@", "Bad User@host", "host:99999", "127.0.0.1"} {
		if _, err := s.Secrets.Save(secret.SaveRequest{WorkspaceID: ws, Name: "S2", Kind: secret.KindSSH, Value: key, Hosts: []string{bad}}); err == nil {
			t.Errorf("accepted ssh host %q", bad)
		}
	}
	if _, err := s.Secrets.Save(secret.SaveRequest{WorkspaceID: ws, Name: "S3", Kind: secret.KindSSH, Value: "ghp_not_a_key"}); err == nil {
		t.Error("accepted a token as an ssh key")
	}
	// A key with a passphrase: refused without it or with a wrong one;
	// with it, stored unlocked (ssh then needs no passphrase).
	locked := genKey(t, "key passphrase")
	if _, err := s.Secrets.Save(secret.SaveRequest{WorkspaceID: ws, Name: "S4", Kind: secret.KindSSH, Value: locked}); err == nil || !strings.Contains(err.Error(), "Key passphrase") {
		t.Errorf("a locked key without its passphrase = %v", err)
	}
	if _, err := s.Secrets.Save(secret.SaveRequest{WorkspaceID: ws, Name: "S4", Kind: secret.KindSSH, Value: locked, KeyPassphrase: "wrong"}); err == nil {
		t.Error("a wrong key passphrase was accepted")
	}
	if _, err := s.Secrets.Save(secret.SaveRequest{WorkspaceID: ws, Name: "S4", Kind: secret.KindSSH, Value: locked, KeyPassphrase: "key passphrase"}); err != nil {
		t.Fatal(err)
	}
	unlocked, _ := s.Secrets.Reveal(secret.RevealRequest{WorkspaceID: ws, Name: "S4"})
	if _, err := ssh.ParseRawPrivateKey([]byte(unlocked)); err != nil || unlocked == locked {
		t.Errorf("stored key is not unlocked: %v", err)
	}
	if _, err := s.Secrets.Save(secret.SaveRequest{WorkspaceID: ws, Name: "DEPLOY_SSH", Kind: secret.KindToken, Value: "x"}); err == nil {
		t.Error("changed a secret's kind")
	}
}

func TestGrantsAndPassphrase(t *testing.T) {
	s := open(t)
	s.Secrets.Verify = func(_, p string) error {
		if p != "right passphrase" {
			return errors.New("wrong passphrase")
		}
		return nil
	}
	a, err := s.Agents.Create(agent.CreateRequest{IdempotencyKey: "a", WorkspaceID: ws, Title: "a"})
	if err != nil {
		t.Fatal(err)
	}
	s.Secrets.Save(secret.SaveRequest{WorkspaceID: ws, Name: "TOKEN", Value: "abcd1234", Hosts: []string{"api.example.com"}})
	if _, err := s.Secrets.Reveal(secret.RevealRequest{WorkspaceID: ws, Name: "TOKEN", Passphrase: "nope"}); err == nil {
		t.Fatal("revealed with a wrong passphrase")
	}
	if v, _ := s.Secrets.Reveal(secret.RevealRequest{WorkspaceID: ws, Name: "TOKEN", Passphrase: "right passphrase"}); v != "abcd1234" {
		t.Fatalf("reveal = %q", v)
	}
	sealer := secret.NewSealer(s.Secrets)
	if _, err := sealer.Check(ws, a, []string{"TOKEN"}); err == nil || !strings.Contains(err.Error(), "not granted") {
		t.Fatalf("ungranted = %v", err)
	}
	if _, err := s.Secrets.Grant(secret.GrantRequest{WorkspaceID: ws, Name: "TOKEN", AgentID: a.ID, Mode: secret.Ask}); err != nil {
		t.Fatal(err)
	}
	use, err := sealer.Check(ws, a, []string{"TOKEN"})
	if err != nil || use.Trusted || !strings.Contains(use.Preview, "api.example.com") {
		t.Fatalf("asked use = %+v %v", use, err)
	}
	if _, err := s.Secrets.Grant(secret.GrantRequest{WorkspaceID: ws, Name: "TOKEN", AgentID: a.ID, Mode: secret.Trusted, Passphrase: "nope"}); err == nil {
		t.Fatal("trusted without the passphrase")
	}
	if _, err := s.Secrets.Grant(secret.GrantRequest{WorkspaceID: ws, Name: "TOKEN", AgentID: a.ID, Mode: secret.Trusted, Passphrase: "right passphrase"}); err != nil {
		t.Fatal(err)
	}
	if use, _ := sealer.Check(ws, a, []string{"TOKEN"}); !use.Trusted {
		t.Fatal("not trusted")
	}
	if _, err := sealer.Check(ws, a, []string{"TOKEN", "MISSING"}); err == nil {
		t.Fatal("a missing secret passed")
	}
	// Deleting removes the grants too.
	if err := s.Secrets.Delete(secret.NameRequest{WorkspaceID: ws, Name: "TOKEN"}); err != nil {
		t.Fatal(err)
	}
	var n int
	s.DB().QueryRow(`SELECT COUNT(*) FROM secret_grants`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d grants left", n)
	}
}

func TestScrub(t *testing.T) {
	v := map[string]string{"TOKEN": "s3cr3t/value+x", "SHORT": "ab"}
	in := "a s3cr3t/value+x b czNjcjN0L3ZhbHVlK3g= c s3cr3t%2Fvalue%2Bx d czNjcjN0L3ZhbHVlK3g e ab"
	out := secret.Scrub(in, v)
	if strings.Contains(out, "s3cr3t") || strings.Contains(out, "czNjcjN0") || strings.Count(out, "[secret:TOKEN]") != 4 || !strings.HasSuffix(out, "e ab") {
		t.Fatalf("scrubbed = %q", out)
	}
}

func jsonOf(v any) (string, error) { b, err := json.Marshal(v); return string(b), err }

func eventsText(t *testing.T, s *backend.Services) string {
	var b strings.Builder
	rows, err := s.DB().Query(`SELECT kind || data FROM events`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		rows.Scan(&k)
		b.WriteString(k + "\n")
	}
	return b.String()
}

// genKey makes an ed25519 key with ssh-keygen (passphrase "" for none).
func genKey(t *testing.T, passphrase string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "k")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", passphrase, "-f", path).CombinedOutput(); err != nil {
		t.Skipf("ssh-keygen: %v %s", err, out)
	}
	b, _ := os.ReadFile(path)
	return string(b)
}
