//go:build server

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/devark28/xhaos/app/backend"
)

// An older installer put the commands' Flutter ($DATA/sandbox/flutter/bin)
// on the server's PATH. At start the server drops it, so a program a
// command planted there is never run by the server.
func TestStartDropsCommandFoldersFromPath(t *testing.T) {
	data := t.TempDir()
	bin := filepath.Join(data, "sandbox", "flutter", "bin")
	os.MkdirAll(bin, 0o755)
	os.WriteFile(filepath.Join(bin, "xhaos-planted-claude"), []byte("#!/bin/sh\necho PWNED\n"), 0o755)
	t.Setenv("PATH", os.Getenv("PATH")+string(os.PathListSeparator)+bin)
	s, err := backend.Open(filepath.Join(data, "x.db"), backend.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if p, err := exec.LookPath("xhaos-planted-claude"); err == nil {
		t.Errorf("the server still finds a command-planted program on its PATH: %s", p)
	}
}

// The installer's service file must not put the sandbox cache on the
// server's PATH.
func TestInstallerKeepsSandboxCacheOffServerPath(t *testing.T) {
	raw, err := os.ReadFile("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	if m := regexp.MustCompile(`(?m)^[^#\n]*path="[^"\n]*\$DATA[^"\n]*"`).Find(raw); m != nil {
		t.Errorf("install.sh puts a data folder on the server's PATH: %s", m)
	}
}

// The installer points the server at its classifier and dictation services
// on Unix sockets: on a loopback port, a sandboxed command could listen
// while a service is down and answer in its place.
func TestInstallerReachesServicesOnSockets(t *testing.T) {
	raw, err := os.ReadFile("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"-classifier-url", "-dictation-url"} {
		m := regexp.MustCompile(`flags="\$flags ` + flag + ` (\S+)"`).FindSubmatch(raw)
		if m == nil {
			t.Errorf("install.sh sets no %s", flag)
			continue
		}
		if !regexp.MustCompile(`^unix:`).Match(m[1]) {
			t.Errorf("install.sh gives %s %s, not a Unix socket", flag, m[1])
		}
	}
}
