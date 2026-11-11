package mcpserver

// The approve-feature-branch scope never approves the agent's own command
// text. It reads the command only to learn what the agent wants, as a
// structured intent:
//
//   - commit: git add … and git commit -m … in the project (or a folder of
//     it, by cd), on the branch HEAD resolves to, through every symbolic
//     ref, which must be a branch that is not protected;
//   - push: one local branch (an existing refs/heads/ name, not a symbolic
//     ref, not protected) to one remote configured in the repo by name,
//     whose URL is https or ssh and is not rewritten for pushing (no
//     pushurl, no insteadOf or pushInsteadOf that matches it).
//
// Then Xhaos runs its own command for that intent in the agent's place
// (agent.ToolData.Run): hooks and fsmonitor off; the message passed as
// quoted text; the push to the remote's URL itself (so the remote's push,
// mirror and pushurl settings do not apply) with the one refspec
// refs/heads/<b>:refs/heads/<b>, no tags, the global and system config left
// out, and only the remote's transport allowed. No force, tags, wildcards or
// other destinations can come out of it by construction. A command that
// does not map to such an intent is the owner's to decide.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/devark28/xhaos/app/danger"
	"github.com/devark28/xhaos/app/tool"
)

// protected is a branch only the owner commits or pushes to: danger's list
// (main, master, trunk, develop, production, release…) and integration,
// compared without case (remotes on case-insensitive file systems).
func protected(branch string) bool {
	b := strings.ToLower(branch)
	return danger.Protected(b) || b == "integration" || strings.HasPrefix(b, "integration/")
}

// branchName is a plain branch name: no refspec, glob, revision syntax or
// leading dash.
var branchName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)

func validBranch(b string) bool {
	return branchName.MatchString(b) && !strings.Contains(b, "..") && !strings.HasSuffix(b, ".lock") &&
		!strings.HasSuffix(b, "/") && !strings.HasSuffix(b, ".") && !strings.Contains(b, "//")
}

// intent is what the scope may allow, and the command Xhaos runs for it.
type intent struct {
	Branch string `json:"branch"`           // committed on and/or pushed
	Commit bool   `json:"commit,omitempty"` // a commit on Branch
	Remote string `json:"remote,omitempty"` // a push of Branch to this remote…
	URL    string `json:"url,omitempty"`    // …at this URL
	Run    string `json:"run"`              // Xhaos's own command
}

func (in intent) String() string {
	var parts []string
	if in.Commit {
		parts = append(parts, "commit on "+in.Branch)
	}
	if in.Remote != "" {
		parts = append(parts, fmt.Sprintf("push %s to %s (%s)", in.Branch, in.Remote, in.URL))
	}
	return strings.Join(parts, ", then ")
}

// planner reads a project's repo with git's own parse (the forced-safe git
// environment, run by the server).
type planner struct {
	root string
	// local lets a push go to a local path or file:// URL (tests only).
	local bool
}

func (p planner) git(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := tool.GitSafeCommand(ctx, p.root, dir, args...).Output()
	return strings.TrimSpace(string(out)), err
}

// metachars are refused anywhere in the command: what bash would read
// differently from the words we parse (substitution, variables,
// redirection, globs, ~ and revision syntax).
const metachars = "`$<>\\*?[]~^{}"

// errOwner wraps why the owner must decide.
type errOwner struct{ why string }

func (e errOwner) Error() string { return e.why }

func ownerOnly(format string, a ...any) error { return errOwner{fmt.Sprintf(format, a...)} }

// plan reads command (a bash call run in root) as an intent.
func (p planner) plan(command string) (intent, error) {
	line := tool.JoinLines(command)
	if strings.ContainsAny(line, metachars) || strings.Contains(line, "@{") {
		return intent{}, ownerOnly("the command uses shell substitution, variables, redirection or globs")
	}
	cmds, ok := tool.SimpleCommands(line)
	if !ok || len(cmds) == 0 {
		return intent{}, ownerOnly("the command cannot be read as plain git commands")
	}
	dir := p.root
	if cmds[0][0] == "cd" {
		if len(cmds[0]) != 2 {
			return intent{}, ownerOnly("cd must name one folder")
		}
		d, err := inside(p.root, p.root, cmds[0][1])
		if err != nil {
			return intent{}, err
		}
		dir, cmds = d, cmds[1:]
	}
	var adds [][]string
	var commit []string
	var push []string
	for _, w := range cmds {
		if w[0] != "git" || len(w) < 2 {
			return intent{}, ownerOnly("the command runs %q as well as git (only cd, then git add, commit and push)", w[0])
		}
		switch sub := w[1]; {
		case strings.HasPrefix(sub, "-"):
			return intent{}, ownerOnly("git option %s is left to the owner", sub)
		case sub == "add" && commit == nil && push == nil:
			adds = append(adds, w[2:])
		case sub == "commit" && commit == nil && push == nil:
			commit = w[2:]
		case sub == "push" && push == nil:
			push = w[2:]
		default:
			return intent{}, ownerOnly("git %s here is left to the owner (only git add, one commit, then one push)", sub)
		}
	}
	if commit == nil && push == nil {
		return intent{}, ownerOnly("the command neither commits nor pushes")
	}
	var head string
	if commit != nil || !pushNamesBranch(push) {
		h, err := p.head(dir)
		if err != nil {
			return intent{}, err
		}
		head = h
	}
	in := intent{Branch: head}
	var run []string
	run = append(run, "cd "+quote(dir))
	safe := "git -c core.hooksPath=/dev/null -c core.fsmonitor=false"
	if commit != nil {
		addRun, err := addCommands(adds, safe)
		if err != nil {
			return intent{}, err
		}
		commitRun, err := commitCommand(commit, safe)
		if err != nil {
			return intent{}, err
		}
		in.Commit = true
		// At run time HEAD must still resolve to the approved branch.
		run = append(run, fmt.Sprintf("test \"$(git symbolic-ref -q HEAD)\" = %s", quote("refs/heads/"+head)))
		run = append(run, addRun...)
		run = append(run, commitRun)
	} else if len(adds) > 0 {
		return intent{}, ownerOnly("git add without a commit is left to the owner")
	}
	if push != nil {
		remote, branch, err := pushArgs(push)
		if err != nil {
			return intent{}, err
		}
		if branch == "" {
			branch = head
		}
		if in.Commit && branch != head {
			return intent{}, ownerOnly("the push names %s but the commit is on %s", branch, head)
		}
		if err := p.localBranch(dir, branch); err != nil && !(in.Commit && branch == head) {
			return intent{}, err
		}
		if remote == "" {
			remote = "origin"
			if r, err := p.git(dir, "config", "--get", "branch."+branch+".remote"); err == nil && r != "" {
				remote = r
			}
		}
		url, transport, err := p.remoteURL(dir, remote)
		if err != nil {
			return intent{}, err
		}
		in.Branch, in.Remote, in.URL = branch, remote, url
		ref := "refs/heads/" + branch
		run = append(run, "GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null "+safe+
			" -c push.recurseSubmodules=no -c push.followTags=false -c push.gpgSign=false -c protocol.allow=never -c protocol."+transport+".allow=always"+
			" push --no-follow-tags --no-verify "+quote(url)+" "+quote(ref+":"+ref))
	}
	in.Run = strings.Join(run, " && ")
	return in, nil
}

// head is the branch HEAD resolves to in dir, through every symbolic ref; it
// must be a branch that is not protected.
func (p planner) head(dir string) (string, error) {
	ref, err := p.git(dir, "symbolic-ref", "-q", "HEAD")
	if err != nil || ref == "" {
		return "", ownerOnly("HEAD is not on a branch (detached, or not a git repository)")
	}
	b, ok := strings.CutPrefix(ref, "refs/heads/")
	if !ok || !validBranch(b) {
		return "", ownerOnly("HEAD resolves to %s, not a plain branch", ref)
	}
	if protected(b) {
		return "", ownerOnly("HEAD resolves to %s, a protected branch", b)
	}
	return b, nil
}

// localBranch checks that b is an existing local branch, not a symbolic
// ref, and not protected.
func (p planner) localBranch(dir, b string) error {
	if !validBranch(b) {
		return ownerOnly("%q is not a plain branch name", b)
	}
	if protected(b) {
		return ownerOnly("%s is a protected branch", b)
	}
	if _, err := p.git(dir, "show-ref", "--verify", "-q", "refs/heads/"+b); err != nil {
		return ownerOnly("there is no local branch %s", b)
	}
	if target, err := p.git(dir, "symbolic-ref", "-q", "refs/heads/"+b); err == nil && target != "" {
		return ownerOnly("branch %s is a symbolic ref (to %s)", b, target)
	}
	return nil
}

// pushNamesBranch reports whether a push's arguments name its branch.
func pushNamesBranch(args []string) bool {
	_, b, err := pushArgs(args)
	return err == nil && b != ""
}

// pushArgs reads git push [-u] [<remote>] [<branch>].
func pushArgs(args []string) (remote, branch string, err error) {
	var plain []string
	for _, a := range args {
		switch {
		case a == "-u" || a == "--set-upstream" || a == "-q" || a == "--quiet":
		case strings.HasPrefix(a, "-"):
			return "", "", ownerOnly("git push %s is left to the owner", a)
		default:
			plain = append(plain, a)
		}
	}
	switch len(plain) {
	case 0:
	case 1:
		remote = plain[0]
	case 2:
		remote, branch = plain[0], plain[1]
	default:
		return "", "", ownerOnly("the push names more than one branch")
	}
	if remote != "" && !remoteName.MatchString(remote) {
		return "", "", ownerOnly("the push goes to %q, not to a remote of the repo by name", remote)
	}
	if branch != "" && !validBranch(branch) {
		return "", "", ownerOnly("%q is not a plain branch name (no refspecs, tags or patterns)", branch)
	}
	return remote, branch, nil
}

var remoteName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// remoteURL is the URL of the remote named name, and its transport. The
// remote must be configured in the repo with one URL, no pushurl or other
// push redirection, and no insteadOf or pushInsteadOf rewriting it.
func (p planner) remoteURL(dir, name string) (url, transport string, err error) {
	cfg, err := p.git(dir, "config", "--includes", "--get-regexp", `^(remote|url|core|http)\.`)
	if err != nil && cfg == "" {
		return "", "", ownerOnly("the repo has no remotes")
	}
	var urls []string
	// Section and key names come lowercased; the remote's name keeps its case.
	prefix := "remote." + name + "."
	type rewrite struct{ key, value string }
	var rewrites []rewrite
	for _, line := range strings.Split(cfg, "\n") {
		key, value, _ := strings.Cut(line, " ")
		k := strings.ToLower(key)
		switch {
		case strings.HasPrefix(key, prefix) && !strings.Contains(key[len(prefix):], "."):
			switch k[len(prefix):] {
			case "url":
				urls = append(urls, value)
			case "pushurl", "receivepack", "proxy", "vcs":
				return "", "", ownerOnly("remote %s has %s set; its pushes are left to the owner", name, k[len(prefix):])
			}
		case strings.HasPrefix(k, "url.") && (strings.HasSuffix(k, ".insteadof") || strings.HasSuffix(k, ".pushinsteadof")):
			rewrites = append(rewrites, rewrite{key, value})
		case k == "core.sshcommand" || k == "core.gitproxy" || (strings.HasPrefix(k, "http.") && strings.HasSuffix(k, "proxy")):
			return "", "", ownerOnly("the repo sets %s; its pushes are left to the owner", key)
		}
	}
	if len(urls) == 0 {
		return "", "", ownerOnly("there is no remote named %s in the repo", name)
	}
	if len(urls) > 1 {
		return "", "", ownerOnly("remote %s has several URLs", name)
	}
	url = urls[0]
	for _, r := range rewrites {
		if r.value != "" && strings.HasPrefix(url, r.value) {
			return "", "", ownerOnly("%s rewrites the remote's URL", r.key)
		}
	}
	if transport = urlTransport(url, p.local); transport == "" {
		return "", "", ownerOnly("remote %s's URL is not https or ssh", name)
	}
	return url, transport, nil
}

// scpLike is git's user@host:path form (ssh).
var scpLike = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[^/]?`)

// urlTransport is the protocol a URL pushes over: https or ssh (file for a
// local path when local is set), or "" for anything else.
func urlTransport(url string, local bool) string {
	if url == "" || strings.HasPrefix(url, "-") || strings.ContainsAny(url, " \t\n'\"\\`$") {
		return ""
	}
	switch {
	case strings.HasPrefix(url, "https://"):
		return "https"
	case strings.HasPrefix(url, "ssh://"):
		return "ssh"
	case !strings.Contains(url, "://") && scpLike.MatchString(url):
		return "ssh"
	case local && (strings.HasPrefix(url, "file://") || filepath.IsAbs(url)):
		return "file"
	}
	return ""
}

// addCommands rebuilds git add: -A, -u, or paths inside the folder.
func addCommands(adds [][]string, safe string) ([]string, error) {
	var out []string
	for _, args := range adds {
		var flags, paths []string
		dashes := false
		for _, a := range args {
			switch {
			case !dashes && a == "--":
				dashes = true
			case !dashes && (a == "-A" || a == "--all" || a == "-u" || a == "--update"):
				flags = append(flags, a)
			case !dashes && strings.HasPrefix(a, "-"):
				return nil, ownerOnly("git add %s is left to the owner", a)
			case filepath.IsAbs(a) || a == ".." || strings.HasPrefix(filepath.Clean(a), "../"):
				return nil, ownerOnly("git add %s reaches outside the folder", a)
			default:
				paths = append(paths, quote(a))
			}
		}
		cmd := safe + " add"
		if len(flags) > 0 {
			cmd += " " + strings.Join(flags, " ")
		}
		if len(paths) > 0 {
			cmd += " -- " + strings.Join(paths, " ")
		}
		out = append(out, cmd)
	}
	return out, nil
}

// commitCommand rebuilds git commit: messages (-m), -a, -s and -q only.
func commitCommand(args []string, safe string) (string, error) {
	var msgs, flags []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case (a == "-m" || a == "--message") && i+1 < len(args):
			msgs = append(msgs, args[i+1])
			i++
		case strings.HasPrefix(a, "--message="):
			msgs = append(msgs, strings.TrimPrefix(a, "--message="))
		case strings.HasPrefix(a, "-m") && len(a) > 2:
			msgs = append(msgs, a[2:])
		case a == "-a" || a == "--all" || a == "-s" || a == "--signoff" || a == "-q" || a == "--quiet":
			flags = append(flags, a)
		default:
			return "", ownerOnly("git commit %s is left to the owner (only -m, -a, -s and -q)", a)
		}
	}
	if len(msgs) == 0 {
		return "", ownerOnly("the commit has no -m message")
	}
	cmd := safe + " commit --no-verify"
	for _, f := range flags {
		cmd += " " + f
	}
	for _, m := range msgs {
		cmd += " -m " + quote(m)
	}
	return cmd, nil
}

// quote single-quotes s for bash.
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// inside resolves dir (relative to cwd) and requires it to be a folder in
// root. Its errors never say whether a path outside root exists.
func inside(root, cwd, dir string) (string, error) {
	refuse := ownerOnly("cd %s: not a folder inside the project", dir)
	if dir == "" || dir == "-" || strings.HasPrefix(dir, "~") {
		return "", refuse
	}
	p := dir
	if !filepath.IsAbs(p) {
		p = filepath.Join(cwd, p)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", refuse
	}
	real, err := filepath.EvalSymlinks(filepath.Clean(p))
	if err != nil {
		return "", refuse
	}
	if rel, err := filepath.Rel(realRoot, real); err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", refuse
	}
	if fi, err := os.Stat(real); err != nil || !fi.IsDir() {
		return "", refuse
	}
	return real, nil
}
