package mcpserver

// The approve-feature-branch scope lets an MCP client allow an agent's git
// commit or push, but only one it can read completely: the command must be
// plain git (and cd), every commit must land on a branch that is not
// protected, and every push must name such a branch, without force,
// deletion or tags. Anything else (another program in the same command,
// shell substitution, redirection, git options that change what runs, a
// detached HEAD) is left to the owner. Approving the call approves the
// whole command line, which is why nothing but git may be in it.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/devark28/xhaos/app/danger"
	"github.com/devark28/xhaos/app/tool"
)

// protected is a branch only the owner commits or pushes to: danger's list
// (main, master, trunk, develop, production, release…), integration, and
// anything that is not a plain branch name.
func protected(branch string) bool {
	b := strings.TrimPrefix(branch, "refs/heads/")
	switch {
	case b == "" || b == "HEAD" || b == "@":
		return true
	case danger.Protected(b), b == "integration", strings.HasPrefix(b, "integration/"):
		return true
	case strings.HasPrefix(branch, "refs/") && !strings.HasPrefix(branch, "refs/heads/"):
		return true // refs/tags/…, refs/remotes/…
	}
	return false
}

// readOnlyGit are git subcommands that may share a command line with a
// commit or push (they change nothing outside the index).
var readOnlyGit = map[string]bool{"status": true, "diff": true, "log": true, "show": true, "add": true, "rev-parse": true}

// featureBranchOnly checks a bash command line run in root (the agent's
// project) and returns the branches it commits or pushes to, or why the
// owner must decide.
func featureBranchOnly(root, command string) ([]string, error) {
	line := tool.JoinLines(command)
	if strings.ContainsAny(line, "`<>$\\") {
		return nil, errors.New("the command uses shell substitution, variables or redirection")
	}
	cmds, ok := tool.SimpleCommands(line)
	if !ok || len(cmds) == 0 {
		return nil, errors.New("the command cannot be read as plain git commands")
	}
	cwd := root
	var branches []string
	committed := false
	for _, words := range cmds {
		switch words[0] {
		case "cd":
			if len(words) != 2 {
				return nil, errors.New("cd must name one folder")
			}
			dir, err := inside(root, cwd, words[1])
			if err != nil {
				return nil, err
			}
			cwd = dir
			continue
		case "git":
		default:
			return nil, fmt.Errorf("the command runs %q as well as git", words[0])
		}
		dir, sub, args, err := gitWords(root, cwd, words[1:])
		if err != nil {
			return nil, err
		}
		switch {
		case readOnlyGit[sub]:
		case sub == "commit":
			b, err := danger.CurrentBranch(dir)
			if err != nil {
				return nil, err
			}
			if b == "" {
				return nil, errors.New("the commit is not in a git repository")
			}
			if protected(b) {
				return nil, fmt.Errorf("the commit lands on %s, a protected branch", b)
			}
			branches = appendNew(branches, b)
			committed = true
		case sub == "push":
			pushed, err := pushTargets(args)
			if err != nil {
				return nil, err
			}
			for _, b := range pushed {
				branches = appendNew(branches, b)
			}
		default:
			return nil, fmt.Errorf("git %s is left to the owner", sub)
		}
	}
	if len(branches) == 0 && !committed {
		return nil, errors.New("the command neither commits nor pushes")
	}
	return branches, nil
}

// gitWords reads git's own options (only -C <dir> and --no-pager are
// accepted) and returns the folder it runs in, the subcommand and its
// arguments.
func gitWords(root, cwd string, words []string) (dir, sub string, args []string, err error) {
	dir = cwd
	for i := 0; i < len(words); i++ {
		w := words[i]
		switch {
		case w == "-C" && i+1 < len(words):
			if dir, err = inside(root, dir, words[i+1]); err != nil {
				return "", "", nil, err
			}
			i++
		case w == "--no-pager":
		case strings.HasPrefix(w, "-"):
			return "", "", nil, fmt.Errorf("git option %s is left to the owner", w)
		default:
			return dir, w, words[i+1:], nil
		}
	}
	return "", "", nil, errors.New("git without a subcommand")
}

// pushTargets returns the branches a push names: git push <remote>
// <branch>… (src:dst refspecs allowed, the destination counts), with no
// force, delete, mirror, all or tags.
func pushTargets(args []string) ([]string, error) {
	var plain []string
	for _, a := range args {
		switch {
		case a == "-u" || a == "--set-upstream" || a == "-q" || a == "--quiet" || a == "-v" || a == "--verbose" || a == "--porcelain":
		case strings.HasPrefix(a, "-"):
			return nil, fmt.Errorf("git push %s is left to the owner", a)
		default:
			plain = append(plain, a)
		}
	}
	if len(plain) < 2 {
		return nil, errors.New("the push does not name its branch (git push <remote> <branch>)")
	}
	var out []string
	for _, ref := range plain[1:] {
		if strings.HasPrefix(ref, "+") || strings.HasPrefix(ref, ":") {
			return nil, fmt.Errorf("force or delete refspec %q is left to the owner", ref)
		}
		src, dst, isPair := strings.Cut(ref, ":")
		if !isPair {
			dst = src
		}
		if strings.HasPrefix(src, "refs/tags/") || protected(dst) {
			return nil, fmt.Errorf("the push goes to %s, a protected branch or a tag", dst)
		}
		out = appendNew(out, strings.TrimPrefix(dst, "refs/heads/"))
	}
	return out, nil
}

// inside resolves dir (relative to cwd) and requires it to stay in root.
func inside(root, cwd, dir string) (string, error) {
	if dir == "" || dir == "-" || strings.HasPrefix(dir, "~") {
		return "", fmt.Errorf("folder %q is left to the owner", dir)
	}
	p := dir
	if !filepath.IsAbs(p) {
		p = filepath.Join(cwd, p)
	}
	p = filepath.Clean(p)
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", fmt.Errorf("folder %s: %v", dir, errors.Unwrap(err))
	}
	if fi, err := os.Stat(real); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("%s is not a folder", dir)
	}
	if rel, err := filepath.Rel(realRoot, real); err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("folder %s is outside the project", dir)
	}
	return real, nil
}

func appendNew(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}
