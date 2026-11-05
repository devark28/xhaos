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
//   - read and write its project folder, and a shared cache (HOME, Go, npm,
//     and other tool caches live there, never in the real home);
//   - read and run system files (/usr, /etc, …) and the toolchains on PATH;
//   - read the git identity (~/.gitconfig), but not ~/.ssh or credentials.
//
// It cannot read the rest of the home folder (other projects, Xhaos's
// database, transcripts, attachments, the key file), and cannot signal or
// connect to processes outside the sandbox through abstract sockets.
// Network access is unchanged for now.
package sandbox

import (
	"context"
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
}

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
	s := Status{Mode: c.Mode, ABI: abi, Active: c.Mode != Off && abi > 0}
	switch {
	case c.Mode == Off:
		s.Note = "Off: agents' commands run with the server user's full access."
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
}

// Command builds the command that runs `bash -c command` in root, inside
// the sandbox when it is active. The error says why a command cannot run.
func (c Config) Command(ctx context.Context, root, command string) (*exec.Cmd, error) {
	if c.Mode == Off || c.Mode == "" {
		cmd := exec.CommandContext(ctx, "bash", "-c", command)
		cmd.Dir = root
		return cmd, nil
	}
	if ABI() == 0 {
		if c.Mode == On {
			return nil, errors.New("commands must run sandboxed, and this kernel has no Landlock")
		}
		cmd := exec.CommandContext(ctx, "bash", "-c", command)
		cmd.Dir = root
		return cmd, nil
	}
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	env, p, err := c.prepare(root)
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(p)
	cmd := exec.CommandContext(ctx, self, helperArg, string(raw), command)
	cmd.Dir = root
	cmd.Env = env
	return cmd, nil
}

// prepare makes the cache folders and works out the environment and the
// policy for a command in root.
func (c Config) prepare(root string) ([]string, policy, error) {
	if c.Cache == "" {
		return nil, policy{}, errors.New("sandbox: no cache folder configured")
	}
	dirs := map[string]string{}
	for _, d := range []string{"home", "tmp", "cache", "go-build", "go-mod", "npm"} {
		dirs[d] = filepath.Join(c.Cache, d)
		if err := os.MkdirAll(dirs[d], 0o700); err != nil {
			return nil, policy{}, err
		}
	}
	home, _ := os.UserHomeDir()
	p := policy{
		Read:  []string{"/usr", "/bin", "/sbin", "/lib", "/lib32", "/lib64", "/libx32", "/etc", "/opt", "/proc", "/sys", "/snap", "/nix", "/run/systemd/resolve", "/var/lib/ca-certificates"},
		Write: []string{root, c.Cache, "/dev"},
	}
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
	for _, k := range []string{"LANG", "LC_ALL", "TERM", "TZ", "USER", "LOGNAME", "GOTOOLCHAIN", "GOROOT", "GOPATH", "GOFLAGS", "CGO_ENABLED"} {
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
	if err := restrict(p); err != nil {
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
		landlock.RWDirs(p.Write...).WithIoctlDev().WithRefer(),
	}
	if err := cfg.RestrictPaths(rules...); err != nil {
		return fmt.Errorf("landlock paths: %w", err)
	}
	// Signals and abstract UNIX sockets only reach processes in the same
	// sandbox (so a command cannot signal the server).
	if err := cfg.RestrictScoped(); err != nil {
		return fmt.Errorf("landlock scope: %w", err)
	}
	return nil
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "xhaos sandbox: %v\n", err)
	os.Exit(126)
}
