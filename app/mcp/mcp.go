// Package mcp connects agents to MCP servers: local commands (stdio) or
// remote URLs (Streamable HTTP). The owner configures servers per workspace;
// the server keeps one session per configured server and offers its tools to
// agents as "mcp__<server>__<tool>". Every MCP call goes through the same
// policy gate as the built-in tools: a server's trust setting decides the
// effect its calls have (see Effect).
package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"xhaos/app/apperr"
	"xhaos/app/llm"
	"xhaos/app/secretbox"
	"xhaos/app/sqlitedb"
	"xhaos/app/tool"
	"xhaos/app/workspace"
)

// Trust levels, from most to least cautious.
const (
	TrustAsk  = "ask"  // every call waits for the owner
	TrustAuto = "auto" // runs under auto review, asks under manual (like a file write)
	TrustRead = "read" // read-only server: calls run at once
)

// Effect maps a server's trust to the policy effect of its calls.
func Effect(trust string) string {
	switch trust {
	case TrustRead:
		return tool.Read
	case TrustAuto:
		return tool.Write
	}
	return tool.Exec
}

// Server is a configured MCP server and, when known, its live state.
type Server struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspaceId"`
	Name        string    `json:"name"`
	Transport   string    `json:"transport"` // "stdio" | "http"
	Command     string    `json:"command,omitempty"`
	Args        []string  `json:"args,omitempty"`
	URL         string    `json:"url,omitempty"`
	EnvKeys     []string  `json:"envKeys,omitempty"`    // names only; values are never sent back
	HeaderKeys  []string  `json:"headerKeys,omitempty"` // names only
	Trust       string    `json:"trust"`
	Enabled     bool      `json:"enabled"`
	UpdatedAt   time.Time `json:"updatedAt"`

	Status string     `json:"status"` // "connected" | "connecting" | "error" | "disabled"
	Error  string     `json:"error,omitempty"`
	Tools  []ToolInfo `json:"tools"`
}

type ToolInfo struct {
	Name        string `json:"name"`        // as the server names it
	AgentName   string `json:"agentName"`   // as agents call it
	Description string `json:"description"` // the server's description
	ReadOnly    bool   `json:"readOnly"`    // the server's (untrusted) hint
}

type secrets struct {
	Env     map[string]string `json:"env,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

const schema = `
CREATE TABLE IF NOT EXISTS mcp_servers (
	workspace_id TEXT NOT NULL REFERENCES workspaces(id),
	id           TEXT NOT NULL,
	name         TEXT NOT NULL,
	transport    TEXT NOT NULL,
	command      TEXT NOT NULL DEFAULT '',
	args         TEXT NOT NULL DEFAULT '[]',
	url          TEXT NOT NULL DEFAULT '',
	secrets      TEXT NOT NULL DEFAULT '',
	env_keys     TEXT NOT NULL DEFAULT '[]',
	header_keys  TEXT NOT NULL DEFAULT '[]',
	trust        TEXT NOT NULL,
	enabled      INTEGER NOT NULL,
	updated_at   TEXT NOT NULL,
	PRIMARY KEY (workspace_id, id)
);`

func Migrate(db *sql.DB) error {
	_, err := db.Exec(schema)
	return err
}

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,23}$`)

const selectServer = `SELECT workspace_id, id, name, transport, command, args, url, secrets, env_keys, header_keys, trust, enabled, updated_at FROM mcp_servers`

func scan(r interface{ Scan(...any) error }) (Server, string, error) {
	var s Server
	var args, sealed, envKeys, headerKeys, at string
	var enabled int
	if err := r.Scan(&s.WorkspaceID, &s.ID, &s.Name, &s.Transport, &s.Command, &args, &s.URL, &sealed, &envKeys, &headerKeys, &s.Trust, &enabled, &at); err != nil {
		return s, "", err
	}
	_ = json.Unmarshal([]byte(args), &s.Args)
	_ = json.Unmarshal([]byte(envKeys), &s.EnvKeys)
	_ = json.Unmarshal([]byte(headerKeys), &s.HeaderKeys)
	s.Enabled, s.UpdatedAt = enabled == 1, sqlitedb.ParseStamp(at)
	return s, sealed, nil
}

// ---- connections -------------------------------------------------------------

type key struct{ ws, id string }

type conn struct {
	session *sdk.ClientSession
	tools   []*sdk.Tool
	status  string
	err     string
}

// Manager holds the live sessions. It is the runner's source of MCP tools.
type Manager struct {
	db     *sql.DB
	box    *secretbox.Box
	client *sdk.Client
	mu     sync.Mutex
	conns  map[key]*conn
	// connectTimeout bounds starting a server and listing its tools.
	connectTimeout time.Duration
}

func NewManager(db *sql.DB, box *secretbox.Box, version string) *Manager {
	return &Manager{
		db:             db,
		box:            box,
		client:         sdk.NewClient(&sdk.Implementation{Name: "xhaos", Version: version}, nil),
		conns:          map[key]*conn{},
		connectTimeout: 30 * time.Second,
	}
}

// Start connects every enabled server in the background.
func (m *Manager) Start() {
	rows, err := m.db.Query(selectServer + ` WHERE enabled = 1`)
	if err != nil {
		slog.Error("mcp: list servers", "err", err)
		return
	}
	var servers []Server
	for rows.Next() {
		if s, _, err := scan(rows); err == nil {
			servers = append(servers, s)
		}
	}
	rows.Close()
	for _, s := range servers {
		go m.connect(s.WorkspaceID, s.ID)
	}
}

// Close ends every session (and stops local server processes).
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, c := range m.conns {
		if c.session != nil {
			c.session.Close()
		}
		delete(m.conns, k)
	}
}

func (m *Manager) load(ws, id string) (Server, secrets, error) {
	s, sealed, err := scan(m.db.QueryRow(selectServer+` WHERE workspace_id = ? AND id = ?`, ws, id))
	if errors.Is(err, sql.ErrNoRows) {
		return s, secrets{}, fmt.Errorf("%w: MCP server %q", apperr.NotFound, id)
	}
	if err != nil {
		return s, secrets{}, err
	}
	var sec secrets
	if sealed != "" {
		plain, err := m.box.Unseal(sealed)
		if err != nil {
			return s, sec, err
		}
		_ = json.Unmarshal([]byte(plain), &sec)
	}
	return s, sec, nil
}

// headerTransport adds configured headers (e.g. Authorization) to requests.
type headerTransport struct {
	headers map[string]string
	base    http.RoundTripper
}

func (t headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range t.headers {
		r.Header.Set(k, v)
	}
	return t.base.RoundTrip(r)
}

// connect (re)starts a server's session and lists its tools. It blocks up to
// connectTimeout and records the outcome in the server's state.
func (m *Manager) connect(ws, id string) {
	k := key{ws, id}
	m.mu.Lock()
	if old := m.conns[k]; old != nil && old.session != nil {
		old.session.Close()
	}
	m.conns[k] = &conn{status: "connecting"}
	m.mu.Unlock()

	s, sec, err := m.load(ws, id)
	set := func(c *conn) {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.conns[k] = c
	}
	if err != nil {
		set(&conn{status: "error", err: err.Error()})
		return
	}
	if !s.Enabled {
		set(&conn{status: "disabled"})
		return
	}
	var transport sdk.Transport
	switch s.Transport {
	case "stdio":
		cmd := exec.Command(s.Command, s.Args...)
		cmd.Env = os.Environ()
		for k, v := range sec.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		transport = &sdk.CommandTransport{Command: cmd}
	case "http":
		client := &http.Client{Transport: headerTransport{headers: sec.Headers, base: http.DefaultTransport}}
		transport = &sdk.StreamableClientTransport{Endpoint: s.URL, HTTPClient: client}
	default:
		set(&conn{status: "error", err: "unknown transport " + s.Transport})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), m.connectTimeout)
	defer cancel()
	session, err := m.client.Connect(ctx, transport, nil)
	if err != nil {
		set(&conn{status: "error", err: err.Error()})
		return
	}
	var tools []*sdk.Tool
	for t, err := range session.Tools(ctx, nil) {
		if err != nil {
			session.Close()
			set(&conn{status: "error", err: "list tools: " + err.Error()})
			return
		}
		tools = append(tools, t)
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	set(&conn{session: session, tools: tools, status: "connected"})
}

func (m *Manager) state(ws, id string) conn {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c := m.conns[key{ws, id}]; c != nil {
		return *c
	}
	return conn{status: "disabled"}
}

// ---- tools for agents ----------------------------------------------------------

const prefix = "mcp__"

var unsafeName = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// AgentName is the tool name agents use: model APIs allow [a-zA-Z0-9_-]{1,64}.
func AgentName(serverID, toolName string) string {
	name := prefix + serverID + "__" + unsafeName.ReplaceAllString(toolName, "_")
	if len(name) > 64 {
		name = name[:64]
	}
	return name
}

// Owns reports whether a tool name belongs to an MCP server.
func (m *Manager) Owns(name string) bool { return strings.HasPrefix(name, prefix) }

type entry struct {
	server Server
	tool   *sdk.Tool
	conn   conn
}

// tools lists a workspace's connected tools by agent name.
func (m *Manager) tools(ws string) map[string]entry {
	rows, err := m.db.Query(selectServer+` WHERE workspace_id = ? AND enabled = 1 ORDER BY id`, ws)
	if err != nil {
		return nil
	}
	var servers []Server
	for rows.Next() {
		if s, _, err := scan(rows); err == nil {
			servers = append(servers, s)
		}
	}
	rows.Close()
	out := map[string]entry{}
	for _, s := range servers {
		c := m.state(ws, s.ID)
		if c.status != "connected" {
			continue
		}
		for _, t := range c.tools {
			out[AgentName(s.ID, t.Name)] = entry{server: s, tool: t, conn: c}
		}
	}
	return out
}

// Specs lists the MCP tools agents in a workspace can call, sorted by name.
func (m *Manager) Specs(ws string) []llm.Tool {
	all := m.tools(ws)
	names := make([]string, 0, len(all))
	for n := range all {
		names = append(names, n)
	}
	sort.Strings(names)
	specs := make([]llm.Tool, 0, len(names))
	for _, n := range names {
		e := all[n]
		var schema struct {
			Properties map[string]any `json:"properties"`
			Required   []string       `json:"required"`
		}
		raw, _ := json.Marshal(e.tool.InputSchema)
		_ = json.Unmarshal(raw, &schema)
		desc := fmt.Sprintf("[MCP server %s] %s", e.server.Name, e.tool.Description)
		specs = append(specs, llm.Tool{Name: n, Description: strings.TrimSpace(desc), Schema: llm.ToolSchema{Properties: schema.Properties, Required: schema.Required}})
	}
	return specs
}

// Prepare checks an MCP call: the tool must exist now, and its input must be
// a JSON object. The server's trust sets the call's effect for the gate.
func (m *Manager) Prepare(ws string, c llm.ToolCall) (tool.Call, error) {
	call := tool.Call{ID: c.ID, Name: c.Name, Effect: tool.Exec}
	e, ok := m.tools(ws)[c.Name]
	if !ok {
		return call, fmt.Errorf("MCP tool %s is not available (its server may be disconnected or disabled)", c.Name)
	}
	var args map[string]any
	if err := json.Unmarshal(c.Input, &args); err != nil {
		return call, fmt.Errorf("invalid input for %s: must be a JSON object", c.Name)
	}
	call.Effect = Effect(e.server.Trust)
	call.Args = c.Input
	compact, _ := json.Marshal(args)
	call.Summary = fmt.Sprintf("%s · %s %s", e.server.Name, e.tool.Name, compact)
	pretty, _ := json.MarshalIndent(args, "", "  ")
	call.Preview = string(pretty)
	return call, nil
}

// Execute calls the tool. A failed session is reconnected once.
func (m *Manager) Execute(ctx context.Context, ws string, call tool.Call) (string, error) {
	e, ok := m.tools(ws)[call.Name]
	if !ok {
		return "", fmt.Errorf("MCP tool %s is not available", call.Name)
	}
	var args map[string]any
	_ = json.Unmarshal(call.Args, &args)
	res, err := e.conn.session.CallTool(ctx, &sdk.CallToolParams{Name: e.tool.Name, Arguments: args})
	if err != nil && ctx.Err() == nil {
		m.connect(ws, e.server.ID)
		if c := m.state(ws, e.server.ID); c.status == "connected" {
			res, err = c.session.CallTool(ctx, &sdk.CallToolParams{Name: e.tool.Name, Arguments: args})
		}
	}
	if err != nil {
		return "", err
	}
	out := tool.Clip(content(res))
	if res.IsError {
		return out, errors.New(firstLine(out))
	}
	return out, nil
}

// content renders a result for the model: text as is, other kinds noted.
func content(res *sdk.CallToolResult) string {
	var parts []string
	for _, c := range res.Content {
		switch v := c.(type) {
		case *sdk.TextContent:
			parts = append(parts, v.Text)
		case *sdk.EmbeddedResource:
			if v.Resource != nil && v.Resource.Text != "" {
				parts = append(parts, v.Resource.Text)
			} else if v.Resource != nil {
				parts = append(parts, "[resource "+v.Resource.URI+"]")
			}
		case *sdk.ResourceLink:
			parts = append(parts, "[resource link "+v.URI+"]")
		case *sdk.ImageContent:
			parts = append(parts, "[image ("+v.MIMEType+") not shown]")
		case *sdk.AudioContent:
			parts = append(parts, "[audio ("+v.MIMEType+") not shown]")
		}
	}
	if len(parts) == 0 && res.StructuredContent != nil {
		raw, _ := json.MarshalIndent(res.StructuredContent, "", "  ")
		parts = append(parts, string(raw))
	}
	if len(parts) == 0 {
		return "(no output)"
	}
	return strings.Join(parts, "\n")
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return "the tool reported an error"
	}
	return s
}

// ---- client-facing service ---------------------------------------------------

type Service struct{ m *Manager }

func NewService(m *Manager) *Service { return &Service{m: m} }

func (s *Service) withState(srv Server) Server {
	c := s.m.state(srv.WorkspaceID, srv.ID)
	srv.Status, srv.Error, srv.Tools = c.status, c.err, []ToolInfo{}
	if !srv.Enabled {
		srv.Status = "disabled"
	}
	for _, t := range c.tools {
		info := ToolInfo{Name: t.Name, AgentName: AgentName(srv.ID, t.Name), Description: t.Description}
		if t.Annotations != nil {
			info.ReadOnly = t.Annotations.ReadOnlyHint
		}
		srv.Tools = append(srv.Tools, info)
	}
	return srv
}

// Servers lists a workspace's MCP servers with their live state.
func (s *Service) Servers(workspaceID string) ([]Server, error) {
	rows, err := s.m.db.Query(selectServer+` WHERE workspace_id = ? ORDER BY id`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Server{}
	for rows.Next() {
		srv, _, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s.withState(srv))
	}
	return out, rows.Err()
}

type SaveRequest struct {
	WorkspaceID string            `json:"workspaceId"`
	ID          string            `json:"id"`
	Name        string            `json:"name,omitempty"`
	Transport   string            `json:"transport"`
	Command     string            `json:"command,omitempty"`
	Args        []string          `json:"args,omitempty"`
	URL         string            `json:"url,omitempty"`
	Env         map[string]string `json:"env,omitempty"`     // nil keeps the saved values
	Headers     map[string]string `json:"headers,omitempty"` // nil keeps the saved values
	Trust       string            `json:"trust"`
	Enabled     bool              `json:"enabled"`
}

// Save creates or updates a server, then (re)connects it and returns its
// state. Connecting waits up to 30 seconds; a failure is reported in the
// returned state, not as an error, so the config is kept for fixing.
func (s *Service) Save(req SaveRequest) (Server, error) {
	req.ID, req.Transport = strings.TrimSpace(req.ID), strings.TrimSpace(req.Transport)
	if _, err := workspace.Get(s.m.db, req.WorkspaceID); err != nil {
		return Server{}, err
	}
	if !idPattern.MatchString(req.ID) {
		return Server{}, fmt.Errorf("%w: id must be up to 24 lowercase letters, digits, and dashes", apperr.Invalid)
	}
	switch req.Transport {
	case "stdio":
		if req.Command = strings.TrimSpace(req.Command); req.Command == "" {
			return Server{}, fmt.Errorf("%w: a local server needs a command", apperr.Invalid)
		}
		req.URL = ""
	case "http":
		if !strings.HasPrefix(req.URL, "http://") && !strings.HasPrefix(req.URL, "https://") {
			return Server{}, fmt.Errorf("%w: a remote server needs an http(s) URL", apperr.Invalid)
		}
		req.Command, req.Args = "", nil
	default:
		return Server{}, fmt.Errorf("%w: transport must be stdio or http", apperr.Invalid)
	}
	switch req.Trust {
	case TrustAsk, TrustAuto, TrustRead:
	case "":
		req.Trust = TrustAsk
	default:
		return Server{}, fmt.Errorf("%w: trust must be ask, auto, or read", apperr.Invalid)
	}
	if req.Name = strings.TrimSpace(req.Name); req.Name == "" {
		req.Name = req.ID
	}
	// Secrets: a nil map keeps what is stored.
	_, old, err := s.m.load(req.WorkspaceID, req.ID)
	if err != nil && !errors.Is(err, apperr.NotFound) {
		return Server{}, err
	}
	sec := old
	if req.Env != nil {
		sec.Env = req.Env
	}
	if req.Headers != nil {
		sec.Headers = req.Headers
	}
	sealed := ""
	if len(sec.Env) > 0 || len(sec.Headers) > 0 {
		plain, _ := json.Marshal(sec)
		if sealed, err = s.m.box.Seal(string(plain)); err != nil {
			return Server{}, err
		}
	}
	args, _ := json.Marshal(nonNil(req.Args))
	envKeys, _ := json.Marshal(keys(sec.Env))
	headerKeys, _ := json.Marshal(keys(sec.Headers))
	enabled := 0
	if req.Enabled {
		enabled = 1
	}
	_, err = s.m.db.Exec(`INSERT INTO mcp_servers (workspace_id, id, name, transport, command, args, url, secrets, env_keys, header_keys, trust, enabled, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(workspace_id, id) DO UPDATE SET name = excluded.name, transport = excluded.transport, command = excluded.command,
			args = excluded.args, url = excluded.url, secrets = excluded.secrets, env_keys = excluded.env_keys, header_keys = excluded.header_keys,
			trust = excluded.trust, enabled = excluded.enabled, updated_at = excluded.updated_at`,
		req.WorkspaceID, req.ID, req.Name, req.Transport, req.Command, string(args), strings.TrimSpace(req.URL), sealed,
		string(envKeys), string(headerKeys), req.Trust, enabled, sqlitedb.Stamp(time.Now()))
	if err != nil {
		return Server{}, err
	}
	s.m.connect(req.WorkspaceID, req.ID)
	srv, _, err := s.m.load(req.WorkspaceID, req.ID)
	return s.withState(srv), err
}

type ServerRef struct {
	WorkspaceID string `json:"workspaceId"`
	ID          string `json:"id"`
}

// Reconnect restarts a server's session and returns its state.
func (s *Service) Reconnect(ref ServerRef) (Server, error) {
	srv, _, err := s.m.load(ref.WorkspaceID, ref.ID)
	if err != nil {
		return Server{}, err
	}
	s.m.connect(ref.WorkspaceID, ref.ID)
	return s.withState(srv), nil
}

// Delete stops and removes a server.
func (s *Service) Delete(ref ServerRef) error {
	s.m.mu.Lock()
	if c := s.m.conns[key{ref.WorkspaceID, ref.ID}]; c != nil && c.session != nil {
		c.session.Close()
	}
	delete(s.m.conns, key{ref.WorkspaceID, ref.ID})
	s.m.mu.Unlock()
	_, err := s.m.db.Exec(`DELETE FROM mcp_servers WHERE workspace_id = ? AND id = ?`, ref.WorkspaceID, ref.ID)
	return err
}

func keys(m map[string]string) []string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
