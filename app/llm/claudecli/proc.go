package claudecli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

// CommandEnv names the Claude Code program to run when the adapter sets none.
const CommandEnv = "XHAOS_CLAUDE_COMMAND"

const installHint = "Claude Code is not installed for the user the Xhaos server runs as (no `claude` on PATH or in the usual install folders). " +
	"Install it with `npm install -g @anthropic-ai/claude-code`, or set " + CommandEnv + " to its path."

const loggedOutHint = "Claude Code has no usable login for the user the Xhaos server runs as. " +
	"Run `claude auth login` as that user, or set CLAUDE_CODE_OAUTH_TOKEN (from `claude setup-token`) in the server's environment."

// ErrNotInstalled and ErrLoggedOut say what to do; the run loop shows them to the owner.
var (
	ErrNotInstalled = errors.New(installHint)
	ErrLoggedOut    = errors.New(loggedOutHint)
)

// installDirs are looked in after PATH: a service started by systemd or a
// launcher has a short PATH that carries none of them.
func installDirs(home string) []string {
	dirs := []string{"/usr/local/bin", "/opt/homebrew/bin"}
	if home != "" {
		for _, d := range []string{".local/bin", ".claude/local", "bin", ".npm-global/bin", ".bun/bin", ".volta/bin"} {
			dirs = append([]string{filepath.Join(home, d)}, dirs...)
		}
	}
	return dirs
}

// resolve finds the program: an explicit path or name is the owner's choice
// and never second-guessed; the bare default also tries the install folders.
func resolve(command string, env []string) (string, error) {
	explicit := command != ""
	if command == "" {
		command = "claude"
	}
	if strings.ContainsRune(command, '/') {
		if info, err := os.Stat(command); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return command, nil
		}
		return "", ErrNotInstalled
	}
	if path, err := exec.LookPath(command); err == nil {
		return path, nil
	}
	if !explicit {
		home := envValue(env, "HOME")
		for _, dir := range installDirs(home) {
			p := filepath.Join(dir, command)
			if info, err := os.Stat(p); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
				return p, nil
			}
		}
	}
	return "", ErrNotInstalled
}

func envValue(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(env[i], key+"="); ok {
			return v
		}
	}
	return ""
}

// apiCredentials would make Claude Code sign in with an API key and bill API
// usage instead of the subscription, so no child receives one.
var apiCredentials = []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_FOUNDRY_API_KEY"}

// Every CLAUDE_CODE_USE_* variable (Bedrock, Vertex, Foundry, a gateway,
// AWS, Google Cloud, …, and ones later versions add) sends Claude Code
// somewhere other than Anthropic's own service, which is not the
// subscription either: refused, not stripped, because the owner set them on
// purpose.
const backendSwitchPrefix = "CLAUDE_CODE_USE_"

// parentSession are what a Claude Code session sets for the programs it
// starts. A server started from inside one would otherwise hand them on, and
// the child would take on that session's identity and messaging socket.
var parentSession = []string{"CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT", "CLAUDE_CODE_SESSION_ID",
	"CLAUDE_CODE_CHILD_SESSION", "CLAUDE_CODE_MESSAGING_SOCKET", "CLAUDE_CODE_MESSAGING_TOKEN",
	"CLAUDE_PID", "CLAUDE_EFFORT", "CLAUDE_CODE_EXECPATH"}

// childEnv is the environment of a Claude Code child: the server's, without
// API credentials, with what keeps a turn predictable.
func childEnv(base []string, extra ...string) ([]string, error) {
	if v := envValue(base, "ANTHROPIC_BASE_URL"); v != "" {
		return nil, errors.New("ANTHROPIC_BASE_URL is set in the server's environment, so Claude Code would not use the subscription; unset it")
	}
	for _, kv := range base {
		k, v, _ := strings.Cut(kv, "=")
		if !strings.HasPrefix(k, backendSwitchPrefix) {
			continue
		}
		switch strings.ToLower(v) {
		case "", "0", "false", "no", "off":
		default:
			return nil, fmt.Errorf("%s is set in the server's environment, so Claude Code would not use the subscription; unset it", k)
		}
	}
	drop := map[string]bool{}
	for _, k := range append(append(apiCredentials, parentSession...), "CLAUDE_CODE_EXTRA_BODY", "CLAUDE_CODE_EFFORT_LEVEL") {
		drop[k] = true // the last two would override the effort and body Xhaos sets
	}
	var env []string
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		// Every ANTHROPIC_* (keys, tokens, identity files, custom headers,
		// model overrides) is the API's, never the subscription's.
		if drop[k] || strings.HasPrefix(k, "ANTHROPIC_") {
			continue
		}
		env = append(env, kv)
	}
	// Without USER a logged-in Claude Code reports that it is logged out.
	if envValue(env, "USER") == "" {
		if u, err := user.Current(); err == nil {
			env = append(env, "USER="+u.Username, "LOGNAME="+u.Username)
		}
	}
	return append(env, extra...), nil
}

var (
	workdirOnce sync.Once
	workdir     string
)

// workDir is the folder Claude Code runs in. It names its working directory
// in the prompt, so a new folder per call would move the start of the prompt
// and miss the cache every time. It must be ours and closed to others, in a
// shared temp folder.
func workDir() (string, error) {
	path := filepath.Join(os.TempDir(), fmt.Sprintf("xhaos-claude-%d", os.Getuid()))
	if ours(path) {
		return path, nil
	}
	if err := os.Mkdir(path, 0o700); err == nil && ours(path) {
		return path, nil
	}
	// Something else sits there: one private folder for this process instead.
	var err error
	workdirOnce.Do(func() { workdir, err = os.MkdirTemp("", "xhaos-claude-") })
	if err != nil {
		return "", err
	}
	if workdir == "" {
		return "", errors.New("no private working folder for Claude Code")
	}
	return workdir, nil
}

// ours reports a real directory owned by this user and closed to others.
func ours(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid() && info.Mode().Perm()&0o077 == 0
}

// ring keeps the last lines Claude Code wrote to stderr: the whole diagnosis
// of a failure that has no other message.
type ring struct {
	mu    sync.Mutex
	lines []string
}

func (r *ring) add(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lines = append(r.lines, line); len(r.lines) > 40 {
		r.lines = r.lines[1:]
	}
}

// tail is the lines joined, cut to a size fit for an error message.
func (r *ring) tail() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var parts []string
	for _, l := range r.lines {
		if l = strings.TrimSpace(l); l != "" {
			parts = append(parts, l)
		}
	}
	text := strings.Join(parts, " ")
	if len(text) > 400 {
		text = text[:400]
	}
	if text == "" {
		return ""
	}
	return " (stderr: " + text + ")"
}
