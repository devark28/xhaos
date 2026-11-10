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
	"slices"
	"strings"
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
	args := append(slices.Clone(bwrapNamespaces), "--ro-bind", "/", "/", "--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp", "--", "true")
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
func bwrapArgs(p policy, self string) []string {
	args := slices.Clone(bwrapNamespaces)
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
	for _, r := range p.Read {
		if r == "/proc" || inside(r, []string{"/proc", "/dev"}) || inside(r, write) {
			continue
		}
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
		if f != "" && !inside(f, write) {
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
	args := append(bwrapArgs(p, self), "--chdir", dir, "--", self, helperArg, string(raw), command)
	return exec.CommandContext(ctx, path, args...), nil
}
