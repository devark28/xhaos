package tool

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Read-class commands that run git get git's settings through the
// environment (GIT_CONFIG_COUNT, which every git process reads, subshells
// included), so they hold even where the scan of the project's config
// (GitDriverRisk) misses a repo. The floor and the scan stay as extra
// signals; the sandbox is the boundary (see the floor's documentation).

// gitSafePairs are the config settings every read-class git run gets. Each
// one stops a program git would otherwise run, or a remote it would reach.
var gitSafePairs = [][2]string{
	{"core.fsmonitor", "false"},     // runs a program to find changed files
	{"core.hooksPath", "/dev/null"}, // runs the project's hooks (post-index-change, ...)
	{"core.attributesFile", "/dev/null"},
	{"protocol.allow", "never"},      // no remote protocol, lazy fetch included
	{"protocol.ext.allow", "never"},  // ext:: runs a program
	{"protocol.file.allow", "never"}, // no local clone or fetch
}

// gitSafeEnv returns the environment for a read-class command that runs git
// in root. The drivers git lists (filter clean, smudge and process; textconv
// and command for diff) are blanked, so no attribute can run them.
func gitSafeEnv(root string) []string {
	home, _ := os.UserHomeDir()
	global := "/dev/null"
	for _, f := range []string{".gitconfig", ".config/git/config"} {
		if home != "" && fileExists(filepath.Join(home, f)) {
			global = filepath.Join(home, f)
			break
		}
	}
	env := []string{
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_CONFIG_GLOBAL=" + global,
	}
	// git looks for a repo in the folders above root; the parent of root is
	// the ceiling, so a repo around the project is not used.
	if parent := filepath.Dir(root); parent != root {
		env = append(env, "GIT_CEILING_DIRECTORIES="+parent)
	}
	pairs := slicesClone(gitSafePairs)
	for _, k := range gitDriverNames(root, home, env) {
		pairs = append(pairs, [2]string{k, ""})
	}
	env = append(env, fmt.Sprintf("GIT_CONFIG_COUNT=%d", len(pairs)))
	for i, p := range pairs {
		env = append(env, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, p[0]), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, p[1]))
	}
	return env
}

// gitDriverNames lists the config names git itself finds for a filter's
// clean, smudge or process, and a diff driver's textconv or command.
func gitDriverNames(root, home string, env []string) []string {
	out := gitOutput(root, home, env, "config", "--name-only", "--get-regexp", `^(filter|diff)\.`)
	var names []string
	for _, name := range strings.Fields(out) {
		last := name[strings.LastIndex(name, ".")+1:]
		switch {
		case strings.HasPrefix(name, "filter.") && (last == "clean" || last == "smudge" || last == "process"):
			names = append(names, name)
		case strings.HasPrefix(name, "diff.") && (last == "textconv" || last == "command"):
			names = append(names, name)
		}
	}
	return names
}

// gitConfigRuns asks git itself which config the project's repo has, after
// its own parse of symlinks, .git files, commondir, worktree config and
// includes, and reports whether any of it could make git run a program: a key
// in runKeys, a key of a runSections section, or an ext:: URL. The owner's
// global and system config are not read here (the owner's own config is
// trusted), and the safe settings are not set (they would name core.fsmonitor
// and hide the repo's own). A config git cannot read asks.
func gitConfigRuns(root string) bool {
	if root == "" {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	home, _ := os.UserHomeDir()
	cmd := exec.CommandContext(ctx, "git", "config", "-z", "--list")
	cmd.Dir = root
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_NO_REPLACE_OBJECTS=1"}
	if parent := filepath.Dir(root); parent != root {
		env = append(env, "GIT_CEILING_DIRECTORIES="+parent)
	}
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		return true // a repo or config git cannot read: ask
	}
	for _, entry := range strings.Split(string(out), "\x00") {
		name, value, _ := strings.Cut(entry, "\n")
		if name == "" {
			continue
		}
		// git lists names as section.[subsection.]key, with the section and
		// key in lower case: the run keys are section.key with no subsection.
		section, _, _ := strings.Cut(name, ".")
		key := name[strings.LastIndex(name, ".")+1:]
		if runSections[section] || runKeys[section+"."+key] {
			return true
		}
		// url.<base>.insteadOf rewrites a URL to <base>: an ext:: base is in
		// the subsection name, an ext:: prefix is in the value.
		if (key == "url" || key == "insteadof" || key == "pushinsteadof") && (strings.Contains(value, "ext::") || strings.Contains(name, "ext::")) {
			return true
		}
	}
	return false
}

// gitOutput runs git with args in root under env (plus PATH and HOME) and
// returns its standard output; "" when git fails (not a repo, no config).
func gitOutput(root, home string, env []string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + home}, env...)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return ""
	}
	return out.String()
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func slicesClone(p [][2]string) [][2]string { return append([][2]string(nil), p...) }

// slicesClip returns s with no spare capacity, so appending to it copies.
func slicesClip(s []string) []string { return s[:len(s):len(s)] }
