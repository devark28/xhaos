// Package sandbox runs agents' shell commands under Landlock, the Linux
// kernel's unprivileged access control: no root, no user namespaces (which
// Ubuntu restricts), and it works inside containers.
//
// The server starts itself again as a small helper (Command); the helper
// (MaybeRun, first thing in main) restricts itself and then execs bash. A
// Landlock restriction cannot be lifted and is inherited by every process
// the command starts.
//
// Inside, a command can:
//   - read and write its project folder, a temp folder of its own under
//     /tmp/xhaos-<uid>/sandboxes/ (TMPDIR), and a shared cache beside the
//     database (HOME, Go, npm and other tool caches; never the real home);
//   - read and run system files (/usr, /etc, …) and the toolchains on PATH;
//   - read the git identity (~/.gitconfig), but not ~/.ssh or credentials.
//
// It cannot read the rest of the home folder (other projects, Xhaos's
// database, transcripts, attachments, the key file), and cannot signal or
// connect to processes outside the sandbox through abstract sockets, nor
// open any Unix socket (the SSH agent, keyring, D-Bus, Docker; seccomp).
// Network access is unchanged for now.
//
// On kernels without Landlock, xhaos-node can enforce the same policy with
// bubblewrap instead (bwrap.go, Config.Enforcer); the server never does.
package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/landlock-lsm/go-landlock/landlock"
	llsys "github.com/landlock-lsm/go-landlock/landlock/syscall"
)

const helperArg = "__xhaos_sandbox"

// Mode: "auto" sandboxes when the kernel supports Landlock and runs
// commands unsandboxed otherwise (with a note); "on" refuses to run
// commands without it; "off" never sandboxes.
const (
	Auto = "auto"
	On   = "on"
	Off  = "off"
)

// Config is set once at startup.
type Config struct {
	Mode  string
	Cache string // writable directory shared by sandboxed commands (HOME, tool caches)
	// LimitDevices closes /dev except the harmless devices (null, zero,
	// random, tty, shm) and Devices (xhaos-node: hardware is opt-in). Off,
	// commands may use every device the user may (the server's commands).
	LimitDevices bool
	Devices      []string // device files or folders of them, e.g. /dev/video0, /dev/dri
	// Enforcer is what confines commands: Landlock (or "") or Bwrap
	// (bubblewrap; xhaos-node on kernels without Landlock, see Pick).
	Enforcer string
}

// commonDevices are open to every command, even with LimitDevices.
var commonDevices = []string{"/dev/null", "/dev/zero", "/dev/random", "/dev/urandom", "/dev/tty", "/dev/ptmx", "/dev/pts"}

// Status describes the sandbox for the Tools panel.
type Status struct {
	Mode   string `json:"mode"`
	Active bool   `json:"active"` // commands actually run sandboxed
	ABI    int    `json:"abi"`    // Landlock ABI version; 0 when unavailable
	Note   string `json:"note"`
}

// ABI is the kernel's Landlock ABI version, 0 when Landlock is unavailable.
func ABI() int {
	v, err := llsys.LandlockGetABIVersion()
	if err != nil {
		return 0
	}
	return v
}

func (c Config) Status() Status {
	abi := ABI()
	s := Status{Mode: c.Mode, ABI: abi, Active: c.Mode != Off && (abi > 0 || c.Enforcer == Bwrap)}
	switch {
	case c.Mode == Off:
		s.Note = "Off: agents' commands run with the server user's full access."
	case c.Enforcer == Bwrap:
		s.Note = "Commands run under bubblewrap: they see only their project folder (plus a shared cache), system files and toolchains, and their own processes."
	case abi == 0 && c.Mode == On:
		s.Note = "Required, but this kernel has no Landlock: commands are refused."
	case abi == 0:
		s.Note = "This kernel has no Landlock: commands run with the server user's full access."
	default:
		s.Note = "Commands can read and write only their project folder (plus a shared cache), read system files and toolchains, and cannot see the rest of the home folder or Xhaos's data."
	}
	return s
}

// policy is what the helper restricts itself to.
type policy struct {
	Read  []string `json:"read"`  // directories: read and execute
	Files []string `json:"files"` // single files: read
	Write []string `json:"write"` // directories: read, write, execute
	// Devices: device files (or folders of them) to read, write and ioctl,
	// when /dev itself is not in Write; /dev can then only be listed.
	Devices []string `json:"devices,omitempty"`
	// Sealed commands: the only TCP port they may connect to (the egress
	// proxy), and no UDP (DNS lookups could carry a secret out).
	Port    int  `json:"port,omitempty"`
	DenyUDP bool `json:"denyUdp,omitempty"`
	// Bwrap: bubblewrap confines the helper already (its mounts are this
	// policy); the helper only adds the seccomp filter.
	Bwrap bool `json:"bwrap,omitempty"`
}

// Command builds the command that runs `bash -c command` in root, inside
// the sandbox when it is active. The error says why a command cannot run.
// Extra is more for one command: folders it may read (skills' files),
// environment variables, and the folder it starts in when not root.
type Extra struct {
	Read []string
	Env  []string
	Dir  string
}

func (x Extra) apply(env []string, p *policy) []string {
	p.Read = append(p.Read, x.Read...)
	return append(env, x.Env...)
}

func (c Config) Command(ctx context.Context, root, command string, extra ...Extra) (*exec.Cmd, error) {
	dir := root
	for _, x := range extra {
		if x.Dir != "" {
			dir = x.Dir
		}
	}
	plain := func() (*exec.Cmd, error) {
		cmd := exec.CommandContext(ctx, "bash", "-c", command)
		cmd.Dir = dir
		for _, x := range extra {
			if len(x.Env) > 0 {
				cmd.Env = append(os.Environ(), x.Env...)
			}
		}
		return cmd, nil
	}
	if c.Mode == Off || c.Mode == "" {
		return plain()
	}
	bwrap := c.Enforcer == Bwrap
	if !bwrap && ABI() == 0 {
		if c.Mode == On {
			return nil, errors.New("commands must run sandboxed, and this kernel has no Landlock")
		}
		return plain()
	}
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	env, p, err := c.prepare(root)
	if err != nil {
		return nil, err
	}
	for _, x := range extra {
		env = x.apply(env, &p)
	}
	p.Bwrap = bwrap
	raw, _ := json.Marshal(p)
	var cmd *exec.Cmd
	if bwrap {
		if cmd, err = bwrapCommand(ctx, self, p, raw, dir, command); err != nil {
			return nil, err
		}
	} else {
		cmd = exec.CommandContext(ctx, self, helperArg, string(raw), command)
	}
	cmd.Dir = dir
	cmd.Env = env
	return cmd, nil
}

// Sealed describes a command that holds secrets (a "sealed environment").
type Sealed struct {
	Env       []string // NAME=value: the secrets and the proxy settings
	ProxyPort int      // the one TCP port it may connect to (127.0.0.1)
	// Setup, when set, prepares the scratch folder before the command runs
	// (SSH keys and config) and returns more environment; its values win.
	Setup func(scratch string) ([]string, error)
	// Done, when set, runs after the command, before the scratch folder is
	// deleted (to keep SSH host keys it accepted).
	Done func(scratch string)
	// Extra adds read-only folders and variables (skills).
	Extra Extra
}

// NetABI is the first Landlock ABI that restricts TCP connections.
const NetABI = 4

// CommandSealed is Command for a command that holds secrets. On top of the
// usual sandbox: the project is read-only (except .git, so git can push and
// update its refs); HOME, TMPDIR and the tool caches are a throwaway folder,
// deleted by the returned cleanup, so nothing it writes outlives it; it may
// connect only to the proxy's port, and cannot open UDP sockets. Secrets
// need the sandbox: without it, or on a kernel whose Landlock cannot
// restrict the network, this refuses.
func (c Config) CommandSealed(ctx context.Context, root, command string, s Sealed) (*exec.Cmd, func(), error) {
	if c.Mode == Off || c.Mode == "" {
		return nil, nil, errors.New("secrets are only given to sandboxed commands, and the sandbox is off (start the server with -sandbox auto)")
	}
	if c.Enforcer == Bwrap {
		return nil, nil, errors.New("secrets need Landlock, and commands here run under bubblewrap")
	}
	if abi := ABI(); abi < NetABI {
		return nil, nil, fmt.Errorf("secrets need a kernel whose Landlock can restrict the network (ABI %d+, Linux 6.7+); this one has ABI %d", NetABI, abi)
	}
	if s.ProxyPort <= 0 {
		return nil, nil, errors.New("sandbox: sealed command without a proxy")
	}
	self, err := os.Executable()
	if err != nil {
		return nil, nil, err
	}
	env, p, err := c.prepare(root)
	if err != nil {
		return nil, nil, err
	}
	project, err := TempDir(root)
	if err != nil {
		return nil, nil, err
	}
	scratch, err := os.MkdirTemp(project, "sealed-")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() {
		if s.Done != nil {
			s.Done(scratch)
		}
		os.RemoveAll(scratch)
	}
	for _, d := range []string{"home", "tmp", "cache", "go-build"} {
		os.MkdirAll(filepath.Join(scratch, d), 0o700)
	}
	// The project and the shared cache become read-only; only the scratch
	// folder, devices and the project's .git stay writable.
	p.Read = append(p.Read, root, c.Cache)
	p.Write = append([]string{scratch, "/dev"}, GitDirs(root)...)
	p.Port, p.DenyUDP = s.ProxyPort, true
	over := map[string]string{
		"HOME": filepath.Join(scratch, "home"), "TMPDIR": filepath.Join(scratch, "tmp"),
		"XDG_CACHE_HOME": filepath.Join(scratch, "cache"), "GOCACHE": filepath.Join(scratch, "go-build"),
		"npm_config_cache": filepath.Join(scratch, "cache", "npm"),
	}
	out := env[:0:0]
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if _, replaced := over[k]; !replaced {
			out = append(out, kv)
		}
	}
	for k, v := range over {
		out = append(out, k+"="+v)
	}
	out = append(out, s.Env...)
	if s.Setup != nil {
		more, err := s.Setup(scratch)
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		for _, kv := range more {
			k, _, _ := strings.Cut(kv, "=")
			out = slices.DeleteFunc(out, func(e string) bool { return strings.HasPrefix(e, k+"=") })
			out = append(out, kv)
		}
	}
	// The command may start the server's binary as ssh's ProxyCommand.
	p.Files = append(p.Files, self)
	out = s.Extra.apply(out, &p)
	raw, _ := json.Marshal(p)
	cmd := exec.CommandContext(ctx, self, helperArg, string(raw), command)
	cmd.Dir = root
	cmd.Env = out
	return cmd, cleanup, nil
}

// prepare makes the cache folders and works out the environment and the
// policy for a command in root.
func (c Config) prepare(root string) ([]string, policy, error) {
	if c.Cache == "" {
		return nil, policy{}, errors.New("sandbox: no cache folder configured")
	}
	dirs := map[string]string{}
	for _, d := range []string{"home", "cache", "go-build", "go-mod", "npm"} {
		dirs[d] = filepath.Join(c.Cache, d)
		if err := os.MkdirAll(dirs[d], 0o700); err != nil {
			return nil, policy{}, err
		}
	}
	// Temp files go to a folder of the project's own under /tmp, which the
	// system clears: /tmp/xhaos-<uid>/sandboxes/<project>-<hash>.
	tmp, err := TempDir(root)
	if err != nil {
		return nil, policy{}, err
	}
	dirs["tmp"] = tmp
	home, _ := os.UserHomeDir()
	p := policy{
		Read:  []string{"/usr", "/bin", "/sbin", "/lib", "/lib32", "/lib64", "/libx32", "/etc", "/opt", "/proc", "/sys", "/snap", "/nix", "/run/systemd/resolve", "/var/lib/ca-certificates"},
		Write: []string{root, c.Cache, tmp, "/dev"},
	}
	if c.LimitDevices {
		p.Write = []string{root, c.Cache, tmp, "/dev/shm"}
		p.Devices = append(slices.Clone(commonDevices), c.Devices...)
	}
	// A submodule's or worktree's git data can live outside the project.
	p.Write = append(p.Write, GitDirs(root)...)
	// Toolchains on PATH that live in the home folder (~/.local/go/bin,
	// ~/.cargo/bin, …), with their install folder when the entry is a bin/;
	// never the home folder itself or ~/.local (that holds Xhaos's data).
	path := os.Getenv("PATH")
	for _, entry := range filepath.SplitList(path) {
		if entry == "" || !filepath.IsAbs(entry) {
			continue
		}
		p.Read = append(p.Read, entry)
		if home != "" && strings.HasPrefix(entry, home+"/") && filepath.Base(entry) == "bin" {
			parent := filepath.Dir(entry)
			if parent != home && parent != filepath.Join(home, ".local") {
				p.Read = append(p.Read, parent)
			}
		}
	}
	env := []string{
		"PATH=" + path,
		"HOME=" + dirs["home"],
		"TMPDIR=" + dirs["tmp"],
		"XDG_CACHE_HOME=" + dirs["cache"],
		"GOCACHE=" + dirs["go-build"],
		"GOMODCACHE=" + dirs["go-mod"],
		"npm_config_cache=" + dirs["npm"],
		"XHAOS_SANDBOX=1",
	}
	// Toolchain settings pass through: a system-wide rustup keeps its
	// toolchains in RUSTUP_HOME (under /opt or /usr, readable here), which its
	// cargo and rustc proxies need. CARGO_HOME does not: cargo's registry
	// cache stays in the command's own home, which it can write.
	for _, k := range []string{"LANG", "LC_ALL", "TERM", "TZ", "USER", "LOGNAME", "GOTOOLCHAIN", "GOROOT", "GOPATH", "GOFLAGS", "CGO_ENABLED", "RUSTUP_HOME", "RUSTUP_TOOLCHAIN"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	if home != "" {
		for _, f := range []string{".gitconfig", ".config/git/config"} {
			if _, err := os.Stat(filepath.Join(home, f)); err == nil {
				p.Files = append(p.Files, filepath.Join(home, f))
				if f == ".gitconfig" {
					env = append(env, "GIT_CONFIG_GLOBAL="+filepath.Join(home, f))
				}
			}
		}
	}
	p.Read = slices.Compact(p.Read)
	return env, p, nil
}

// TempDir is the private temp folder for commands in root.
func TempDir(root string) (string, error) {
	sum := sha256.Sum256([]byte(root))
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("xhaos-%d", os.Getuid()), "sandboxes", fmt.Sprintf("%s-%x", filepath.Base(root), sum[:4]))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	// Another user could have made /tmp/xhaos-<uid> first: it must be ours.
	if fi, err := os.Stat(filepath.Dir(filepath.Dir(dir))); err != nil {
		return "", err
	} else if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Getuid() || fi.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("sandbox: %s is not private to this user", filepath.Dir(filepath.Dir(dir)))
	}
	return dir, nil
}

// MaybeRun is the helper side: when this process was started by Command,
// it restricts itself and execs bash; it never returns then. Call it first
// in main (and in TestMain of packages that run commands).
func MaybeRun() {
	if len(os.Args) != 4 || os.Args[1] != helperArg {
		return
	}
	var p policy
	if err := json.Unmarshal([]byte(os.Args[2]), &p); err != nil {
		fail(err)
	}
	if p.Bwrap {
		// Inside bwrap: its mounts are the policy; only Unix sockets are left.
		if err := denySockets(p.DenyUDP); err != nil {
			fail(err)
		}
	} else if err := restrict(p); err != nil {
		fail(err)
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		fail(err)
	}
	fail(syscall.Exec(bash, []string{"bash", "-c", os.Args[3]}, os.Environ()))
}

func restrict(p policy) error {
	cfg := landlock.V6.BestEffort()
	rules := []landlock.Rule{
		landlock.RODirs(p.Read...).IgnoreIfMissing(),
		landlock.ROFiles(p.Files...).IgnoreIfMissing(),
		landlock.RWDirs(p.Write...).WithIoctlDev().WithRefer().IgnoreIfMissing(),
	}
	if len(p.Devices) > 0 {
		rules = append(rules, landlock.RWFiles(p.Devices...).WithIoctlDev().IgnoreIfMissing(),
			landlock.PathAccess(llsys.AccessFSReadDir, "/dev").IgnoreIfMissing())
	}
	if err := cfg.RestrictPaths(rules...); err != nil {
		return fmt.Errorf("landlock paths: %w", err)
	}
	// Signals and abstract UNIX sockets only reach processes in the same
	// sandbox (so a command cannot signal the server).
	if err := cfg.RestrictScoped(); err != nil {
		return fmt.Errorf("landlock scope: %w", err)
	}
	if p.Port > 0 {
		// BestEffort would quietly skip this on an older kernel: check.
		if ABI() < NetABI {
			return errors.New("landlock cannot restrict the network on this kernel")
		}
		if err := landlock.V6.RestrictNet(landlock.ConnectTCP(uint16(p.Port))); err != nil {
			return fmt.Errorf("landlock network: %w", err)
		}
	}
	return denySockets(p.DenyUDP)
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "xhaos sandbox: %v\n", err)
	os.Exit(126)
}
