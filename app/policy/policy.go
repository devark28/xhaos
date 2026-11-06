// Package policy defines per-agent execution policy, its ordering rules, and
// the gate every tool call passes before it has any effect.
package policy

import (
	"fmt"

	"xhaos/app/apperr"
)

// Policy is an agent's execution policy for its project/repository.
type Policy struct {
	Access string `json:"access"` // "ro" | "rw"
	Review string `json:"review"` // "manual" | "auto"
	Commit string `json:"commit"` // "blocked" | "review" | "allowed"
}

// Values, each listed from most to least restrictive.
var (
	accessLevels = []string{"ro", "rw"}
	reviewLevels = []string{"manual", "auto"}
	commitLevels = []string{"blocked", "review", "allowed"}
)

// Default is the dogfooding default: RW + Manual + Review commit.
func Default() Policy { return Policy{Access: "rw", Review: "manual", Commit: "review"} }

func rank(levels []string, v string) int {
	for i, l := range levels {
		if l == v {
			return i
		}
	}
	return -1
}

// Validate reports whether every field holds a known value.
func (p Policy) Validate() error {
	if rank(accessLevels, p.Access) < 0 || rank(reviewLevels, p.Review) < 0 || rank(commitLevels, p.Commit) < 0 {
		return fmt.Errorf("%w: unknown policy %+v", apperr.Invalid, p)
	}
	return nil
}

// Within reports whether p grants nothing beyond limit. A parent can restrict
// a child but never grant it more than its own policy.
func (p Policy) Within(limit Policy) bool {
	return rank(accessLevels, p.Access) <= rank(accessLevels, limit.Access) &&
		rank(reviewLevels, p.Review) <= rank(reviewLevels, limit.Review) &&
		rank(commitLevels, p.Commit) <= rank(commitLevels, limit.Commit)
}

// Verdicts of the gate.
const (
	Allow = "allow" // run now
	Ask   = "ask"   // wait for the owner's decision
	Deny  = "deny"  // refuse; Reason says why
)

// Effects a tool call can have (see package tool).
const (
	EffectRead  = "read"  // reads in the project (files, or read-only commands)
	EffectWrite = "write" // file changes in the project
	EffectGit   = "git"   // local git changes: add, commit, switch
	EffectExec  = "exec"  // any other command
	// EffectContact opens a channel to agents outside the caller's family
	// (another top-level agent and its subagents): it always waits for the
	// owner, whatever the policy, so agents never start talking unseen.
	EffectContact = "contact"
	// EffectSecret is a command given secrets (package secret): it always
	// asks, whatever the agent's policy, unless the owner trusted every
	// secret it uses for this agent (then it is gated as a plain command).
	EffectSecret = "secret"
	// EffectNet reaches the web (web_fetch, web_search): nothing changes
	// here, but a URL can carry data out and a page can carry instructions
	// in. It runs under auto review, read-only agents included, and asks
	// under manual review.
	EffectNet = "net"
)

// Gate decides what happens to a tool call with the given effect. commit
// marks a git commit or push, which the Commit field governs on its own.
//
// Reads always run. Under auto review, file writes and local git changes
// run without asking; any other command always waits for the owner, so an
// agent never runs arbitrary programs (or pushes) unnoticed.
func (p Policy) Gate(effect string, commit bool) (verdict, reason string) {
	if effect == EffectRead {
		return Allow, ""
	}
	if effect == EffectContact {
		return Ask, "opening a channel to agents outside its own family needs your approval"
	}
	if effect == EffectNet {
		if p.Review == "auto" {
			return Allow, ""
		}
		return Ask, "web access needs review"
	}
	if effect == EffectSecret {
		return Ask, "commands that use secrets need your approval"
	}
	if p.Access != "rw" {
		return Deny, "this agent is read-only"
	}
	if commit {
		switch p.Commit {
		case "blocked":
			return Deny, "commits and pushes are blocked for this agent"
		case "review":
			return Ask, "commits and pushes need review"
		}
	}
	switch {
	case effect == EffectExec:
		return Ask, "only read-only commands and local git changes run without you"
	case p.Review == "auto":
		return Allow, ""
	}
	return Ask, "changes need review"
}
