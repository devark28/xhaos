// Package classifier allows ordinary commands (builds, tests, linters) that
// would otherwise wait for the owner: first a fixed built-in list of exact
// commands (Listed, Allowed; no model), then, for the long tail, a small local
// model when one is configured.
//
// The built-in list follows the same server-wide switch as the model and
// needs no model server. It passes the same checks as the model (Eligible)
// before the list is consulted.
//
// The model runs in llama.cpp's llama-server, its own service on 127.0.0.1
// with the model loaded once; the server asks it through the
// OpenAI-compatible API (Client). It can only make the gate less strict where
// the policy would ask, never more permissive than that:
//
//   - it is consulted only for a bash call the policy would ask about: an
//     exec command by a read-write agent under auto review, not a commit or
//     push, without secrets, not web or contact, that dangerously allow's
//     check passes (Eligible);
//   - the deny floor (Floor) refuses the rest of the dangerous list before
//     the model is asked, whatever it would say;
//   - any failure (timeout, error, malformed or unknown answer) means ask.
//
// Configuration (cmd/xhaos flags, passed as Config):
//
//	-classifier-url      llama-server's base URL; empty turns the model off (the list stays).
//	-classifier-timeout  the limit for one answer (default 15 s).
//
// The owner turns the list and the model on or off together, server-wide (on
// by default), and can keep a developer log of every decision (off by default).
package classifier

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/devark28/xhaos/app/danger"
	"github.com/devark28/xhaos/app/policy"
)

// By is the ToolData.By of calls the classifier allowed.
const By = "classifier"

// ReasonPrefix starts the ToolData.Reason of calls the classifier allowed.
const ReasonPrefix = "allowed by the local classifier: "

// FallbackReason is the ToolData.Reason of a call the classifier allowed when
// the model gives no reason for it.
const FallbackReason = "allowed by the local model"

// logFile is the developer log, in Config.Debug.
const logFile = "log.jsonl"

// Config says where the model is.
type Config struct {
	URL     string        // llama-server's base URL; "" turns the model off (the built-in list still applies)
	Timeout time.Duration // limit for one answer; 0 is 15 s
	Debug   string        // folder for the developer log (beside the database); "" disables it
}

// Call is what the eligibility check needs to know about a tool call.
type Call struct {
	Name    string        // the tool; only "bash" is classified
	Effect  string        // policy.Effect*
	Commit  bool          // a git commit or push
	Command string        // the command line
	Policy  policy.Policy // the agent's policy
	Root    string        // the project's folder: npx finds its tools here
}

// limits reports whether c passes every hard limit and the deny floor. Otherwise
// the policy's verdict stands.
func limits(c Call) bool {
	return c.Name == "bash" &&
		c.Effect == policy.EffectExec && // not read, write, git, secret, net or contact
		c.Policy.Access == "rw" &&
		c.Policy.Review == "auto" &&
		!c.Commit &&
		danger.CheckCommand(c.Command) == "" &&
		FloorIn(c.Root, c.Command) == ""
}

// ManualHint ends the reason of a command that asks because its agent is on
// manual review, when the classifier is on and would have been consulted
// under auto review (OnlyManual). The owner sees it once per agent.
const ManualHint = "the classifier only decides for agents on auto review — switch this agent to auto to let it decide"

// OnlyManual reports whether manual review alone keeps c from the
// classifier: the same call by the agent on auto review would pass every
// hard limit and the deny floor.
func OnlyManual(c Call) bool {
	if c.Policy.Review != "manual" {
		return false
	}
	c.Policy.Review = "auto"
	return limits(c)
}

// On reports the server-wide switch (false on a nil Classifier or an error).
func (c *Classifier) On() bool {
	if c == nil {
		return false
	}
	on, err := c.on()
	return err == nil && on
}

// ModelMaxChars is the longest command the model is asked about. A longer one
// asks without the model: the system prompt is cached, but the command's own
// tokens process at about 30 a second, so a long one cannot finish within the
// answer's limit (a 2,300-token command took about 73 s).
const ModelMaxChars = 1500

// Eligible reports whether the model may be asked about c: limits hold, the
// command is at most ModelMaxChars long, and it runs no npx-style runner
// (those are the list's job alone, see runner.go). Otherwise the policy's
// verdict stands.
func Eligible(c Call) bool {
	return utf8.RuneCountInString(c.Command) <= ModelMaxChars && limits(c) && !hasRunner(c.Command)
}

// Allowed reports whether c is allowed by the built-in list with no model:
// limits hold (hard limits, dangerously allow's check, the deny floor) and the
// command is listed in the project c.Root. It returns the listed names.
func Allowed(c Call) (name string, ok bool) {
	if !limits(c) {
		return "", false
	}
	return ListedIn(c.Root, c.Command)
}

// List reports whether the built-in list allows call (see Allowed) while the
// classifier is on. It needs no model server, and writes the decision to the
// developer log (reason "built-in list: <name>"); nothing is cached.
func (c *Classifier) List(r Request, call Call) (name string, ok bool) {
	if c == nil {
		return "", false
	}
	if on, err := c.on(); err != nil || !on {
		return "", false
	}
	name, ok = Allowed(call)
	if ok {
		c.record(r, verdict{allow: true, reason: "built-in list: " + name}, 0, false)
	}
	return name, ok
}

// Classifier decides eligible calls with the model. It is used by the runner;
// the page reaches its settings through Service (Owner).
type Classifier struct {
	db     *sql.DB
	cfg    Config
	client *Client // nil when no URL is set
	now    func() time.Time

	mu    sync.Mutex
	cache *verdictCache // successful answers, for the server's lifetime (at most maxCached)

	logMu sync.Mutex // one developer log line at a time
}

type cacheKey struct{ project, command string }

type verdict struct {
	allow  bool
	reason string
}

// New returns the classifier. db holds its settings (see Migrate).
func New(db *sql.DB, cfg Config) *Classifier {
	c := &Classifier{db: db, cfg: cfg, now: time.Now, cache: newVerdictCache(maxCached)}
	if cfg.URL != "" {
		c.client = NewClient(cfg.URL, cfg.Timeout)
	}
	return c
}

// Migrate creates the settings table (server-wide key/value pairs).
func Migrate(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS classifier_settings (
		key   TEXT NOT NULL PRIMARY KEY,
		value TEXT NOT NULL
	)`)
	return err
}

// Request is one eligible call to decide.
type Request struct {
	Workspace string
	Agent     int64
	Project   string // the project's root: verdicts are cached per project
	Command   string
}

// Decide asks the model about an eligible call (see Eligible). It returns
// whether the call may run without the owner, and the text to show for it:
// for an ask (or a failure, which asks) the reason, or "" when there is none;
// for an allow, ReasonPrefix and the model's reason when it is already known,
// or "" when it is not. An allow with "" is explained in the background
// (see Explain). Verdicts are cached per project and command; failures are not.
func (c *Classifier) Decide(ctx context.Context, r Request) (allow bool, text string) {
	if c == nil || c.client == nil {
		return false, ""
	}
	if on, err := c.on(); err != nil || !on {
		return false, ""
	}
	// Eligible checked these; checked again so Decide alone never passes the
	// floor, sees a runner, or asks about a command too long to answer in time.
	if FloorIn(r.Project, r.Command) != "" || hasRunner(r.Command) || utf8.RuneCountInString(r.Command) > ModelMaxChars {
		return false, ""
	}
	model, ok := modelText(r.Command)
	if !ok {
		return false, "the command has a shell comment or a quoted string over 200 characters, which the classifier does not read"
	}
	started := c.now()
	key := cacheKey{r.Project, r.Command}
	c.mu.Lock()
	v, hit := c.cache.get(key)
	c.mu.Unlock()
	if !hit {
		yes, why, err := c.client.Ask(ctx, model)
		if err != nil {
			log.Printf("WARN classifier: %v", err)
			c.record(r, verdict{reason: why}, c.now().Sub(started), false)
			return false, why
		}
		v = verdict{allow: yes}
		c.mu.Lock()
		c.cache.put(key, v)
		c.mu.Unlock()
	}
	c.record(r, v, c.now().Sub(started), hit)
	switch {
	case !v.allow:
		return false, ""
	case v.reason != "":
		return true, ReasonPrefix + v.reason
	}
	return true, ""
}

// Warm makes the model's warm-up request (see Client.Warm), so the first
// command does not wait for the model to load. It does nothing with no model.
func (c *Classifier) Warm(ctx context.Context) error {
	if c == nil || c.client == nil {
		return nil
	}
	return c.client.Warm(ctx)
}

// Explain returns the text to show for an allowed call whose reason Decide
// did not have: ReasonPrefix and the model's reason, or FallbackReason when
// the model cannot give one. It asks the model once per command and project;
// the answer is kept with the verdict. It runs in the background, after the
// call was recorded as allowed.
func (c *Classifier) Explain(ctx context.Context, r Request) string {
	if c == nil || c.client == nil {
		return FallbackReason
	}
	model, ok := modelText(r.Command)
	if !ok {
		return FallbackReason
	}
	key := cacheKey{r.Project, r.Command}
	c.mu.Lock()
	v, hit := c.cache.get(key)
	c.mu.Unlock()
	if !hit || !v.allow {
		return FallbackReason
	}
	if v.reason != "" {
		return ReasonPrefix + v.reason
	}
	started := c.now()
	why, err := c.client.Explain(ctx, model)
	if err != nil {
		log.Printf("WARN classifier: %v", err)
		return FallbackReason
	}
	if why == "" {
		why = "an ordinary project command"
	}
	v.reason = why
	c.mu.Lock()
	c.cache.put(key, v)
	c.mu.Unlock()
	c.record(r, v, c.now().Sub(started), true)
	return ReasonPrefix + why
}

// entry is one line of the developer log.
type entry struct {
	Time      string `json:"time"`
	Workspace string `json:"workspace"`
	Agent     int64  `json:"agent"`
	Project   string `json:"project"`
	Command   string `json:"command"`
	Verdict   string `json:"verdict"`
	Reason    string `json:"reason"`
	LatencyMS int64  `json:"latency_ms"`
	CacheHit  bool   `json:"cache_hit"`
}

// record appends the decision to the developer log when it is on. A failed
// write only goes to the server log.
func (c *Classifier) record(r Request, v verdict, took time.Duration, hit bool) {
	if c.cfg.Debug == "" {
		return
	}
	if on, err := c.developer(); err != nil || !on {
		return
	}
	e := entry{Time: c.now().UTC().Format(time.RFC3339Nano), Workspace: r.Workspace, Agent: r.Agent, Project: r.Project,
		Command: r.Command, Verdict: "ask", Reason: v.reason, LatencyMS: took.Milliseconds(), CacheHit: hit}
	if v.allow {
		e.Verdict = "allow"
	}
	line, err := json.Marshal(e)
	if err != nil {
		log.Printf("WARN classifier: developer log: %v", err)
		return
	}
	c.logMu.Lock()
	defer c.logMu.Unlock()
	if err := appendLine(filepath.Join(c.cfg.Debug, logFile), append(line, '\n')); err != nil {
		log.Printf("WARN classifier: developer log: %v", err)
	}
}

func appendLine(path string, line []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(line)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// on reports the server-wide switch: on unless the owner turned it off.
func (c *Classifier) on() (bool, error) {
	v, err := c.setting("on")
	return v != "off", err
}

// developer reports the developer log switch: off unless turned on.
func (c *Classifier) developer() (bool, error) {
	v, err := c.setting("developer")
	return v == "on", err
}

func (c *Classifier) setting(key string) (string, error) {
	var v string
	err := c.db.QueryRow(`SELECT value FROM classifier_settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (c *Classifier) putSetting(key string, on bool) error {
	v := "off"
	if on {
		v = "on"
	}
	_, err := c.db.Exec(`INSERT INTO classifier_settings (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, v)
	return err
}

// Owner is the page's view: the settings and the health check.
func (c *Classifier) Owner() *Service { return &Service{c: c} }
