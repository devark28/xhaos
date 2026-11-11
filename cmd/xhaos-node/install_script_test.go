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

// The sandbox check runs the way the service does: systemd-run with the
// unit's user, environment and confinement, the command after them.
func TestInstallScriptChecksTheSandboxAsTheService(t *testing.T) {
	bin := t.TempDir()
	stub := "#!/bin/sh\nfor a in \"$@\"; do printf '[%s]\\n' \"$a\"; done\n"
	if err := os.WriteFile(filepath.Join(bin, "systemd-run"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	script, _ := filepath.Abs("../../install-node.sh")
	sh := `set -eu
XHAOS_INSTALL_NODE_FUNCTIONS=1 . "$1"
ROOT=/srv/work SYSTEMD=yes
as_service /usr/local/bin/xhaos-node sandbox-check --sandbox "a b"
echo ---
SYSTEMD=no
as_node() { echo "as_node $*"; }
as_service /usr/local/bin/xhaos-node sandbox-check
`
	cmd := exec.Command("sh", "-c", sh, "sh", script)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v %s", err, b)
	}
	run, plain, _ := strings.Cut(string(b), "---\n")
	want := "[--wait]\n[--pipe]\n[--quiet]\n[--collect]\n[-p]\n[User=xhaos-node]\n[-E]\n[HOME=/var/lib/xhaos-node]\n[-E]\n[XHAOS_NODE_STATE=/var/lib/xhaos-node]\n" +
		"[-p]\n[NoNewPrivileges=yes]\n[-p]\n[ProtectSystem=strict]\n[-p]\n[ProtectHome=read-only]\n[-p]\n[ReadWritePaths=/var/lib/xhaos-node /srv/work]\n[-p]\n[PrivateTmp=yes]\n" +
		"[--]\n[/usr/local/bin/xhaos-node]\n[sandbox-check]\n[--sandbox]\n[a b]\n"
	if run != want {
		t.Errorf("systemd-run got\n%swant\n%s", run, want)
	}
	if plain != "as_node /usr/local/bin/xhaos-node sandbox-check\n" {
		t.Errorf("without systemd: %q", plain)
	}
	// The unit holds the same confinement.
	raw, _ := os.ReadFile(script)
	if !strings.Contains(string(raw), "Environment=HOME=$STATE XHAOS_NODE_STATE=$STATE\nExecStart=$BIN run\n") || !strings.Contains(string(raw), "RestartPreventExitStatus=3\n$(confinement)\n\n[Install]") {
		t.Error("the unit no longer takes its confinement from confinement()")
	}
}

// --no-join keeps --root, --device and --unsandboxed in join.conf; a later
// run (the join) reads them back, its own flags winning, and refuses a file
// that is not root's or has a device outside /dev.
func TestInstallScriptJoinConf(t *testing.T) {
	conf := filepath.Join(t.TempDir(), "etc", "join.conf")
	run := func(call string) (string, error) {
		out, _, err := installScript(t, t.TempDir(), `JOIN_CONF=$3 ROOT_UID=$(id -u) DEVICES_GIVEN=no
`+call, conf)
		return out, err
	}
	if out, err := run(`ROOT=/srv/work DEVICES=" /dev/video0 /dev/ttyUSB0" UNSANDBOXED=yes; save_join_conf`); err != nil {
		t.Fatalf("save: %v %s", err, out)
	}
	if err := os.Chmod(conf, 0o644); err != nil { // the script's chmod is recorded, not run
		t.Fatal(err)
	}
	show := `; echo "root=$ROOT devices=$DEVICES unsandboxed=$UNSANDBOXED"`
	for _, c := range []struct{ call, want string }{
		{`ROOT="" DEVICES="" UNSANDBOXED=""; load_join_conf` + show, "root=/srv/work devices= /dev/video0 /dev/ttyUSB0 unsandboxed=yes"},
		{`ROOT=/other DEVICES=" /dev/video2" DEVICES_GIVEN=yes UNSANDBOXED=no; load_join_conf` + show, "root=/other devices= /dev/video2 unsandboxed=no"},
	} {
		if out, err := run(c.call); err != nil || strings.TrimSpace(out) != c.want {
			t.Errorf("%s:\n got %q %v\nwant %q", c.call, out, err, c.want)
		}
	}
	os.WriteFile(conf, []byte("device=/etc/shadow\n"), 0o644)
	if out, err := run(`load_join_conf`); err == nil {
		t.Errorf("a device outside /dev was read: %s", out)
	}
	os.WriteFile(conf, []byte("server=https://evil.example\n"), 0o644)
	if out, err := run(`load_join_conf`); err == nil {
		t.Errorf("an unknown line was read: %s", out)
	}
	os.WriteFile(conf, []byte("root=/srv/work\n"), 0o644)
	os.Chmod(conf, 0o664)
	if out, err := run(`load_join_conf`); err == nil || !strings.Contains(out, "writable by others") {
		t.Errorf("a group-writable join.conf was read: %v %s", err, out)
	}
	os.Chmod(conf, 0o644)
	if out, err := run(`ROOT_UID=$(($(id -u) + 1)); load_join_conf`); err == nil || !strings.Contains(out, "not root's") {
		t.Errorf("another user's join.conf was read: %v %s", err, out)
	}
}
