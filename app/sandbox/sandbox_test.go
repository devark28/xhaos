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
	// Agents are told $TMPDIR is kept between commands: a log written by one
	// command is there for the next, outside the project.
	ok("$TMPDIR is kept between commands", "echo build log > $TMPDIR/build.log", "")
	ok("… and read by the next one", "cat $TMPDIR/build.log && test ! -e build.log && echo outside", "build log\noutside")
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

// Commands the classifier allows run with no TCP connections; the same
// command connects without that flag.
func TestNoNetBlocksConnections(t *testing.T) {
	if ABI() < NetABI {
		t.Skip("needs Landlock ABI 4 (Linux 6.7+)")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	dir := t.TempDir()
	c := Config{Mode: On, Cache: filepath.Join(dir, "c")}
	script := fmt.Sprintf("exec 3<>/dev/tcp/127.0.0.1/%d", ln.Addr().(*net.TCPAddr).Port)
	if out, err := run(t, c, dir, script); err != nil {
		t.Fatalf("without NoNet the connection failed: %q %v", out, err)
	}
	cmd, err := c.Command(context.Background(), dir, script, Extra{NoNet: true})
	if err != nil {
		t.Fatal(err)
	}
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("with NoNet the connection worked: %q", out)
	}
	if s := c.Status(); !s.NoNet {
		t.Errorf("status without NoNet: %+v", s)
	}
}

// The no-network rule needs the sandbox on and Landlock ABI 4: with the
// sandbox off (or unset) it is never possible, and the status says so.
func TestNoNetPossibleNeedsSandboxAndABI(t *testing.T) {
	for _, m := range []string{Off, ""} {
		if (Config{Mode: m}).NoNetPossible() || (Config{Mode: m}).Status().NoNet {
			t.Errorf("mode %q: no-network possible without the sandbox", m)
		}
	}
	on := Config{Mode: Auto}
	if on.NoNetPossible() != (ABI() >= NetABI) || on.Status().NoNet != on.NoNetPossible() {
		t.Errorf("auto: NoNetPossible %v, status %v; want the kernel's ABI %d to decide", on.NoNetPossible(), on.Status().NoNet, ABI())
	}
	// Under bubblewrap the helper applies no Landlock rules, so TCP stays
	// open: no-network must not be claimed there, whatever the kernel.
	bw := Config{Mode: Auto, Enforcer: Bwrap}
	if bw.NoNetPossible() || bw.Status().NoNet {
		t.Errorf("bubblewrap: no-network claimed (NoNetPossible %v, status %v); it cannot restrict TCP", bw.NoNetPossible(), bw.Status().NoNet)
	}
}

// Commands the classifier allows also get no UDP: a datagram to a local
// listener (a DNS-style leak) must not arrive. The same command without
// NoNet still sends it, so the listener works.
func TestNoNetBlocksUDP(t *testing.T) {
	if ABI() < NetABI {
		t.Skip("needs Landlock ABI 4 (Linux 6.7+)")
	}
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	port := pc.LocalAddr().(*net.UDPAddr).Port
	dir := t.TempDir()
	c := Config{Mode: On, Cache: filepath.Join(dir, "c")}
	script := fmt.Sprintf("echo leak > /dev/udp/127.0.0.1/%d", port)
	// received runs cmd, then reports whether a datagram arrived. Anything
	// left from an earlier run is drained first, so it cannot count.
	received := func(cmd *exec.Cmd) bool {
		buf := make([]byte, 64)
		// A deadline in the past fails before reading queued data, so drain
		// with a short future one.
		_ = pc.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
		for {
			if _, _, err := pc.ReadFrom(buf); err != nil {
				break
			}
		}
		_, _ = cmd.CombinedOutput()
		_ = pc.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, _, err := pc.ReadFrom(buf)
		return err == nil && n > 0
	}
	plain, err := c.Command(context.Background(), dir, script, Extra{})
	if err != nil {
		t.Fatal(err)
	}
	if !received(plain) {
		t.Skip("the UDP listener got nothing even without NoNet: the test cannot tell")
	}
	cmd, err := c.Command(context.Background(), dir, script, Extra{NoNet: true})
	if err != nil {
		t.Fatal(err)
	}
	if received(cmd) {
		t.Fatal("with NoNet a UDP datagram reached a local listener")
	}
}

// Landlock's TCP rules do not cover other protocols on an IPv4 socket, so
// SCTP (132) and MPTCP (262) connected out of a NoNet command and carried
// data. The filter must refuse their socket() call (TCP still opens: it
// only fails at connect, where Landlock applies).
func TestNoNetBlocksOtherProtocols(t *testing.T) {
	if ABI() < NetABI {
		t.Skip("needs Landlock ABI 4 (Linux 6.7+)")
	}
	probe := func(family string, proto int) string {
		return fmt.Sprintf("python3 -c \"\nimport socket\ntry:\n    socket.socket(socket.%s, socket.SOCK_STREAM, %d)\n    print('opened')\nexcept PermissionError:\n    print('denied')\n\"", family, proto)
	}
	dir := t.TempDir()
	c := Config{Mode: On, Cache: filepath.Join(dir, "c")}
	ran := func(cmd *exec.Cmd) string {
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Logf("probe: %v: %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	for _, family := range []string{"AF_INET", "AF_INET6"} {
		for _, proto := range []int{132, 262} {
			plain, err := c.Command(context.Background(), dir, probe(family, proto), Extra{})
			if err != nil {
				t.Fatal(err)
			}
			if ran(plain) != "opened" {
				t.Skipf("%s protocol %d cannot be opened even without NoNet: the test cannot tell", family, proto)
			}
			cmd, err := c.Command(context.Background(), dir, probe(family, proto), Extra{NoNet: true})
			if err != nil {
				t.Fatal(err)
			}
			if got := ran(cmd); got != "denied" {
				t.Errorf("NoNet: %s protocol %d socket %q, want denied", family, proto, got)
			}
			// The proxy-only path (secrets) must refuse the same socket.
			sealed, cleanup, err := c.CommandSealed(context.Background(), dir, probe(family, proto), Sealed{ProxyPort: 1})
			if err != nil {
				t.Fatal(err)
			}
			got := ran(sealed)
			cleanup()
			if got != "denied" {
				t.Errorf("sealed: %s protocol %d socket %q, want denied", family, proto, got)
			}
		}
	}
	// TCP keeps working: NoNet only blocks connects, and the test must not
	// have passed by breaking every socket.
	tcp, err := c.Command(context.Background(), dir, probe("AF_INET", 6), Extra{NoNet: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := ran(tcp); got != "opened" {
		t.Errorf("NoNet: TCP socket %q, want opened", got)
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

// With LimitDevices (xhaos-node), only the harmless devices and the ones
// named in Devices can be opened; /dev can still be listed.
func TestLimitDevices(t *testing.T) {
	if ABI() == 0 {
		t.Skip("no Landlock in this kernel")
	}
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	os.MkdirAll(root, 0o755)
	c := Config{Mode: On, Cache: filepath.Join(dir, "cache"), LimitDevices: true}
	for _, command := range []string{
		"echo x > /dev/null && head -c 4 /dev/urandom | wc -c",
		"echo to-stderr >&2 && echo to-stderr2 > /dev/stderr",
		// Programs other than bash open /dev/stderr through /proc/self/fd.
		"python3 -c \"open('/dev/stderr','w').write('x')\" 2>&1 | cat",
		"ls /dev >/dev/null && echo listed",
		"echo shm > /dev/shm/xhaos-test-$$ 2>/dev/null; rm -f /dev/shm/xhaos-test-$$; echo ok",
	} {
		if out, err := run(t, c, root, command); err != nil {
			t.Errorf("%q: %v %q", command, err, out)
		}
	}
	// /dev/full is not among the common devices: closed until named.
	if out, err := run(t, c, root, "head -c 1 /dev/full"); err == nil {
		t.Errorf("opened a device that was not named: %q", out)
	}
	named := Config{Mode: On, Cache: filepath.Join(dir, "cache"), LimitDevices: true, Devices: []string{"/dev/full"}}
	if out, err := run(t, named, root, "head -c 1 /dev/full | wc -c"); err != nil || strings.TrimSpace(out) != "1" {
		t.Errorf("named device: %v %q", err, out)
	}
	// /dev itself is not writable.
	if out, err := run(t, c, root, "touch /dev/xhaos-planted"); err == nil {
		t.Errorf("created a file in /dev: %q", out)
	}
	// The server's own setting keeps /dev open.
	open := Config{Mode: On, Cache: filepath.Join(dir, "cache")}
	if out, err := run(t, open, root, "cat /dev/null && echo ok"); err != nil {
		t.Errorf("open devices: %v %q", err, out)
	}
}

// Without an owner's ~/.gitconfig, git must not read a global config from the
// sandbox's home: a command could write one there, and every later git in
// every project would run it. GIT_CONFIG_GLOBAL points at /dev/null then.
func TestSandboxGitGlobalConfigPinned(t *testing.T) {
	owner := t.TempDir()
	t.Setenv("HOME", owner)
	c := Config{Cache: t.TempDir()}
	env, _, err := c.prepare(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(env, "GIT_CONFIG_GLOBAL=/dev/null") {
		t.Errorf("no owner ~/.gitconfig: want GIT_CONFIG_GLOBAL=/dev/null, env has %v", envWith(env, "GIT_CONFIG_"))
	}
	// With one, the owner's file is the global config, as before.
	os.WriteFile(filepath.Join(owner, ".gitconfig"), []byte("[user]\n\tname = x\n"), 0o644)
	env, _, err = c.prepare(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(env, "GIT_CONFIG_GLOBAL="+filepath.Join(owner, ".gitconfig")) {
		t.Errorf("owner ~/.gitconfig: want it as GIT_CONFIG_GLOBAL, env has %v", envWith(env, "GIT_CONFIG_"))
	}
}

// A command plants a config in its home, and a git command in the same
// command reads it: the planted fsmonitor hook must not run.
func TestSandboxPlantedHomeConfigDoesNotRun(t *testing.T) {
	if ABI() == 0 {
		t.Skip("no Landlock in this kernel")
	}
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	os.MkdirAll(root, 0o755)
	c := Config{Mode: On, Cache: filepath.Join(dir, "cache")}
	command := `git init -q . && printf '[core]\n\tfsmonitor = touch PLANTED\n' > "$HOME/.gitconfig" && git status >/dev/null 2>&1 && echo GIT-RAN`
	out, err := run(t, c, root, command)
	if err != nil || !strings.Contains(out, "GIT-RAN") {
		t.Fatalf("git did not run under the pin: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(root, "PLANTED")); err == nil {
		t.Error("a config planted in the sandbox home ran git's fsmonitor hook")
	}
}

// envWith lists the variables in env that start with prefix (for messages).
func envWith(env []string, prefix string) []string {
	var out []string
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			out = append(out, kv)
		}
	}
	return out
}
