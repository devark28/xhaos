// Package secret is the workspace's secrets vault: named values (API tokens,
// a GitHub PAT…) encrypted with the data key that the owner's passphrase
// unlocks, like provider keys.
//
// Agents never see a value. secrets_list shows names, descriptions and
// hosts; a bash call names the secrets it needs, and the runner gives them
// to that one command as environment variables inside a sealed environment
// (package sandbox: read-only project, throwaway home, network only through
// the egress proxy to the secret's hosts), then scrubs the values out of
// its output. A secret reaches an agent only after the owner grants it to
// that agent, and every command using it waits for approval unless the
// owner trusted it to that agent (which takes the passphrase).
package secret

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"xhaos/app/agent"
	"xhaos/app/apperr"
	"xhaos/app/egress"
	"xhaos/app/eventlog"
	"xhaos/app/llm"
	"xhaos/app/secretbox"
	"xhaos/app/sqlitedb"
	"xhaos/app/tool"
)

const EventChanged = "secret.changed" // data: {"name": …}; never a value

const schema = `
CREATE TABLE IF NOT EXISTS secrets (
	workspace_id TEXT NOT NULL REFERENCES workspaces(id),
	name         TEXT NOT NULL,
	description  TEXT NOT NULL DEFAULT '',
	value        TEXT NOT NULL, -- sealed with the data key
	hosts        TEXT NOT NULL DEFAULT '[]',
	created_at   TEXT NOT NULL,
	updated_at   TEXT NOT NULL,
	PRIMARY KEY (workspace_id, name)
);
CREATE TABLE IF NOT EXISTS secret_grants (
	workspace_id TEXT NOT NULL,
	name         TEXT NOT NULL,
	agent_id     INTEGER NOT NULL REFERENCES agents(id),
	mode         TEXT NOT NULL, -- ask | trusted
	PRIMARY KEY (workspace_id, name, agent_id),
	FOREIGN KEY (workspace_id, name) REFERENCES secrets(workspace_id, name) ON DELETE CASCADE
);`

func Migrate(db *sql.DB) error {
	if _, err := db.Exec(schema); err != nil {
		return err
	}
	for _, col := range []string{`kind TEXT NOT NULL DEFAULT 'token'`, `known_hosts TEXT NOT NULL DEFAULT ''`} {
		if _, err := db.Exec(`ALTER TABLE secrets ADD COLUMN ` + col); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return err
		}
	}
	return nil
}

// Kinds of secret.
const (
	// KindToken is a value given to the command as $NAME (an API token, a
	// password); its hosts are reached over HTTPS (and HTTP) through the proxy.
	KindToken = "token"
	// KindSSH is a private key: the command gets $NAME as the key file's
	// path and an ssh set up to use it through the proxy, to its hosts only
	// ([user@]host[:port], port 22 by default).
	KindSSH = "ssh"
)

// Grant modes.
const (
	Ask     = "ask"     // each command using it waits for approval
	Trusted = "trusted" // commands using it are gated like any command
)

// View is a secret as the page sees it: never the value.
type View struct {
	Name        string    `json:"name"`
	Kind        string    `json:"kind"`
	Description string    `json:"description"`
	Hosts       []string  `json:"hosts"`
	Grants      []Grant   `json:"grants"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type Grant struct {
	AgentID int64  `json:"agentId"`
	Mode    string `json:"mode"`
}

// Service is the owner's API (bound to the page). Agent-side use goes
// through Tools and Sealer, which take the acting agent.
type Service struct {
	db      *sql.DB
	box     *secretbox.Box
	publish eventlog.Publisher
	// Verify checks the owner's passphrase (auth.Vault.Verify); nil skips
	// the check (tests without a passphrase).
	Verify func(pass string) error
	now    func() time.Time
}

func NewService(db *sql.DB, box *secretbox.Box, publish eventlog.Publisher) *Service {
	if publish == nil {
		publish = func(eventlog.Event) {}
	}
	return &Service{db: db, box: box, publish: publish, now: time.Now}
}

var (
	nameRE = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,63}$`)
	hostRE = regexp.MustCompile(`^(\*\.)?([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,62}$`)
	// Names the sealed environment sets itself, or that change how programs
	// load code or reach the network.
	reserved = []string{"PATH", "HOME", "TMPDIR", "SHELL", "USER", "LOGNAME", "LANG", "TERM", "PWD", "IFS", "ENV", "BASH_ENV",
		"LD_PRELOAD", "LD_LIBRARY_PATH", "LD_AUDIT", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "GOCACHE", "GOMODCACHE",
		"GOFLAGS", "GOPROXY", "XDG_CACHE_HOME", "PYTHONPATH", "NODE_OPTIONS", "GIT_CONFIG_GLOBAL", "GIT_SSH_COMMAND", "SSH_AUTH_SOCK"}
)

func validName(name string) error {
	if !nameRE.MatchString(name) {
		return fmt.Errorf("%w: a secret's name is an environment variable name: capitals, digits and _ (like GITHUB_TOKEN)", apperr.Invalid)
	}
	if slices.Contains(reserved, name) || strings.HasPrefix(name, "XHAOS_") {
		return fmt.Errorf("%w: %s is reserved; choose another name", apperr.Invalid, name)
	}
	return nil
}

// sshHost splits an SSH host entry: [user@]host[:port].
func sshHost(entry string) (user, host string, port int, err error) {
	rest := entry
	if u, h, ok := strings.Cut(rest, "@"); ok {
		user, rest = u, h
		if !regexp.MustCompile(`^[a-z_][a-z0-9_.-]{0,31}$`).MatchString(user) {
			return "", "", 0, fmt.Errorf("%q is not a user name", user)
		}
	}
	host, port = rest, 22
	if h, p, err := net.SplitHostPort(rest); err == nil {
		host = h
		if port, err = strconv.Atoi(p); err != nil || port < 1 || port > 65535 {
			return "", "", 0, fmt.Errorf("%q is not a port", p)
		}
	}
	host = strings.Trim(host, "[]")
	if !hostOrIP(host) || strings.HasPrefix(host, "*.") {
		return "", "", 0, fmt.Errorf("%q is not a host name or IP address", host)
	}
	return user, host, port, nil
}

// hostOrIP accepts a host name (or *.name) or an IP address, but not
// loopback or unspecified addresses (that would be the server itself).
func hostOrIP(h string) bool {
	if ip, err := netip.ParseAddr(h); err == nil {
		return !ip.IsLoopback() && !ip.IsUnspecified() && !ip.IsMulticast()
	}
	return hostRE.MatchString(h) && h != "localhost"
}

func cleanHosts(kind string, hosts []string) ([]string, error) {
	out := []string{}
	for _, h := range hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		if kind == KindSSH {
			if _, _, _, err := sshHost(h); err != nil {
				return nil, fmt.Errorf("%w: %v (an SSH host is [user@]host[:port], like git@github.com or deploy@192.168.1.47)", apperr.Invalid, err)
			}
		} else if !hostOrIP(h) {
			return nil, fmt.Errorf("%w: %q is not a host name or IP address (like api.github.com, *.github.com for its subdomains, or 192.168.1.47)", apperr.Invalid, h)
		}
		if !slices.Contains(out, h) {
			out = append(out, h)
		}
	}
	return out, nil
}

// Secrets lists the workspace's secrets, names only, with their grants.
func (s *Service) Secrets(workspaceID string) ([]View, error) {
	rows, err := s.db.Query(`SELECT name, kind, description, hosts, updated_at FROM secrets WHERE workspace_id = ? ORDER BY name`, workspaceID)
	if err != nil {
		return nil, err
	}
	out := []View{}
	for rows.Next() {
		var v View
		var hosts, at string
		if err := rows.Scan(&v.Name, &v.Kind, &v.Description, &hosts, &at); err != nil {
			rows.Close()
			return nil, err
		}
		_ = json.Unmarshal([]byte(hosts), &v.Hosts)
		v.UpdatedAt = sqlitedb.ParseStamp(at)
		v.Grants = []Grant{}
		out = append(out, v)
	}
	rows.Close()
	grants, err := s.db.Query(`SELECT name, agent_id, mode FROM secret_grants WHERE workspace_id = ? ORDER BY agent_id`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer grants.Close()
	for grants.Next() {
		var name string
		var g Grant
		if err := grants.Scan(&name, &g.AgentID, &g.Mode); err != nil {
			return nil, err
		}
		for i := range out {
			if out[i].Name == name {
				out[i].Grants = append(out[i].Grants, g)
			}
		}
	}
	return out, grants.Err()
}

type SaveRequest struct {
	WorkspaceID string   `json:"workspaceId"`
	Name        string   `json:"name"`
	Kind        string   `json:"kind"` // token (default) or ssh; fixed once saved
	Description string   `json:"description"`
	Value       string   `json:"value"` // empty keeps the saved value
	Hosts       []string `json:"hosts"`
}

// Save adds a secret, or updates one (an empty value keeps the old one).
func (s *Service) Save(req SaveRequest) (View, error) {
	name := strings.TrimSpace(req.Name)
	if err := validName(name); err != nil {
		return View{}, err
	}
	kind := req.Kind
	if kind == "" {
		kind = KindToken
	}
	if kind != KindToken && kind != KindSSH {
		return View{}, fmt.Errorf("%w: a secret is a token or an ssh key", apperr.Invalid)
	}
	var saved string
	if err := s.db.QueryRow(`SELECT kind FROM secrets WHERE workspace_id = ? AND name = ?`, req.WorkspaceID, name).Scan(&saved); err == nil && saved != kind {
		return View{}, fmt.Errorf("%w: %s is a %s; delete it to save it as another kind", apperr.Invalid, name, saved)
	}
	if kind == KindSSH && req.Value != "" {
		if !strings.Contains(req.Value, "PRIVATE KEY-----") {
			return View{}, fmt.Errorf("%w: paste the whole private key, from -----BEGIN … PRIVATE KEY----- to the END line", apperr.Invalid)
		}
		if strings.Contains(req.Value, "ENCRYPTED") {
			return View{}, fmt.Errorf("%w: this key has a passphrase; agents cannot type it. Use a key without one (ideally a key just for this)", apperr.Invalid)
		}
		if !strings.HasSuffix(req.Value, "\n") {
			req.Value += "\n" // ssh rejects a key file without its final newline
		}
	}
	hosts, err := cleanHosts(kind, req.Hosts)
	if err != nil {
		return View{}, err
	}
	desc := strings.TrimSpace(req.Description)
	if len(desc) > 500 {
		return View{}, fmt.Errorf("%w: keep the description under 500 characters", apperr.Invalid)
	}
	if len(req.Value) > 64<<10 {
		return View{}, fmt.Errorf("%w: a secret is at most 64 KB", apperr.Invalid)
	}
	hostsJSON, _ := json.Marshal(hosts)
	at := sqlitedb.Stamp(s.now())
	if req.Value == "" {
		res, err := s.db.Exec(`UPDATE secrets SET description = ?, hosts = ?, updated_at = ? WHERE workspace_id = ? AND name = ?`, desc, string(hostsJSON), at, req.WorkspaceID, name)
		if err != nil {
			return View{}, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return View{}, fmt.Errorf("%w: a new secret needs a value", apperr.Invalid)
		}
	} else {
		sealed, err := s.box.Seal(req.Value)
		if err != nil {
			return View{}, err
		}
		if _, err := s.db.Exec(`INSERT INTO secrets (workspace_id, name, kind, description, value, hosts, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (workspace_id, name) DO UPDATE SET description = excluded.description, value = excluded.value, hosts = excluded.hosts, updated_at = excluded.updated_at`,
			req.WorkspaceID, name, kind, desc, sealed, string(hostsJSON), at, at); err != nil {
			return View{}, err
		}
	}
	s.changed(req.WorkspaceID, name)
	return s.view(req.WorkspaceID, name)
}

func (s *Service) view(ws, name string) (View, error) {
	list, err := s.Secrets(ws)
	if err != nil {
		return View{}, err
	}
	for _, v := range list {
		if v.Name == name {
			return v, nil
		}
	}
	return View{}, fmt.Errorf("%w: secret %s", apperr.NotFound, name)
}

// changed records a change (never the value) so other pages refresh.
func (s *Service) changed(ws, name string) {
	tx, err := s.db.Begin()
	if err != nil {
		return
	}
	defer tx.Rollback()
	ev, err := eventlog.Append(tx, ws, 0, EventChanged, map[string]string{"name": name}, s.now())
	if err == nil && tx.Commit() == nil {
		s.publish(ev)
	}
}

type NameRequest struct {
	WorkspaceID string `json:"workspaceId"`
	Name        string `json:"name"`
}

// Delete removes a secret and its grants.
func (s *Service) Delete(req NameRequest) error {
	res, err := s.db.Exec(`DELETE FROM secrets WHERE workspace_id = ? AND name = ?`, req.WorkspaceID, req.Name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: secret %s", apperr.NotFound, req.Name)
	}
	s.changed(req.WorkspaceID, req.Name)
	return nil
}

type RevealRequest struct {
	WorkspaceID string `json:"workspaceId"`
	Name        string `json:"name"`
	Passphrase  string `json:"passphrase"`
}

// Reveal returns a secret's value, after checking the passphrase again.
func (s *Service) Reveal(req RevealRequest) (string, error) {
	if s.Verify != nil {
		if err := s.Verify(req.Passphrase); err != nil {
			return "", fmt.Errorf("%w: %v", apperr.Invalid, err)
		}
	}
	vals, err := s.values(req.WorkspaceID, []string{req.Name})
	if err != nil {
		return "", err
	}
	return vals[req.Name], nil
}

type GrantRequest struct {
	WorkspaceID string `json:"workspaceId"`
	Name        string `json:"name"`
	AgentID     int64  `json:"agentId"`
	Mode        string `json:"mode"`       // ask, trusted, or "" to revoke
	Passphrase  string `json:"passphrase"` // needed to trust
}

// Grant lets an agent use a secret (each use asks), trusts it to the agent
// (gated like any command; needs the passphrase), or revokes it ("").
func (s *Service) Grant(req GrantRequest) (View, error) {
	if _, err := s.view(req.WorkspaceID, req.Name); err != nil {
		return View{}, err
	}
	switch req.Mode {
	case "":
		if _, err := s.db.Exec(`DELETE FROM secret_grants WHERE workspace_id = ? AND name = ? AND agent_id = ?`, req.WorkspaceID, req.Name, req.AgentID); err != nil {
			return View{}, err
		}
	case Ask, Trusted:
		if req.Mode == Trusted && s.Verify != nil {
			if err := s.Verify(req.Passphrase); err != nil {
				return View{}, fmt.Errorf("%w: trusting a secret to an agent needs your passphrase: %v", apperr.Invalid, err)
			}
		}
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM agents WHERE workspace_id = ? AND id = ?`, req.WorkspaceID, req.AgentID).Scan(&n); err != nil || n == 0 {
			return View{}, fmt.Errorf("%w: agent #%d", apperr.NotFound, req.AgentID)
		}
		if _, err := s.db.Exec(`INSERT INTO secret_grants (workspace_id, name, agent_id, mode) VALUES (?, ?, ?, ?)
			ON CONFLICT (workspace_id, name, agent_id) DO UPDATE SET mode = excluded.mode`, req.WorkspaceID, req.Name, req.AgentID, req.Mode); err != nil {
			return View{}, err
		}
	default:
		return View{}, fmt.Errorf("%w: mode is ask, trusted, or empty", apperr.Invalid)
	}
	s.changed(req.WorkspaceID, req.Name)
	return s.view(req.WorkspaceID, req.Name)
}

func (s *Service) values(ws string, names []string) (map[string]string, error) {
	out := map[string]string{}
	for _, name := range names {
		var sealed string
		err := s.db.QueryRow(`SELECT value FROM secrets WHERE workspace_id = ? AND name = ?`, ws, name).Scan(&sealed)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: secret %s", apperr.NotFound, name)
		}
		if err != nil {
			return nil, err
		}
		v, err := s.box.Unseal(sealed)
		if err != nil {
			return nil, fmt.Errorf("cannot open secret %s: %w", name, err)
		}
		out[name] = v
	}
	return out, nil
}

// ---- agent side -------------------------------------------------------------

// Sealer is the runner's way in. It is a function, not a method, so
// binding Service to the page does not expose it.
type Sealer struct{ s *Service }

func NewSealer(s *Service) *Sealer { return &Sealer{s: s} }

// Use describes the secrets a command asks for, as the agent may use them.
type Use struct {
	Preview string        // for the approval card
	Trusted bool          // every one is trusted to the agent
	Rules   []egress.Rule // what the proxy lets the command reach
	Kinds   map[string]string
	// Hosts are each SSH key's host entries ([user@]host[:port]).
	Hosts map[string][]string
}

// Check validates names for agent a: each must exist and be granted to it.
func (x *Sealer) Check(ws string, a agent.Agent, names []string) (Use, error) {
	if len(names) == 0 {
		return Use{}, errors.New("no secrets named")
	}
	list, err := x.s.Secrets(ws)
	if err != nil {
		return Use{}, err
	}
	use := Use{Trusted: true, Kinds: map[string]string{}, Hosts: map[string][]string{}}
	var lines []string
	for _, name := range names {
		i := slices.IndexFunc(list, func(v View) bool { return v.Name == name })
		if i < 0 {
			return Use{}, fmt.Errorf("there is no secret named %s (secrets_list shows them)", name)
		}
		v := list[i]
		g := slices.IndexFunc(v.Grants, func(g Grant) bool { return g.AgentID == a.ID })
		if g < 0 {
			return Use{}, fmt.Errorf("secret %s is not granted to you; ask the user to grant it in Settings → Secrets", name)
		}
		use.Trusted = use.Trusted && v.Grants[g].Mode == Trusted
		use.Kinds[name] = v.Kind
		for _, h := range v.Hosts {
			if v.Kind == KindSSH {
				_, host, port, _ := sshHost(h)
				use.Rules = append(use.Rules, egress.Rule{Pattern: host, Ports: []int{port}})
				use.Hosts[name] = append(use.Hosts[name], h)
			} else {
				use.Rules = append(use.Rules, egress.Web(h))
			}
		}
		where := "no network"
		if len(v.Hosts) > 0 {
			where = "can reach " + strings.Join(v.Hosts, ", ")
		}
		what := "secret"
		if v.Kind == KindSSH {
			what = "SSH key"
		}
		lines = append(lines, fmt.Sprintf("%s %s (%s)", what, name, where))
	}
	use.Preview = "Uses " + strings.Join(lines, "; ") + ".\nThe project is read-only for this command; other hosts, and UDP, are blocked."
	return use, nil
}

// KnownHosts returns the SSH host keys remembered for a key ("" for none).
func (x *Sealer) KnownHosts(ws, name string) string {
	var k string
	_ = x.s.db.QueryRow(`SELECT known_hosts FROM secrets WHERE workspace_id = ? AND name = ?`, ws, name).Scan(&k)
	return k
}

// RememberHosts keeps the host keys an SSH key's command accepted (on first
// connection), so later commands check them.
func (x *Sealer) RememberHosts(ws, name, knownHosts string) {
	if len(knownHosts) > 64<<10 {
		return
	}
	_, _ = x.s.db.Exec(`UPDATE secrets SET known_hosts = ? WHERE workspace_id = ? AND name = ?`, knownHosts, ws, name)
}

// Open returns the values (checked again: a grant may be revoked while
// the call waited).
func (x *Sealer) Open(ws string, a agent.Agent, names []string) (map[string]string, Use, error) {
	use, err := x.Check(ws, a, names)
	if err != nil {
		return nil, Use{}, err
	}
	vals, err := x.s.values(ws, names)
	return vals, use, err
}

// Scrub replaces every value (and its base64 and URL-encoded forms) in text
// with [secret:NAME].
func Scrub(text string, values map[string]string) string {
	type pair struct{ from, to string }
	var pairs []pair
	for name, v := range values {
		if len(v) < 4 {
			continue
		}
		mark := "[secret:" + name + "]"
		// A key printed in part (head, grep) still hides each line.
		if strings.Contains(v, "\n") {
			for _, line := range strings.Split(v, "\n") {
				if line = strings.TrimSpace(line); len(line) >= 16 && !strings.HasPrefix(line, "-----") {
					pairs = append(pairs, pair{line, mark})
				}
			}
		}
		for _, form := range []string{v, base64.StdEncoding.EncodeToString([]byte(v)), base64.RawStdEncoding.EncodeToString([]byte(v)),
			base64.URLEncoding.EncodeToString([]byte(v)), base64.RawURLEncoding.EncodeToString([]byte(v)), url.QueryEscape(v)} {
			pairs = append(pairs, pair{form, mark})
		}
	}
	sort.Slice(pairs, func(i, j int) bool { return len(pairs[i].from) > len(pairs[j].from) })
	for _, p := range pairs {
		text = strings.ReplaceAll(text, p.from, p.to)
	}
	return text
}

// Tools is secrets_list, for agents (a function, like NewSealer).
type Tools struct{ s *Service }

func NewTools(s *Service) *Tools { return &Tools{s: s} }

func (t *Tools) Owns(name string) bool { return name == "secrets_list" }

func (t *Tools) Specs(string) []llm.Tool {
	return []llm.Tool{{Name: "secrets_list",
		Description: "List the workspace's secrets (names, what they are for, which hosts they can reach, and whether you may use them). You never see a value: to use secrets, pass their names in bash's `secrets` and refer to them as environment variables ($NAME) in the command.",
		Schema:      llm.ToolSchema{Properties: map[string]any{}}}}
}

func (t *Tools) Catalog() []tool.Info {
	var out []tool.Info
	for _, s := range t.Specs("") {
		out = append(out, tool.Info{Name: s.Name, Description: s.Description, Effect: tool.Read, Source: "built-in"})
	}
	return out
}

func (t *Tools) Prepare(_ string, _ agent.Agent, c llm.ToolCall) (tool.Call, error) {
	return tool.Call{ID: c.ID, Name: c.Name, Effect: tool.Read, Summary: "secrets", Args: c.Input}, nil
}

func (t *Tools) Execute(_ context.Context, ws string, a agent.Agent, _ tool.Call) (string, error) {
	list, err := t.s.Secrets(ws)
	if err != nil {
		return "", err
	}
	if len(list) == 0 {
		return "There are no secrets in this workspace. The user adds them in Settings → Secrets.", nil
	}
	var b strings.Builder
	for _, v := range list {
		access := "not granted to you (ask the user to grant it in Settings → Secrets)"
		if i := slices.IndexFunc(v.Grants, func(g Grant) bool { return g.AgentID == a.ID }); i >= 0 {
			access = "granted; each command using it waits for the user's approval"
			if v.Grants[i].Mode == Trusted {
				access = "granted and trusted; commands using it are gated like any command"
			}
		}
		hosts := "no network"
		if len(v.Hosts) > 0 {
			hosts = strings.Join(v.Hosts, ", ")
		}
		kind := "token ($" + v.Name + " is the value)"
		if v.Kind == KindSSH {
			kind = "SSH key: run plain ssh/scp/sftp or git over ssh to its hosts ($" + v.Name + " is the key file's path; your own ~/.ssh is not available)"
		}
		fmt.Fprintf(&b, "%s: %s\n  kind: %s\n  hosts: %s\n  you: %s\n", v.Name, firstNonEmpty(v.Description, "(no description)"), kind, hosts, access)
	}
	b.WriteString("\nTo use one: bash with `secrets: [\"NAME\"]`, and $NAME in the command (e.g. curl -H \"Authorization: Bearer $NAME\" https://host/…). The command can reach only those hosts, over HTTPS through a proxy that is set for it; the project is read-only for it.")
	return b.String(), nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
