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
//   - git is locked to one branch: the one checked out when it was turned on
//     (a new xhaos/… branch when that was main or master). Switching branch,
//     force pushes, deleting remote branches, and pushing anywhere but that
//     branch are refused.
//
// The branch lock reads commands as text: it stops mistakes, not an agent
// set on getting around it (it could write a script that pushes). Pushing
// needs credentials the agent does not have (the sandbox hides SSH keys and
// the SSH agent; a token comes only as a secret you grant), and branch
// protection on the remote is the real guard.
package danger

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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
	if total < MinDuration || total > MaxDuration {
		return 0, fmt.Errorf("%w: choose between 10 seconds and 7 days (%s given)", apperr.Invalid, total)
	}
	return total, nil
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
	gitSwitch = regexp.MustCompile(`\bgit\b[^;&|\n]*?\b(switch|checkout)\b([^;&|\n]*)`)
	gitPush   = regexp.MustCompile(`\bgit\b[^;&|\n]*?\bpush\b([^;&|\n]*)`)
	gitBranch = regexp.MustCompile(`\bgit\b[^;&|\n]*?\bbranch\b([^;&|\n]*)`)
)

// CheckCommand returns why command breaks the branch lock, or "".
func CheckCommand(command, branch string) string {
	if branch == "" {
		return "" // not a git project
	}
	for _, m := range gitSwitch.FindAllStringSubmatch(command, -1) {
		args := strings.Fields(m[2])
		if len(args) == 0 {
			continue
		}
		if args[0] == "--" || (m[1] == "checkout" && slices.Contains(args, "--")) {
			continue // checking out files, not a branch
		}
		target := ""
		for _, a := range args {
			if a == "-c" || a == "-C" || a == "-b" || a == "-B" || a == "--create" || a == "--orphan" || a == "--detach" {
				return fmt.Sprintf("git is locked to branch %s while dangerously allowed: no new or other branches", branch)
			}
			if !strings.HasPrefix(a, "-") && target == "" {
				target = a
			}
		}
		if target != "" && target != branch {
			if m[1] == "checkout" {
				return fmt.Sprintf("git is locked to branch %s while dangerously allowed; to restore files use git restore (or git checkout -- <file>)", branch)
			}
			return fmt.Sprintf("git is locked to branch %s while dangerously allowed", branch)
		}
	}
	for _, m := range gitBranch.FindAllStringSubmatch(command, -1) {
		for _, a := range strings.Fields(m[1]) {
			if a == "-d" || a == "-D" || a == "--delete" || a == "-m" || a == "-M" || a == "--move" || a == "-f" || a == "--force" {
				return fmt.Sprintf("deleting, renaming or forcing branches is refused while dangerously allowed (locked to %s)", branch)
			}
		}
	}
	for _, m := range gitPush.FindAllStringSubmatch(command, -1) {
		var plain []string
		for _, a := range strings.Fields(m[1]) {
			switch {
			case a == "-f" || strings.HasPrefix(a, "--force") || a == "--delete" || a == "-d" || a == "--mirror" || a == "--all" || a == "--prune" || a == "--tags":
				return fmt.Sprintf("git push %s is refused while dangerously allowed: push only branch %s, without force", a, branch)
			case strings.HasPrefix(a, "-"):
			default:
				plain = append(plain, a)
			}
		}
		// plain: [remote] [refspec…]
		if len(plain) > 1 {
			for _, ref := range plain[1:] {
				if strings.HasPrefix(ref, "+") || strings.HasPrefix(ref, ":") {
					return fmt.Sprintf("force or delete refspec %q is refused while dangerously allowed", ref)
				}
				dst := ref
				if _, after, ok := strings.Cut(ref, ":"); ok {
					dst = after
				}
				dst = strings.TrimPrefix(dst, "refs/heads/")
				if dst == "HEAD" {
					dst = branch
				}
				if dst != branch {
					return fmt.Sprintf("pushing to %s is refused while dangerously allowed: only %s", dst, branch)
				}
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
		if branch, err = CurrentBranch(root); err != nil {
			return agent.Agent{}, fmt.Errorf("%w: %v", apperr.Invalid, err)
		}
		if Protected(branch) {
			next := fmt.Sprintf("xhaos/%d-%s", a.ID, slug(a.Title))
			if err := s.git(root, "git switch -c "+next+" || git switch "+next); err != nil {
				return agent.Agent{}, fmt.Errorf("could not move off %s to a branch of its own (%s): %v", branch, next, err)
			}
			branch = next
		}
	}
	d := &agent.Danger{Until: s.now().Add(length), Branch: branch, Calls: req.Calls}
	if err := s.store.SetDanger(req.WorkspaceID, a.ID, d); err != nil {
		return agent.Agent{}, err
	}
	return s.get(req.WorkspaceID, a.ID)
}

// git runs a git command in root inside the sandbox (a project's git config
// can run programs, so never as the server itself).
func (s *Service) git(root, command string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd, err := s.Sandbox.Command(ctx, root, command)
	if err != nil {
		return err
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slug(title string) string {
	s := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(title), "-"), "-")
	if len(s) > 30 {
		s = strings.TrimRight(s[:30], "-")
	}
	if s == "" {
		s = "work"
	}
	return s
}
