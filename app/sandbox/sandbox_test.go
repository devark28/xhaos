package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The test binary doubles as the helper, like the server binary does.
func TestMain(m *testing.M) {
	MaybeRun()
	os.Exit(m.Run())
}

func run(t *testing.T, c Config, root, command string) (string, error) {
	t.Helper()
	cmd, err := c.Command(context.Background(), root, command)
	if err != nil {
		return "", err
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestCommandsSeeOnlyTheirProject(t *testing.T) {
	if ABI() == 0 {
		t.Skip("no Landlock in this kernel")
	}
	dir := t.TempDir()
	root, other := filepath.Join(dir, "project"), filepath.Join(dir, "other")
	os.MkdirAll(root, 0o755)
	os.MkdirAll(other, 0o755)
	os.WriteFile(filepath.Join(other, "secret.txt"), []byte("TOP SECRET"), 0o600)
	c := Config{Mode: Auto, Cache: filepath.Join(dir, "cache")}

	ok := func(name, command, want string) {
		t.Helper()
		out, err := run(t, c, root, command)
		if err != nil || !strings.Contains(out, want) {
			t.Errorf("%s: %q → %v %q", name, command, err, out)
		}
	}
	denied := func(name, command string) {
		t.Helper()
		out, err := run(t, c, root, command)
		if err == nil || strings.Contains(out, "TOP SECRET") {
			t.Errorf("%s was allowed: %q → %q", name, command, out)
		}
	}
	ok("write and read in the project", "echo hello > notes.txt && cat notes.txt && mkdir -p a/b && mv notes.txt a/b/ && ls a/b", "notes.txt")
	ok("read system files", "head -c 0 /etc/hostname && ls /usr/bin | head -1 && echo fine", "fine")
	ok("run tools from PATH", "git --version", "git version")
	ok("a writable home of its own", "touch ~/x && echo $HOME", filepath.Join(dir, "cache", "home"))
	ok("temp files", "mktemp && echo made", "made")
	ok("devices", "echo x > /dev/null && echo done", "done")
	denied("reading another folder", "cat "+filepath.Join(other, "secret.txt"))
	denied("listing another folder", "ls "+other)
	denied("writing outside the project", "echo x > "+filepath.Join(other, "planted.txt"))
	if _, err := os.Stat(filepath.Join(other, "planted.txt")); err == nil {
		t.Error("a file was planted outside the project")
	}
	if home, err := os.UserHomeDir(); err == nil {
		denied("listing the real home folder", "ls -a "+home)
	}
	// The server's environment and signals are out of reach.
	pid := os.Getpid()
	denied("reading the server's environment", fmt.Sprintf("cat /proc/%d/environ", pid))
	denied("signalling the server", fmt.Sprintf("kill -0 %d", pid))
}

func TestModes(t *testing.T) {
	dir := t.TempDir()
	off := Config{Mode: Off}
	out, err := run(t, off, dir, "echo $XHAOS_SANDBOX-unsandboxed")
	if err != nil || strings.TrimSpace(out) != "-unsandboxed" {
		t.Fatalf("off = %q %v", out, err)
	}
	if s := off.Status(); s.Active || !strings.Contains(s.Note, "full access") {
		t.Errorf("off status = %+v", s)
	}
	if ABI() == 0 {
		if _, err := (Config{Mode: On, Cache: dir}).Command(context.Background(), dir, "true"); err == nil {
			t.Error("on without Landlock ran a command")
		}
		return
	}
	out, err = run(t, Config{Mode: On, Cache: filepath.Join(dir, "c")}, dir, "echo $XHAOS_SANDBOX")
	if err != nil || strings.TrimSpace(out) != "1" {
		t.Fatalf("on = %q %v", out, err)
	}
	if s := (Config{Mode: Auto}).Status(); !s.Active || s.ABI == 0 {
		t.Errorf("auto status = %+v", s)
	}
}

