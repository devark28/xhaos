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
//	-classifier-timeout  the limit for one answer (default 10 s).
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

	"github.com/devark28/xhaos/app/danger"
	"github.com/devark28/xhaos/app/policy"
)

// By is the ToolData.By of calls the classifier allowed.
const By = "classifier"

// ReasonPrefix starts the ToolData.Reason of calls the classifier allowed.
const ReasonPrefix = "allowed by the local classifier: "

// logFile is the developer log, in Config.Debug.
const logFile = "log.jsonl"

// Config says where the model is.
type Config struct {
	URL     string        // llama-server's base URL; "" turns the model off (the built-in list still applies)
	Timeout time.Duration // limit for one answer; 0 is 10 s
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

// Eligible reports whether the model may be asked about c: limits hold, and
// the command runs no npx-style runner (those are the list's job alone, see
// runner.go). Otherwise the policy's verdict stands.
func Eligible(c Call) bool {
	return limits(c) && !hasRunner(c.Command)
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

// Decide asks the model about an eligible call (see Eligible) and returns
// whether it may run without the owner, with the reason. It returns false
// when the classifier is off or not configured; failures return false with
// the reason. Answers are cached per project and command; failures are not.
func (c *Classifier) Decide(ctx context.Context, r Request) (allow bool, reason string) {
	if c == nil || c.client == nil {
		return false, ""
	}
	if on, err := c.on(); err != nil || !on {
		return false, ""
	}
	if FloorIn(r.Project, r.Command) != "" || hasRunner(r.Command) { // Eligible checked it; checked again so Decide alone never passes the floor or sees a runner
		return false, ""
	}
	started := c.now()
	key := cacheKey{r.Project, r.Command}
	c.mu.Lock()
	v, hit := c.cache.get(key)
	c.mu.Unlock()
	if !hit {
		var err error
		v.allow, v.reason, err = c.client.Ask(ctx, r.Command)
		if err != nil {
			v.allow = false
			log.Printf("WARN classifier: %v", err)
		} else {
			c.mu.Lock()
			c.cache.put(key, v)
			c.mu.Unlock()
		}
	}
	c.record(r, v, c.now().Sub(started), hit)
	return v.allow, v.reason
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
