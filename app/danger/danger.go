// Package danger is "dangerously allow": for a limited time, an agent's
// calls that would wait for the owner run without asking. Turning it on
// takes the passphrase; it is per agent (subagents do not inherit it),
// expires on its own, and is recorded in the agent's history.
//
// What it does not change:
//   - the sandbox (commands still see only their project);
//   - secrets (a command using one still asks unless the secret was trusted
//     to the agent), and channels to agents outside its family (still ask);
//   - denials (a read-only agent stays read-only; blocked commits stay blocked);
//   - protected branches (main, master, trunk, develop, production,
//     release…) and the project's own checkout stay as they were. Turning it
//     on switches nothing: the agent works on its own feature branches (in
//     worktrees under .worktrees/). Two guards keep the rest safe:
//   - before a command runs, its text is checked for what cannot be undone
//     afterwards: pushing to a protected branch, force or delete pushes,
//     pushes that do not name their branch, deleting or renaming branches;
//   - around every command, git itself is checked: if a protected branch
//     moved (or appeared, or went), it is put back, and if the project's
//     checkout changed branch it is switched back. The agent is told.
//
// The text check stops mistakes, not an agent set on getting around it (it
// could write a script that pushes); the git check catches local changes
// however they were made. Pushing needs credentials the agent does not have
// (the sandbox hides SSH keys and the SSH agent; a token comes only as a
// secret you grant), and branch protection on the remote is the real guard.
package danger

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/devark28/xhaos/app/agent"
	"github.com/devark28/xhaos/app/apperr"
	"github.com/devark28/xhaos/app/sandbox"
)

// How long a session may last.
const (
	MinDuration = 10 * time.Second
	MaxDuration = 7 * 24 * time.Hour
)

// MaxCalls is the highest call limit a session may set (or NoCallLimit).
const MaxCalls = 1000

var durationPart = regexp.MustCompile(`(?i)(\d+)\s*(s|m|h|d)`)

// ParseDuration reads <n><unit> parts: 10s, 15m, 2h, 1d, or combined (1h30m).
// Units are s, m, h and d; the total must be 10 s to 7 days.
func ParseDuration(text string) (time.Duration, error) {
	return ParseDurationBetween(text, MinDuration, MaxDuration)
}

// ParseDurationBetween reads the same <n><unit> text, but accepts totals from
// min to max (published channel links use their own range).
func ParseDurationBetween(text string, min, max time.Duration) (time.Duration, error) {
	text = strings.TrimSpace(text)
	parts := durationPart.FindAllStringSubmatchIndex(text, -1)
	if text == "" || len(parts) == 0 || strings.TrimSpace(durationPart.ReplaceAllString(text, "")) != "" {
		return 0, fmt.Errorf("%w: %q is not a duration: write a number and a unit, like 10s, 15m, 2h or 1d", apperr.Invalid, text)
	}
	var total time.Duration
	for _, p := range parts {
		n, err := strconv.Atoi(text[p[2]:p[3]])
		if err != nil || n > 100000 {
			return 0, fmt.Errorf("%w: %q is too long", apperr.Invalid, text)
		}
		unit := map[string]time.Duration{"s": time.Second, "m": time.Minute, "h": time.Hour, "d": 24 * time.Hour}[strings.ToLower(text[p[4]:p[5]])]
		total += time.Duration(n) * unit
	}
	if total < min || total > max {
		return 0, fmt.Errorf("%w: choose between %s and %s (%s given)", apperr.Invalid, spell(min), spell(max), total)
	}
	return total, nil
}

// spell writes a bound the way the dialogs do: 10 seconds, 10 minutes, 7 days.
func spell(d time.Duration) string {
	for _, u := range []struct {
		d    time.Duration
		name string
	}{{24 * time.Hour, "day"}, {time.Hour, "hour"}, {time.Minute, "minute"}, {time.Second, "second"}} {
		if d%u.d == 0 {
			n := int(d / u.d)
			if n == 1 {
				return "1 " + u.name
			}
			return fmt.Sprintf("%d %ss", n, u.name)
		}
	}
	return d.String()
}

// Protected branches are never pushed to, and never the locked branch.
func Protected(branch string) bool {
	switch branch {
	case "main", "master", "trunk", "develop", "production":
		return true
	}
	return strings.HasPrefix(branch, "release")
}

var (
	gitPush   = regexp.MustCompile(`\bgit\b[^;&|\n]*?\bpush\b([^;&|\n]*)`)
	gitBranch = regexp.MustCompile(`\bgit\b[^;&|\n]*?\bbranch\b([^;&|\n]*)`)
)

// CheckCommand returns why a command would do what dangerously allow cannot
// undo afterwards (a push to a protected branch, a force or delete push, a
// push that does not name its branch, deleting or renaming branches), or "".
// Local changes are guarded by Capture and Repair instead.
func CheckCommand(command string) string {
	for _, m := range gitBranch.FindAllStringSubmatch(command, -1) {
		for _, a := range strings.Fields(m[1]) {
			if a == "-d" || a == "-D" || a == "--delete" || a == "-m" || a == "-M" || a == "--move" || a == "-f" || a == "--force" {
				return "deleting, renaming or forcing branches is refused while dangerously allowed"
			}
		}
	}
	for _, m := range gitPush.FindAllStringSubmatch(command, -1) {
		var plain []string
		for _, a := range strings.Fields(m[1]) {
			switch {
			case a == "-f" || strings.HasPrefix(a, "--force") || a == "--delete" || a == "-d" || a == "--mirror" || a == "--all" || a == "--prune" || a == "--tags":
				return fmt.Sprintf("git push %s is refused while dangerously allowed: push one branch by name, without force", a)
			case strings.HasPrefix(a, "-"):
			default:
				plain = append(plain, a)
			}
		}
		// plain: [remote] [refspec…]; the branch must be named, so the
		// destination is known before anything leaves this machine.
		if len(plain) < 2 {
			return "name the branch to push while dangerously allowed: git push origin <branch>"
		}
		for _, ref := range plain[1:] {
			if strings.HasPrefix(ref, "+") || strings.HasPrefix(ref, ":") {
				return fmt.Sprintf("force or delete refspec %q is refused while dangerously allowed", ref)
			}
			dst := ref
			if _, after, ok := strings.Cut(ref, ":"); ok {
				dst = after
			}
			dst = strings.TrimPrefix(dst, "refs/heads/")
			if dst == "HEAD" || dst == "@" {
				return "name the branch to push while dangerously allowed, not HEAD: git push origin <branch>"
			}
			if Protected(dst) {
				return fmt.Sprintf("pushing to %s is refused while dangerously allowed: it is a protected branch", dst)
			}
		}
	}
	return ""
}

// CurrentBranch reads the checked-out branch from .git/HEAD, without running
// git (a project's git config could run commands). "" when root is not a
// git repository; an error when HEAD is detached.
func CurrentBranch(root string) (string, error) {
	dirs := sandbox.GitDirs(root) // .git, or where a submodule's .git file points
	if len(dirs) == 0 {
		return "", nil
	}
	head, err := os.ReadFile(filepath.Join(dirs[0], "HEAD"))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	ref, ok := strings.CutPrefix(strings.TrimSpace(string(head)), "ref: refs/heads/")
	if !ok {
		return "", errors.New("the project's git HEAD is detached; check out a branch first")
	}
	return ref, nil
}

// Service turns dangerously allow on and off (bound to the page).
type Service struct {
	store *agent.Store
	get   func(ws string, id int64) (agent.Agent, error)
	// Verify checks a workspace's passphrase; nil skips it (tests).
	Verify func(workspaceID, pass string) error
	// Root is the agent's project folder; Sandbox runs git there.
	Root    func(a agent.Agent) (string, error)
	Sandbox sandbox.Config
	now     func() time.Time
}

func NewService(store *agent.Store, get func(ws string, id int64) (agent.Agent, error)) *Service {
	return &Service{store: store, get: get, now: time.Now}
}

type Request struct {
	WorkspaceID string `json:"workspaceId"`
	AgentID     int64  `json:"agentId"`
	Passphrase  string `json:"passphrase"`
	// Duration is how long: <n><unit> parts such as 15m, 1h, 1d or 1h30m.
	Duration string `json:"duration"`
	// Minutes is the older form of Duration, used when Duration is empty.
	Minutes int `json:"minutes,omitempty"`
	// Calls is the session's limit on model calls in a row: 0 keeps the
	// normal limit, agent.NoCallLimit lifts it, else 1 to MaxCalls.
	Calls int  `json:"calls,omitempty"`
	Off   bool `json:"off"`
}

// Set turns dangerously allow on for Minutes (after checking the
// passphrase, and moving off a protected branch), or off.
func (s *Service) Set(req Request) (agent.Agent, error) {
	a, err := s.get(req.WorkspaceID, req.AgentID)
	if err != nil {
		return agent.Agent{}, err
	}
	if req.Off {
		if err := s.store.SetDanger(req.WorkspaceID, a.ID, nil); err != nil {
			return agent.Agent{}, err
		}
		return s.get(req.WorkspaceID, a.ID)
	}
	if a.Status == agent.StatusTerminated {
		return agent.Agent{}, fmt.Errorf("%w: agent #%d is terminated", apperr.Invalid, a.ID)
	}
	text := req.Duration
	if text == "" && req.Minutes > 0 {
		text = strconv.Itoa(req.Minutes) + "m"
	}
	length, err := ParseDuration(text)
	if err != nil {
		return agent.Agent{}, err
	}
	if req.Calls != 0 && req.Calls != agent.NoCallLimit && (req.Calls < 1 || req.Calls > MaxCalls) {
		return agent.Agent{}, fmt.Errorf("%w: the call limit is 1 to %d, or none", apperr.Invalid, MaxCalls)
	}
	if s.Verify != nil {
		if err := s.Verify(req.WorkspaceID, req.Passphrase); err != nil {
			return agent.Agent{}, fmt.Errorf("%w: dangerously allow needs your passphrase: %v", apperr.Invalid, err)
		}
	}
	branch := ""
	if s.Root != nil {
		root, err := s.Root(a)
		if err != nil {
			return agent.Agent{}, err
		}
		// Recorded, not changed: the checkout stays on its branch (Repair
		// switches it back if a command moves it). A detached checkout is
		// guarded by its commit, so it records no branch.
		branch, _ = CurrentBranch(root)
	}
	d := &agent.Danger{Until: s.now().Add(length), Branch: branch, Calls: req.Calls}
	if err := s.store.SetDanger(req.WorkspaceID, a.ID, d); err != nil {
		return agent.Agent{}, err
	}
	return s.get(req.WorkspaceID, a.ID)
}

// git runs a git command in root inside the sandbox (a project's git config
// can run programs, so never as the server itself) and returns its output.
func (s *Service) git(root, command string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd, err := s.Sandbox.Command(ctx, root, command)
	if err != nil {
		return "", err
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// Guard is what dangerously allow keeps as it was in a project: the branch
// its checkout is on, and where each protected branch points.
type Guard struct {
	Git       bool              // a git project (else nothing is guarded)
	Head      string            // "refs/heads/main", or a commit when detached
	Protected map[string]string // protected branch → commit
}

// safeRef is a branch name that can go into a command line unquoted.
var safeRef = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

// Capture records root's guarded state, before a command runs.
func (s *Service) Capture(root string) (Guard, error) {
	if dirs := sandbox.GitDirs(root); len(dirs) == 0 {
		return Guard{}, nil
	}
	out, err := s.git(root, "git symbolic-ref -q HEAD || git rev-parse HEAD; echo ---; git for-each-ref --format='%(refname:short) %(objectname)' refs/heads")
	if err != nil {
		return Guard{}, err
	}
	head, refs, _ := strings.Cut(out, "---\n")
	g := Guard{Git: true, Head: strings.TrimSpace(head), Protected: map[string]string{}}
	for _, line := range strings.Split(refs, "\n") {
		name, commit, ok := strings.Cut(strings.TrimSpace(line), " ")
		if ok && Protected(name) {
			g.Protected[name] = commit
		}
	}
	return g, nil
}

// Repair puts root back as before was: protected branches to where they
// pointed (a new one removed, a deleted one restored), and the checkout to
// its branch. It returns what it undid, for the agent, or "".
func (s *Service) Repair(root string, before Guard) string {
	if !before.Git {
		return ""
	}
	after, err := s.Capture(root)
	if err != nil {
		return "Dangerously allow could not check git after this command: " + err.Error()
	}
	var notes []string
	names := map[string]bool{}
	for n := range before.Protected {
		names[n] = true
	}
	for n := range after.Protected {
		names[n] = true
	}
	for name := range names {
		was, now := before.Protected[name], after.Protected[name]
		if was == now {
			continue
		}
		if !safeRef.MatchString(name) {
			notes = append(notes, fmt.Sprintf("protected branch %q changed and could not be put back automatically: tell the owner", name))
			continue
		}
		var cmd, done string
		switch {
		case was == "":
			cmd, done = "git update-ref -d refs/heads/"+name+" "+now, fmt.Sprintf("removed the new protected branch %s", name)
		case now == "":
			cmd, done = "git update-ref refs/heads/"+name+" "+was, fmt.Sprintf("restored the deleted protected branch %s at %.7s", name, was)
		default:
			cmd, done = "git update-ref refs/heads/"+name+" "+was+" "+now, fmt.Sprintf("put %s back at %.7s (it had moved to %.7s; those commits are still in git reflog)", name, was, now)
		}
		if _, err := s.git(root, cmd); err != nil {
			notes = append(notes, fmt.Sprintf("could not undo the change to %s: %v", name, err))
			continue
		}
		notes = append(notes, done)
	}
	if after.Head != before.Head {
		branch, ok := strings.CutPrefix(before.Head, "refs/heads/")
		switch {
		case !ok || !safeRef.MatchString(branch):
			notes = append(notes, "the project's checkout moved off "+before.Head+" and could not be switched back automatically: tell the owner")
		default:
			if _, err := s.git(root, "git switch -q "+branch); err != nil {
				notes = append(notes, fmt.Sprintf("the project's checkout moved off %s and switching it back failed (%v): tell the owner", branch, err))
			} else {
				notes = append(notes, "switched the project's checkout back to "+branch+" (work on your own branch in a worktree under .worktrees/)")
			}
		}
	}
	if len(notes) == 0 {
		return ""
	}
	sort.Strings(notes)
	return "Dangerously allow undid what this command did to protected git state: " + strings.Join(notes, "; ") + "."
}
