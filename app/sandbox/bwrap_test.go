package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// mounts pairs bwrap's mount options with their paths, in order.
func mounts(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--ro-bind", "--ro-bind-try", "--bind", "--bind-try", "--dev-bind", "--dev-bind-try", "--symlink":
			out = append(out, args[i]+" "+args[i+2])
			i += 2
		case "--tmpfs", "--proc", "--dev":
			out = append(out, args[i]+" "+args[i+1])
			i++
		}
	}
	return out
}

func TestBwrapArgs(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "work")
	inner := filepath.Join(root, "bin")
	os.MkdirAll(inner, 0o755)
	link := filepath.Join(dir, "bin")
	os.Symlink("usr/bin", link)
	p := policy{
		Read:    []string{"/usr", link, "/proc", "/etc", filepath.Join(dir, "missing"), inner},
		Files:   []string{"/etc/hostname"},
		Write:   []string{root, "/dev/shm"},
		Devices: append(slices.Clone(commonDevices), "/dev/video0"),
		Bwrap:   true,
	}
	args := bwrapArgs(p, "/usr/local/bin/xhaos-node")
	got := mounts(args)
	want := []string{
		"--tmpfs /tmp", "--proc /proc", "--dev /dev",
		"--ro-bind /usr", "--symlink " + link, "--ro-bind /etc",
		"--ro-bind-try /etc/hostname", "--ro-bind-try /usr/local/bin/xhaos-node",
		"--bind-try " + root,
		"--dev-bind-try /dev/video0",
	}
	if !slices.Equal(got, want) {
		t.Errorf("mounts =\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	if i := slices.Index(args, "--symlink"); i < 0 || args[i+1] != "usr/bin" {
		t.Errorf("the link keeps its target: %v", args)
	}
	for _, ns := range []string{"--unshare-user", "--unshare-pid", "--unshare-ipc"} {
		if !slices.Contains(args, ns) {
			t.Errorf("missing %s", ns)
		}
	}
	if slices.Contains(args, "--unshare-net") || slices.Contains(args, "--unshare-all") {
		t.Error("the network is shared, as with Landlock")
	}
	// The server's setting (devices open): the real /dev, nothing else for it.
	open := bwrapArgs(policy{Read: []string{"/usr"}, Write: []string{root, "/dev"}}, "/x")
	if got := mounts(open); !slices.Contains(got, "--dev-bind /dev") || slices.Contains(got, "--dev /dev") {
		t.Errorf("open devices = %v", got)
	}
}

func TestPickLandlockOnly(t *testing.T) {
	c := Pick(Landlock)
	if ABI() > 0 && (c.Kind != Landlock || c.Note != fmt.Sprintf("Landlock ABI %d", ABI())) {
		t.Errorf("with Landlock = %+v", c)
	}
	if ABI() == 0 && (c.Kind != "" || !strings.Contains(c.Note, "no Landlock")) {
		t.Errorf("without Landlock = %+v", c)
	}
}

func TestPickBwrapMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	c := Pick(Bwrap)
	if c.Kind != "" || !strings.Contains(c.Note, "bwrap is not installed") {
		t.Errorf("no bwrap = %+v", c)
	}
	if _, err := (Config{Mode: On, Cache: t.TempDir(), Enforcer: Bwrap}).Command(t.Context(), t.TempDir(), "true"); err == nil || !strings.Contains(err.Error(), "bwrap is gone") {
		t.Errorf("a bwrap command without bwrap = %v", err)
	}
}

// The real thing, where bubblewrap works for this user (a Raspberry Pi OS
// node; not Ubuntu 24.04 with its AppArmor user namespace restriction).
func TestBwrapCommandsSeeOnlyTheirProject(t *testing.T) {
	if err := ProbeBwrap(); err != nil {
		t.Skipf("bubblewrap: %v", err)
	}
	dir := t.TempDir()
	root, other := filepath.Join(dir, "project"), filepath.Join(dir, "other")
	os.MkdirAll(root, 0o755)
	os.MkdirAll(other, 0o755)
	os.WriteFile(filepath.Join(other, "secret.txt"), []byte("TOP SECRET"), 0o600)
	c := Config{Mode: On, Cache: filepath.Join(dir, "cache"), LimitDevices: true, Devices: []string{"/dev/full"}, Enforcer: Bwrap}
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
	ok("write and read in the project", "echo hello > notes.txt && cat notes.txt && pwd", root)
	ok("read system files", "head -c 0 /etc/hostname && ls /usr/bin | head -1 && echo fine", "fine")
	ok("run tools from PATH", "git --version", "git version")
	ok("a writable home of its own", "touch ~/x && echo $HOME", filepath.Join(dir, "cache", "home"))
	ok("$TMPDIR is kept between commands", "echo build log > $TMPDIR/build.log", "")
	ok("… and read by the next one", "cat $TMPDIR/build.log", "build log")
	ok("devices", "echo x > /dev/null && head -c 1 /dev/full | wc -c", "1")
	ok("the sandbox marker", "echo $XHAOS_SANDBOX", "1")
	denied("reading another folder", "cat "+filepath.Join(other, "secret.txt"))
	denied("writing outside the project", "echo x > "+filepath.Join(other, "planted.txt"))
	denied("writing system files", "touch /usr/xhaos-planted")
	denied("creating a device file", "touch /dev/xhaos-planted")
	if home, err := os.UserHomeDir(); err == nil {
		denied("the real home folder", "ls -a "+filepath.Join(home, ".ssh")+" "+home+"/.bashrc")
	}
	pid := os.Getpid()
	denied("reading the server's environment", fmt.Sprintf("cat /proc/%d/environ", pid))
	denied("signalling the server", fmt.Sprintf("kill -0 %d", pid))
	denied("a Unix socket", "python3 -c 'import socket; socket.socket(socket.AF_UNIX)'")
	host, err := os.CreateTemp("", "xhaos-host-file-")
	if err != nil {
		t.Fatal(err)
	}
	host.Close()
	defer os.Remove(host.Name())
	if out, err := run(t, c, root, "ls -a /tmp"); err != nil || strings.Contains(out, "xhaos-host-file-") {
		t.Errorf("/tmp is private: %v %q", err, out)
	}
	if _, _, err := (Config{Mode: On, Cache: dir, Enforcer: Bwrap}).CommandSealed(t.Context(), root, "true", Sealed{ProxyPort: 1}); err == nil {
		t.Error("a sealed command ran under bubblewrap")
	}
}

// The plumbing, with a stand-in bwrap that skips the mounts and runs what
// follows "--": the helper starts in bwrap mode, still refuses Unix
// sockets, keeps the folder and the environment, and Pick takes it.
func TestBwrapPlumbing(t *testing.T) {
	bin := t.TempDir()
	log := filepath.Join(bin, "args")
	fake := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + log + "\nwhile [ \"$1\" != -- ]; do shift; done; shift; exec \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "bwrap"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if c := Pick(Bwrap); c.Kind != Bwrap {
		t.Fatalf("Pick(bwrap) = %+v", c)
	}
	dir := t.TempDir()
	root := filepath.Join(dir, "p")
	os.MkdirAll(filepath.Join(root, "sub"), 0o755)
	c := Config{Mode: On, Cache: filepath.Join(dir, "c"), LimitDevices: true, Enforcer: Bwrap}
	sub := filepath.Join(root, "sub")
	cmd, err := c.Command(t.Context(), root, "pwd && echo $XHAOS_SANDBOX && python3 -c 'import socket; socket.socket(socket.AF_UNIX)' 2>&1 | tail -1", Extra{Dir: sub})
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), filepath.Join(root, "sub")+"\n1\n") || !strings.Contains(string(out), "Permission denied") {
		t.Errorf("in bwrap mode = %v %q", err, out)
	}
	raw, _ := os.ReadFile(log)
	if args := string(raw); !strings.Contains(args, "--chdir\n"+sub+"\n--\n") || !strings.Contains(args, "--unshare-pid\n") || !strings.Contains(args, "--bind-try\n"+root+"\n") || !strings.Contains(args, `"bwrap":true`) {
		t.Errorf("bwrap got %q", args)
	}
}
