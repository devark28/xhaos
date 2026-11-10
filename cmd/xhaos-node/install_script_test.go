package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// installScript runs install-node.sh's folder functions (sourced, as root
// would run them) with chown and chmod recorded instead of run, so the test
// needs no root: what matters is which paths root would touch.
func installScript(t *testing.T, state, call string, args ...string) (out, touched string, err error) {
	t.Helper()
	script, _ := filepath.Abs("../../install-node.sh")
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "touched")
	for _, name := range []string{"chown", "chmod"} {
		stub := "#!/bin/sh\necho \"" + name + " $*\" >>" + log + "\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(stub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sh := `set -eu
XHAOS_INSTALL_NODE_FUNCTIONS=1 . "$1"
STATE=$2 USER_NAME=$(id -un)
as_node() { "$@"; }
` + call
	cmd := exec.Command("sh", append([]string{"-c", sh, "sh", script, state}, args...)...)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	b, err := cmd.CombinedOutput()
	l, _ := os.ReadFile(log)
	return string(b), string(l), err
}

func TestInstallScriptRefusesSymlinkedRoot(t *testing.T) {
	state := t.TempDir()
	target := t.TempDir() // stands for /etc/systemd/system
	if err := os.Symlink(target, filepath.Join(state, "xhaos-work")); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(target)
	out, touched, err := installScript(t, state, `prepare_root "$STATE/xhaos-work"`)
	if err == nil || !strings.Contains(out, "is a symlink") {
		t.Fatalf("a symlinked xhaos-work was accepted: %v %s", err, out)
	}
	if touched != "" {
		t.Errorf("root touched: %s", touched)
	}
	after, _ := os.Stat(target)
	if after.Mode() != before.Mode() || !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("the link's target changed: %v → %v", before.Mode(), after.Mode())
	}
}

func TestInstallScriptRoots(t *testing.T) {
	state := t.TempDir()
	outside := filepath.Join(t.TempDir(), "srv-work")
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		root    string
		ok      bool
		chowned bool // root hands the folder over (only outside the state folder)
	}{
		{state + "/xhaos-work", true, false},                   // made by the node's user
		{state + "/xhaos-work", true, false},                   // exists now: left alone
		{state + "/a/b", true, false},                          // nested, still the node's user
		{outside, true, true},                                  // the owner's --root
		{outside, true, true},                                  // again: still a real folder
		{link, false, false},                                   // a symlink
		{"relative/work", false, false},                        //
		{state + "/../etc", false, false},                      // .. parts
		{state + "/xhaos-work/", false, false},                 // a trailing / would follow a link
		{state, false, false},                                  // the key's own folder
		{state + "/work\nExecStartPre=+/bin/sh", false, false}, // unit file injection
		{state + "/my work", false, false},                     // ReadWritePaths splits on spaces
	} {
		out, touched, err := installScript(t, state, `prepare_root "$3"`, c.root)
		if (err == nil) != c.ok {
			t.Errorf("%q: ok = %v, want %v: %s", c.root, err == nil, c.ok, out)
			continue
		}
		if wantChown := c.chowned; strings.Contains(touched, "chown -h") != wantChown || strings.Contains(touched, "chmod") {
			t.Errorf("%q: root touched %q", c.root, touched)
		}
		if c.ok {
			if fi, err := os.Lstat(c.root); err != nil || !fi.IsDir() {
				t.Errorf("%q: not a folder: %v", c.root, err)
			}
		}
	}
}

func TestInstallScriptRefusesSymlinkedState(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	if err := os.Symlink(t.TempDir(), state); err != nil {
		t.Fatal(err)
	}
	out, touched, err := installScript(t, state, `prepare_state`)
	if err == nil || !strings.Contains(out, "is a symlink") || touched != "" {
		t.Fatalf("a symlinked state folder: %v %q %s", err, touched, out)
	}
}
