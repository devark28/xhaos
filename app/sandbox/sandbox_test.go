package sandbox

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
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
	ok("temp files in the project's own temp folder", "mktemp && echo $TMPDIR", filepath.Join(os.TempDir(), fmt.Sprintf("xhaos-%d", os.Getuid()), "sandboxes", "project-"))
	denied("other temp files", "ls "+os.TempDir())
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

// Unix sockets are refused, so a command cannot use the SSH agent (or the
// keyring, D-Bus, Docker) even when it knows the socket's path.
func TestNoUnixSockets(t *testing.T) {
	if ABI() == 0 {
		t.Skip("no Landlock in this kernel")
	}
	if _, err := exec.LookPath("ssh-add"); err != nil {
		t.Skip("no ssh-add")
	}
	dir := t.TempDir()
	sock := filepath.Join(dir, "agent.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	reached := make(chan bool, 4)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			reached <- true
			c.Close()
		}
	}()
	root := filepath.Join(dir, "p")
	os.MkdirAll(root, 0o755)
	c := Config{Mode: Auto, Cache: filepath.Join(dir, "c")}
	// Unsandboxed, the same command reaches the socket (the test is real).
	out, _ := run(t, Config{Mode: Off}, root, "SSH_AUTH_SOCK="+sock+" ssh-add -l")
	select {
	case <-reached:
	case <-time.After(2 * time.Second):
		t.Fatalf("control: the unsandboxed command never reached the socket: %q", out)
	}
	out, err = run(t, c, root, "SSH_AUTH_SOCK="+sock+" ssh-add -l")
	if err == nil {
		t.Fatalf("ssh-add succeeded in the sandbox: %q", out)
	}
	select {
	case <-reached:
		t.Fatalf("the sandboxed command reached the agent socket: %q", out)
	case <-time.After(300 * time.Millisecond):
	}
	if out, err := run(t, c, root, "echo piped | cat && python3 -c 'import socket; a, b = socket.socketpair(); a.send(b\"ok\"); print(b.recv(2).decode())'"); err != nil || !strings.Contains(out, "piped") || !strings.Contains(out, "ok") {
		t.Fatalf("pipes and socketpair = %q %v", out, err)
	}
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

// A submodule's (and a worktree's) .git is a file pointing into the parent
// repository: git must work in the sandbox, the parent's files must not.
func TestGitSubmodulesAndWorktrees(t *testing.T) {
	if ABI() == 0 {
		t.Skip("no Landlock in this kernel")
	}
	dir := t.TempDir()
	git := func(in string, args ...string) {
		t.Helper()
		c := exec.Command("git", append([]string{"-c", "user.email=t@t", "-c", "user.name=t", "-c", "protocol.file.allow=always"}, args...)...)
		c.Dir = in
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	lib, parent := filepath.Join(dir, "lib"), filepath.Join(dir, "parent")
	for _, d := range []string{lib, parent} {
		os.MkdirAll(d, 0o755)
		git(d, "init", "-q", "-b", "main")
		os.WriteFile(filepath.Join(d, "README"), []byte(d), 0o644)
		git(d, "add", ".")
		git(d, "commit", "-qm", "init")
	}
	os.WriteFile(filepath.Join(parent, "parent-secret.txt"), []byte("TOP SECRET"), 0o644)
	git(parent, "submodule", "add", "-q", lib, "sub")
	git(parent, "worktree", "add", "-q", filepath.Join(dir, "wt"), "-b", "feature")
	c := Config{Mode: Auto, Cache: filepath.Join(dir, "cache")}
	for _, root := range []string{filepath.Join(parent, "sub"), filepath.Join(dir, "wt")} {
		if fi, _ := os.Stat(filepath.Join(root, ".git")); fi == nil || fi.IsDir() {
			t.Fatalf("%s: .git should be a file", root)
		}
		out, err := run(t, c, root, "git status --short && echo x > new.txt && git add new.txt && git -c user.email=t@t -c user.name=t commit -qm sandboxed && git log --oneline -1")
		if err != nil || !strings.Contains(out, "sandboxed") {
			t.Errorf("%s: git in the sandbox = %v %q", root, err, out)
		}
		if out, err := run(t, c, root, "cat "+filepath.Join(parent, "parent-secret.txt")); err == nil || strings.Contains(out, "TOP SECRET") {
			t.Errorf("%s: read the parent repository's files: %q", root, out)
		}
	}
	if dirs := GitDirs(filepath.Join(dir, "wt")); len(dirs) != 2 || dirs[1] != filepath.Join(parent, ".git") {
		t.Errorf("worktree git dirs = %v", dirs)
	}
}

// Toolchain settings reach commands (a system-wide rustup needs RUSTUP_HOME);
// the rest of the server's environment does not, and HOME is never the real one.
func TestToolchainSettingsPassThrough(t *testing.T) {
	t.Setenv("RUSTUP_HOME", "/opt/rust/rustup")
	t.Setenv("GOROOT", "/usr/local/go")
	t.Setenv("XHAOS_TEST_SECRET", "nope")
	c := Config{Cache: t.TempDir()}
	env, _, err := c.prepare(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	has := func(kv string) bool { return slices.Contains(env, kv) }
	if !has("RUSTUP_HOME=/opt/rust/rustup") || !has("GOROOT=/usr/local/go") {
		t.Errorf("toolchain settings missing: %v", env)
	}
	for _, kv := range env {
		if strings.HasPrefix(kv, "XHAOS_TEST_SECRET=") || kv == "HOME="+os.Getenv("HOME") {
			t.Errorf("leaked %s", kv)
		}
	}
}
