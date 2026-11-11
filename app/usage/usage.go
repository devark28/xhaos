// Package usage counts the tokens every model call spends, per agent, model,
// and purpose (an agent's turn, a compaction summary, a title, /btw), and
// prices them. Prices are per million tokens: Anthropic's published prices
// are built in, and the user sets or overrides any model's in Settings.
// Cost is computed when read, so a price set later applies to past use too.
package usage

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/devark28/xhaos/app/apperr"
	"github.com/devark28/xhaos/app/llm"
	"github.com/devark28/xhaos/app/sqlitedb"
)

// Purposes of a model call.
const (
	Turn       = "turn"
	Compaction = "compaction"
	Title      = "title"
	Btw        = "btw"
)

const schema = `
CREATE TABLE IF NOT EXISTS usage (
	workspace_id TEXT NOT NULL,
	agent_id     INTEGER NOT NULL, -- 0: not an agent's (/btw)
	provider     TEXT NOT NULL,    -- provider config ID
	kind         TEXT NOT NULL,    -- provider kind (anthropic, openai, sim)
	model        TEXT NOT NULL,
	purpose      TEXT NOT NULL,
	calls        INTEGER NOT NULL DEFAULT 0,
	input        INTEGER NOT NULL DEFAULT 0, -- whole prompts, cached parts included
	cache_read   INTEGER NOT NULL DEFAULT 0,
	cache_write  INTEGER NOT NULL DEFAULT 0,
	output       INTEGER NOT NULL DEFAULT 0,
	updated_at   TEXT NOT NULL,
	PRIMARY KEY (workspace_id, agent_id, provider, model, purpose)
);
CREATE TABLE IF NOT EXISTS prices (
	model       TEXT PRIMARY KEY,
	input       REAL NOT NULL, -- USD per million tokens
	output      REAL NOT NULL,
	cache_read  REAL NOT NULL,
	cache_write REAL NOT NULL
);`

func Migrate(db *sql.DB) error { _, err := db.Exec(schema); return err }

// Price is USD per million tokens. Above LongAfter prompt tokens (when set),
// the Long prices apply instead.
type Price struct {
	Model      string  `json:"model"`
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
	LongAfter  int64   `json:"longAfter,omitempty"`
	LongInput  float64 `json:"longInput,omitempty"`
	LongOutput float64 `json:"longOutput,omitempty"`
	Source     string  `json:"source"` // "built-in" or "yours"
}

// builtIn: Anthropic's published prices (cache writes at 1.25x input for the
// 5-minute cache, reads as listed or 0.1x input).
var builtIn = map[string]Price{
	"claude-fable-5-1":  {Input: 10, Output: 50, CacheRead: 0.25, CacheWrite: 12.5},
	"claude-mythos-5-1": {Input: 10, Output: 50, CacheRead: 0.25, CacheWrite: 12.5},
	"claude-fable-5":    {Input: 10, Output: 50, CacheRead: 1, CacheWrite: 12.5},
	"claude-opus-5-5":   {Input: 4, Output: 20, CacheRead: 0.20, CacheWrite: 5},
	"claude-opus-5":     {Input: 5, Output: 25, CacheRead: 0.5, CacheWrite: 6.25},
	"claude-sonnet-5-5": {Input: 2, Output: 10, CacheRead: 0.20, CacheWrite: 2.5},
	"claude-haiku-5-5":  {Input: 0.10, Output: 0.50, CacheRead: 0.01, CacheWrite: 0.125, LongAfter: 100_000, LongInput: 0.50, LongOutput: 2.50},
}

// Recorder writes usage rows.
type Recorder struct {
	db  *sql.DB
	now func() time.Time
}

func NewRecorder(db *sql.DB) *Recorder { return &Recorder{db: db, now: time.Now} }

// Record adds one call's tokens. Errors are returned for logging only: a
// failed count must never fail the call it counts.
func (r *Recorder) Record(workspaceID string, agentID int64, provider, kind, model, purpose string, res llm.Result) error {
	if r == nil || res.InputTokens+res.OutputTokens == 0 {
		return nil
	}
	_, err := r.db.Exec(`INSERT INTO usage (workspace_id, agent_id, provider, kind, model, purpose, calls, input, cache_read, cache_write, output, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?)
		ON CONFLICT (workspace_id, agent_id, provider, model, purpose) DO UPDATE SET
			calls = calls + 1, input = input + excluded.input, cache_read = cache_read + excluded.cache_read,
			cache_write = cache_write + excluded.cache_write, output = output + excluded.output, updated_at = excluded.updated_at`,
		workspaceID, agentID, provider, kind, model, purpose, res.InputTokens, res.CacheReadTokens, res.CacheWriteTokens, res.OutputTokens, sqlitedb.Stamp(r.now()))
	return err
}

// Tokens is a set of counts with their cost (nil when a model has no price).
type Tokens struct {
	Calls      int64    `json:"calls"`
	Input      int64    `json:"input"`
	CacheRead  int64    `json:"cacheRead"`
	CacheWrite int64    `json:"cacheWrite"`
	Output     int64    `json:"output"`
	Cost       *float64 `json:"cost,omitempty"`
}

func (t *Tokens) add(o Tokens) {
	t.Calls += o.Calls
	t.Input += o.Input
	t.CacheRead += o.CacheRead
	t.CacheWrite += o.CacheWrite
	t.Output += o.Output
	if o.Cost != nil {
		c := *o.Cost
		if t.Cost != nil {
			c += *t.Cost
		}
		t.Cost = &c
	}
}

// ModelUsage is one model's use, with the price it was costed at.
type ModelUsage struct {
	Provider string           `json:"provider"`
	Kind     string           `json:"kind"`
	Model    string           `json:"model"`
	Price    *Price           `json:"price,omitempty"`
	Tokens   Tokens           `json:"tokens"`
	Purposes map[string]int64 `json:"purposes"` // calls per purpose
}

type AgentUsage struct {
	AgentID int64  `json:"agentId"`
	Tokens  Tokens `json:"tokens"`
}

// Report is a workspace's usage: the total, per model, and per agent.
type Report struct {
	Total    Tokens       `json:"total"`
	Unpriced int64        `json:"unpriced"` // tokens of models without a price
	Models   []ModelUsage `json:"models"`
	Agents   []AgentUsage `json:"agents"`
}

// cost prices counts. Cached tokens are part of Input; the rest of the
// prompt is charged at the input price. A tiered price is chosen by the
// average prompt per call (an estimate when calls straddle the threshold).
func cost(p Price, t Tokens) float64 {
	in, out := p.Input, p.Output
	if p.LongAfter > 0 && t.Calls > 0 && t.Input/t.Calls > p.LongAfter {
		in, out = p.LongInput, p.LongOutput
	}
	fresh := max(t.Input-t.CacheRead-t.CacheWrite, 0)
	return (float64(fresh)*in + float64(t.CacheRead)*p.CacheRead + float64(t.CacheWrite)*p.CacheWrite + float64(t.Output)*out) / 1e6
}

// Service is the client-facing API.
type Service struct{ db *sql.DB }

func NewService(db *sql.DB) *Service { return &Service{db: db} }

// PriceOf returns model's price (the user's, else built in), or nil.
func (s *Service) PriceOf(model string) *Price { return s.price(model) }

// price returns the user's price for model, else the built-in one.
func (s *Service) price(model string) *Price {
	var p Price
	err := s.db.QueryRow(`SELECT model, input, output, cache_read, cache_write FROM prices WHERE model = ?`, model).Scan(&p.Model, &p.Input, &p.Output, &p.CacheRead, &p.CacheWrite)
	if err == nil {
		p.Source = "yours"
		return &p
	}
	if b, ok := builtIn[model]; ok {
		b.Model, b.Source = model, "built-in"
		return &b
	}
	return nil
}

// Usage reports the workspace's token use and cost.
func (s *Service) Usage(workspaceID string) (Report, error) {
	rows, err := s.db.Query(`SELECT agent_id, provider, kind, model, purpose, calls, input, cache_read, cache_write, output FROM usage WHERE workspace_id = ?`, workspaceID)
	if err != nil {
		return Report{}, err
	}
	type row struct {
		agent                 int64
		provider, kind, model string
		purpose               string
		t                     Tokens
	}
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.agent, &r.provider, &r.kind, &r.model, &r.purpose, &r.t.Calls, &r.t.Input, &r.t.CacheRead, &r.t.CacheWrite, &r.t.Output); err != nil {
			rows.Close()
			return Report{}, err
		}
		all = append(all, r)
	}
	rows.Close()
	rep := Report{Models: []ModelUsage{}, Agents: []AgentUsage{}}
	models := map[string]*ModelUsage{}
	agents := map[int64]*AgentUsage{}
	prices := map[string]*Price{}
	for _, r := range all {
		p, ok := prices[r.model]
		if !ok {
			p = s.price(r.model)
			prices[r.model] = p
		}
		if r.kind == "claude-cli" {
			p = nil // a subscription is flat-rate: API prices would show money that is never charged
		}
		t := r.t
		if p != nil {
			c := cost(*p, t)
			t.Cost = &c
		} else if r.kind != "sim" && r.kind != "claude-cli" {
			rep.Unpriced += t.Input + t.Output
		}
		key := r.provider + "/" + r.model
		m := models[key]
		if m == nil {
			m = &ModelUsage{Provider: r.provider, Kind: r.kind, Model: r.model, Price: p, Purposes: map[string]int64{}}
			models[key] = m
		}
		m.Tokens.add(t)
		m.Purposes[r.purpose] += t.Calls
		if r.agent != 0 {
			a := agents[r.agent]
			if a == nil {
				a = &AgentUsage{AgentID: r.agent}
				agents[r.agent] = a
			}
			a.Tokens.add(t)
		}
		rep.Total.add(t)
	}
	for _, m := range models {
		rep.Models = append(rep.Models, *m)
	}
	sort.Slice(rep.Models, func(i, j int) bool {
		return rep.Models[i].Tokens.Input+rep.Models[i].Tokens.Output > rep.Models[j].Tokens.Input+rep.Models[j].Tokens.Output
	})
	for _, a := range agents {
		rep.Agents = append(rep.Agents, *a)
	}
	sort.Slice(rep.Agents, func(i, j int) bool { return rep.Agents[i].AgentID < rep.Agents[j].AgentID })
	return rep, nil
}

// Prices lists the built-in prices with the user's on top.
func (s *Service) Prices() ([]Price, error) {
	seen := map[string]bool{}
	out := []Price{}
	rows, err := s.db.Query(`SELECT model FROM prices ORDER BY model`)
	if err != nil {
		return nil, err
	}
	var mine []string
	for rows.Next() {
		var m string
		rows.Scan(&m)
		mine = append(mine, m)
	}
	rows.Close()
	for _, m := range mine {
		out = append(out, *s.price(m))
		seen[m] = true
	}
	var names []string
	for m := range builtIn {
		if !seen[m] {
			names = append(names, m)
		}
	}
	sort.Strings(names)
	for _, m := range names {
		out = append(out, *s.price(m))
	}
	return out, nil
}

// SetPrice sets a model's prices (USD per million tokens). Cache prices
// left at zero default to 0.1x (reads) and 1.25x (writes) of input.
func (s *Service) SetPrice(p Price) (Price, error) {
	p.Model = strings.TrimSpace(p.Model)
	if p.Model == "" || p.Input < 0 || p.Output < 0 || p.CacheRead < 0 || p.CacheWrite < 0 {
		return Price{}, fmt.Errorf("%w: a price needs a model and non-negative amounts", apperr.Invalid)
	}
	if p.CacheRead == 0 {
		p.CacheRead = p.Input / 10
	}
	if p.CacheWrite == 0 {
		p.CacheWrite = p.Input * 1.25
	}
	_, err := s.db.Exec(`INSERT INTO prices (model, input, output, cache_read, cache_write) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (model) DO UPDATE SET input = excluded.input, output = excluded.output, cache_read = excluded.cache_read, cache_write = excluded.cache_write`,
		p.Model, p.Input, p.Output, p.CacheRead, p.CacheWrite)
	if err != nil {
		return Price{}, err
	}
	return *s.price(p.Model), nil
}

// ResetPrice removes the user's price (the built-in one applies again).
func (s *Service) ResetPrice(model string) error {
	_, err := s.db.Exec(`DELETE FROM prices WHERE model = ?`, model)
	return err
}
