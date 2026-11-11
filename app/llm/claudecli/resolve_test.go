package claudecli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devark28/xhaos/app/sandbox"
)

// A project registered at the server user's home folder makes ~/.local/bin
// writable by that project's sandboxed commands. A `claude` planted there
// must not be run by the server, whether found in the install folders or
// named explicitly by the owner.
func TestResolveRefusesCommandWritableProgram(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	os.MkdirAll(bin, 0o755)
	planted := filepath.Join(bin, "claude")
	os.WriteFile(planted, []byte("#!/bin/sh\necho PWNED\n"), 0o755)
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("HOME", home)
	// What the server does at start with its registered projects.
	sandbox.GuardPath(home)

	env := []string{"HOME=" + home}
	if p, err := resolve("", env); err == nil {
		t.Errorf("resolve found the planted %s in a command-writable folder", p)
	} else if !strings.Contains(err.Error(), "refusing") {
		t.Errorf("error does not say why: %v", err)
	}
	if p, err := resolve(planted, env); err == nil {
		t.Errorf("an explicit command-writable path was used: %s", p)
	}
	// A link outside the folder that points into it is refused too.
	link := filepath.Join(t.TempDir(), "claude")
	os.Symlink(planted, link)
	if p, err := resolve(link, env); err == nil {
		t.Errorf("a link into a command-writable folder was used: %s", p)
	}
	// A program elsewhere still resolves.
	safe := filepath.Join(t.TempDir(), "claude")
	os.WriteFile(safe, []byte("#!/bin/sh\n"), 0o755)
	if p, err := resolve(safe, env); err != nil || p != safe {
		t.Errorf("resolve(%s) = %q, %v", safe, p, err)
	}
}
