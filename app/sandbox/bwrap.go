package sandbox

// Bubblewrap is the second way to enforce a command's policy, for kernels
// without Landlock (Raspberry Pi OS's kernels have none). xhaos-node uses it;
// the server does not. bwrap builds a new mount namespace that holds only
// the policy's folders (read-only or writable as the policy says), a private
// /tmp, /dev with the harmless devices plus the granted ones, and a /proc of
// its own pid namespace, so a command cannot see or signal processes outside
// it. The helper inside then installs the same seccomp filter as with
// Landlock (no Unix sockets) and execs bash. The network is shared, as with
// Landlock.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Enforcers: what confines commands.
const (
	Landlock = "landlock"
	Bwrap    = "bwrap"
)

// Choice is the sandbox a process picked at start (xhaos-node).
type Choice struct {
	Kind string // Landlock or Bwrap; "" when neither works here
	ABI  int    // the kernel's Landlock ABI, 0 when it has none
	Note string // what is used, or why nothing can be
}

// Pick chooses how commands are confined. want is "auto" (or ""): Landlock
// when the kernel has it, else bubblewrap when it works for this user;
// Landlock or Bwrap: only that one.
func Pick(want string) Choice {
	c := Choice{ABI: ABI()}
	if want != Bwrap && c.ABI > 0 {
		c.Kind, c.Note = Landlock, fmt.Sprintf("Landlock ABI %d", c.ABI)
		return c
	}
	var why []string
	if c.ABI == 0 {
		why = append(why, "this kernel has no Landlock")
	}
	if want == Landlock {
		c.Note = strings.Join(why, "")
		return c
	}
	if err := ProbeBwrap(); err != nil {
		c.Note = strings.Join(append(why, "bubblewrap: "+err.Error()), "; ")
		return c
	}
	c.Kind, c.Note = Bwrap, "bubblewrap"
	if c.ABI == 0 {
		c.Note = "bubblewrap (this kernel has no Landlock)"
	}
	return c
}

// bwrapNamespaces are the namespaces every sandboxed command gets: its own
// user (the same uid inside), pid and IPC namespaces. Not the network.
var bwrapNamespaces = []string{"--unshare-user", "--unshare-pid", "--unshare-ipc"}

// bwrapSession are the options every sandboxed command gets besides its
// namespaces: a new session, so it has no controlling terminal and cannot
// push input into the one xhaos-node was started from (TIOCSTI: /dev/tty is
// there and Pi kernels allow it), and it dies with the helper's parent.
var bwrapSession = []string{"--new-session", "--die-with-parent"}

// bwrapNoUserns is passed when bwrap has it (0.8 and later): the command
// cannot make user namespaces of its own (less kernel to attack).
const bwrapNoUserns = "--disable-userns"

var versionRe = regexp.MustCompile(`(\d+)\.(\d+)`)

var bwrapVersions sync.Map // bwrap's path → [2]int{major, minor}

// bwrapVersion is the major and minor version of the bwrap at path, asked
// once; 0, 0 when it does not say.
func bwrapVersion(path string) (major, minor int) {
	if v, ok := bwrapVersions.Load(path); ok {
		mm := v.([2]int)
		return mm[0], mm[1]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, path, "--version").Output()
	if m := versionRe.FindStringSubmatch(string(out)); m != nil {
		major, _ = strconv.Atoi(m[1])
		minor, _ = strconv.Atoi(m[2])
	}
	bwrapVersions.Store(path, [2]int{major, minor})
	return major, minor
}

// bwrapHasNoUserns: bwrap 0.8 or later.
func bwrapHasNoUserns(path string) bool {
	major, minor := bwrapVersion(path)
	return major > 0 || minor >= 8
}

// ProbeBwrap says why bubblewrap cannot sandbox commands for this user, or
// nil when it can.
func ProbeBwrap() error {
	path, err := exec.LookPath("bwrap")
	if err != nil {
		return errors.New("bwrap is not installed (apt install bubblewrap)")
	}
	if readTrim("/proc/sys/user/max_user_namespaces") == "0" {
		return errors.New("user namespaces are off (/proc/sys/user/max_user_namespaces is 0)")
	}
	if readTrim("/proc/sys/kernel/unprivileged_userns_clone") == "0" {
		return errors.New("unprivileged user namespaces are off (kernel.unprivileged_userns_clone is 0)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	args := append(slices.Clone(bwrapNamespaces), bwrapSession...)
	if bwrapHasNoUserns(path) {
		args = append(args, bwrapNoUserns)
	}
	args = append(args, "--ro-bind", "/", "/", "--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp", "--", "true")
	out, err := exec.CommandContext(ctx, path, args...).CombinedOutput()
	if err != nil {
		msg, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
		if msg == "" {
			msg = err.Error()
		}
		if readTrim("/proc/sys/kernel/apparmor_restrict_unprivileged_userns") == "1" {
			msg += " (AppArmor restricts unprivileged user namespaces here: kernel.apparmor_restrict_unprivileged_userns is 1)"
		}
		return fmt.Errorf("it does not run for this user: %s", msg)
	}
	return nil
}

func readTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// bwrapArgs turns a policy into bwrap's arguments (before "--"). The mount
// namespace starts empty: only what is named here exists in it. Later
// mounts cover earlier ones, so read-only folders inside a writable one are
// left out (Landlock would allow writing there too), /tmp is a fresh tmpfs
// before the folders under it are bound, and the writable folders come last.
//
// bwrap 0.4 (Debian bullseye) refuses to make a path twice ("File exists"),
// so each path is named once: repeats are dropped, and so is anything
// inside a folder already bound read-only or behind a link made here (PATH
// brings /usr/bin and /bin again).
func bwrapArgs(p policy, self string, noUserns bool) []string {
	args := append(slices.Clone(bwrapNamespaces), bwrapSession...)
	if noUserns {
		args = append(args, bwrapNoUserns)
	}
	args = append(args, "--tmpfs", "/tmp", "--proc", "/proc")
	devOpen := slices.Contains(p.Write, "/dev")
	if devOpen {
		args = append(args, "--dev-bind", "/dev", "/dev")
	} else {
		// null, zero, full, random, urandom, tty, ptmx and pts, and a private
		// shm: what commonDevices names (plus /dev/full, harmless).
		args = append(args, "--dev", "/dev")
	}
	inside := func(path string, dirs []string) bool {
		for _, d := range dirs {
			if path == d || strings.HasPrefix(path, strings.TrimSuffix(d, "/")+"/") {
				return true
			}
		}
		return false
	}
	var write []string
	for _, w := range p.Write {
		if w == "/dev" || (!devOpen && inside(w, []string{"/dev"})) {
			continue
		}
		write = append(write, w)
	}
	// The real folders read, and the links among them.
	var dirs, links []string
	for _, r := range p.Read {
		if fi, err := os.Lstat(r); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			links = append(links, r)
		} else if err == nil {
			dirs = append(dirs, r)
		}
	}
	// below: path is strictly inside one of these folders.
	below := func(path string, in []string) bool {
		for _, d := range in {
			if path != d && inside(path, []string{d}) {
				return true
			}
		}
		return false
	}
	named := map[string]bool{}
	for _, r := range p.Read {
		if named[r] || r == "/proc" || inside(r, []string{"/proc", "/dev"}) || inside(r, write) || below(r, dirs) || below(r, links) {
			continue
		}
		named[r] = true
		fi, err := os.Lstat(r)
		switch {
		case err != nil:
			continue
		case fi.Mode()&os.ModeSymlink != 0:
			// /bin → usr/bin and the like: the same link inside.
			if target, err := os.Readlink(r); err == nil {
				args = append(args, "--symlink", target, r)
			}
		default:
			args = append(args, "--ro-bind", r, r)
		}
	}
	for _, f := range append(slices.Clone(p.Files), self) {
		if f != "" && !named[f] && !inside(f, write) && !inside(f, dirs) && !below(f, links) {
			named[f] = true
			args = append(args, "--ro-bind-try", f, f)
		}
	}
	for _, w := range write {
		args = append(args, "--bind-try", w, w)
	}
	if !devOpen {
		for _, d := range p.Devices {
			if !slices.Contains(commonDevices, d) {
				args = append(args, "--dev-bind-try", d, d)
			}
		}
	}
	return args
}

// bwrapCommand runs the helper (self) inside bwrap with policy p, in dir
// (bwrap refuses to start when dir is not inside, rather than use $HOME).
func bwrapCommand(ctx context.Context, self string, p policy, raw []byte, dir, command string) (*exec.Cmd, error) {
	path, err := exec.LookPath("bwrap")
	if err != nil {
		return nil, errors.New("commands run under bubblewrap here, and bwrap is gone (apt install bubblewrap)")
	}
	args := append(bwrapArgs(p, self, bwrapHasNoUserns(path)), "--chdir", dir, "--", self, helperArg, string(raw), command)
	return exec.CommandContext(ctx, path, args...), nil
}
