package secret_test

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"xhaos/app/agent"
	"xhaos/app/backend"
	"xhaos/app/secret"
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
	for _, bad := range []string{"http://github.com", "github.com/path", "*", "localhost", "10.0.0.1"} {
		if _, err := s.Secrets.Save(secret.SaveRequest{WorkspaceID: ws, Name: "X", Value: "x", Hosts: []string{bad}}); err == nil {
			t.Errorf("accepted host %q", bad)
		}
	}
}

func TestGrantsAndPassphrase(t *testing.T) {
	s := open(t)
	s.Secrets.Verify = func(p string) error {
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
