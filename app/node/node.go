// Package node joins other machines to a workspace: a strong PC to build on,
// Raspberry Pis to test on. A node runs xhaos-node (package node/client),
// which dials out to this server's public address and keeps a WebSocket
// open; agents granted the node run commands and copy files there with the
// node_* tools, under the same approvals as bash.
//
// Service is the owner's API (bound to the page). Hub serves the nodes'
// connections and Tools gives agents the node_* tools; neither is bound.
// See docs/nodes.md.
package node

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/devark28/xhaos/app/apperr"
	"github.com/devark28/xhaos/app/eventlog"
	"github.com/devark28/xhaos/app/node/wire"
	"github.com/devark28/xhaos/app/sqlitedb"
)

// EventChanged is recorded when a node is added, renamed, granted, joined
// or removed (not for online/offline or jobs: those go out live only).
const EventChanged = "node.changed" // data: {"id": …}

// Join codes: single use, deleted when used or expired.
const (
	CodeTTL   = 15 * time.Minute
	PiCodeTTL = time.Hour // flashing a card and a Pi's first boot take longer
)

const schema = `
CREATE TABLE IF NOT EXISTS nodes (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	workspace_id TEXT NOT NULL REFERENCES workspaces(id),
	name         TEXT NOT NULL,
	labels       TEXT NOT NULL DEFAULT '[]',
	public_key   BLOB,               -- the node's Ed25519 key; NULL until it joins
	facts        TEXT NOT NULL DEFAULT '{}',
	created_at   TEXT NOT NULL,
	joined_at    TEXT,
	last_seen    TEXT,
	UNIQUE (workspace_id, name)
);
CREATE TABLE IF NOT EXISTS node_codes (
	hash       TEXT PRIMARY KEY,      -- SHA-256 of the code; the code itself is never stored
	node_id    INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
	expires_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS node_grants (
	node_id  INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
	agent_id INTEGER NOT NULL REFERENCES agents(id),
	PRIMARY KEY (node_id, agent_id)
);`

func Migrate(db *sql.DB) error {
	_, err := db.Exec(schema)
	return err
}

// States of a node.
const (
	Waiting = "waiting" // added; the machine has not joined yet
	Online  = "online"
	Offline = "offline"
)

// View is a node as the page sees it.
type View struct {
	ID          int64       `json:"id"`
	Name        string      `json:"name"`
	Labels      []string    `json:"labels"`
	State       string      `json:"state"`
	Facts       *wire.Facts `json:"facts,omitempty"`
	CreatedAt   time.Time   `json:"createdAt"`
	JoinedAt    *time.Time  `json:"joinedAt,omitempty"`
	LastSeen    *time.Time  `json:"lastSeen,omitempty"`
	CodeExpires *time.Time  `json:"codeExpires,omitempty"` // a waiting node's live join code
	Grants      []int64     `json:"grants"`                // agent IDs
	Jobs        []wire.Job  `json:"jobs"`
}

// List is the Nodes tab.
type List struct {
	// URL is the public address nodes connect to (-node-url); empty means
	// nodes cannot be added yet, and Note says why.
	URL   string `json:"url"`
	Note  string `json:"note,omitempty"`
	Nodes []View `json:"nodes"`
}

// Notice is a live, unpersisted change: a node came online, went offline,
// or its jobs changed.
type Notice struct {
	ID       int64       `json:"id"`
	State    string      `json:"state"`
	LastSeen *time.Time  `json:"lastSeen,omitempty"`
	Facts    *wire.Facts `json:"facts,omitempty"`
	Jobs     []wire.Job  `json:"jobs"`
}

// Notifier sends a Notice to a workspace's pages.
type Notifier func(workspaceID string, n Notice)

// Service is the owner's API.
type Service struct {
	db      *sql.DB
	publish eventlog.Publisher
	hub     *Hub
	// URL is the public address nodes connect to (https://…); "" disables
	// adding nodes.
	URL string
	// Version is this server's; release versions (v…) pin the node's.
	Version string
	now     func() time.Time
}

// New makes the service and its hub (which serves the nodes).
func New(db *sql.DB, publish eventlog.Publisher, live Notifier, url string) (*Service, *Hub) {
	if publish == nil {
		publish = func(eventlog.Event) {}
	}
	if live == nil {
		live = func(string, Notice) {}
	}
	s := &Service{db: db, publish: publish, URL: strings.TrimRight(url, "/"), now: time.Now}
	s.hub = newHub(s, live)
	return s, s.hub
}

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{0,39}$`)

func cleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if !nameRE.MatchString(name) {
		return "", fmt.Errorf("%w: a node's name is 1 to 40 letters, digits, spaces, dots, dashes or underscores", apperr.Invalid)
	}
	return name, nil
}

func cleanLabels(labels []string) []string {
	out := []string{}
	for _, l := range labels {
		l = strings.ToLower(strings.TrimSpace(l))
		if l != "" && len(l) <= 30 && !slices.Contains(out, l) {
			out = append(out, l)
		}
	}
	return out
}

// Nodes lists the workspace's nodes.
func (s *Service) Nodes(workspaceID string) (List, error) {
	s.purge()
	list := List{URL: s.URL, Nodes: []View{}}
	if s.URL == "" {
		list.Note = "Nodes connect to this server at a public HTTPS address. Start Xhaos with -node-url https://… (see docs/nodes.md) to add them."
	}
	rows, err := s.db.Query(`SELECT id FROM nodes WHERE workspace_id = ? ORDER BY id`, workspaceID)
	if err != nil {
		return list, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return list, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		v, err := s.view(workspaceID, id)
		if err != nil {
			return list, err
		}
		list.Nodes = append(list.Nodes, v)
	}
	return list, nil
}

// view reads one node (of workspace ws).
func (s *Service) view(ws string, id int64) (View, error) {
	var (
		v                      View
		labels, facts, created string
		joined, seen           sql.NullString
		key                    []byte
	)
	err := s.db.QueryRow(`SELECT id, name, labels, public_key, facts, created_at, joined_at, last_seen FROM nodes WHERE workspace_id = ? AND id = ?`, ws, id).
		Scan(&v.ID, &v.Name, &labels, &key, &facts, &created, &joined, &seen)
	if errors.Is(err, sql.ErrNoRows) {
		return v, fmt.Errorf("%w: node %d", apperr.NotFound, id)
	}
	if err != nil {
		return v, err
	}
	_ = json.Unmarshal([]byte(labels), &v.Labels)
	if v.Labels == nil {
		v.Labels = []string{}
	}
	v.CreatedAt = sqlitedb.ParseStamp(created)
	if joined.Valid {
		t := sqlitedb.ParseStamp(joined.String)
		v.JoinedAt = &t
	}
	if seen.Valid {
		t := sqlitedb.ParseStamp(seen.String)
		v.LastSeen = &t
	}
	if facts != "{}" && facts != "" {
		var f wire.Facts
		if json.Unmarshal([]byte(facts), &f) == nil {
			v.Facts = &f
		}
	}
	v.State, v.Jobs = Offline, []wire.Job{}
	if key == nil {
		v.State = Waiting
		var exp string
		if err := s.db.QueryRow(`SELECT expires_at FROM node_codes WHERE node_id = ? ORDER BY expires_at DESC LIMIT 1`, id).Scan(&exp); err == nil {
			t := sqlitedb.ParseStamp(exp)
			v.CodeExpires = &t
		}
	} else if live, ok := s.hub.live(id); ok {
		v.State, v.Facts, v.Jobs = Online, &live.facts, live.jobs
	}
	v.Grants = []int64{}
	rows, err := s.db.Query(`SELECT agent_id FROM node_grants WHERE node_id = ? ORDER BY agent_id`, id)
	if err != nil {
		return v, err
	}
	defer rows.Close()
	for rows.Next() {
		var a int64
		if err := rows.Scan(&a); err != nil {
			return v, err
		}
		v.Grants = append(v.Grants, a)
	}
	return v, nil
}

// changed records a change so other pages refresh.
func (s *Service) changed(ws string, id int64) {
	tx, err := s.db.Begin()
	if err != nil {
		return
	}
	defer tx.Rollback()
	ev, err := eventlog.Append(tx, ws, 0, EventChanged, map[string]int64{"id": id}, s.now())
	if err == nil && tx.Commit() == nil {
		s.publish(ev)
	}
}

// purge deletes expired join codes, used or not.
func (s *Service) purge() {
	_, _ = s.db.Exec(`DELETE FROM node_codes WHERE expires_at <= ?`, sqlitedb.Stamp(s.now()))
}

// PiSetup is what the Raspberry Pi first-boot files need. The Wi-Fi password
// is written into the files and nowhere else.
type PiSetup struct {
	Hostname     string `json:"hostname"`
	User         string `json:"user"`   // default "pi"
	SSHKey       string `json:"sshKey"` // optional: a public key for the owner's login
	WifiSSID     string `json:"wifiSsid"`
	WifiPassword string `json:"wifiPassword"`
	Country      string `json:"country"`   // ISO 3166 code, e.g. GB, RW
	UsbConfig    bool   `json:"usbConfig"` // install-node.sh --usb-config: XHAOS sticks may change Wi-Fi and server
	UsbGadget    bool   `json:"usbGadget"` // the Pi's USB data port becomes a network link to the computer it is plugged into
}

type AddRequest struct {
	WorkspaceID string   `json:"workspaceId"`
	Name        string   `json:"name"`
	Labels      []string `json:"labels"`
	Pi          *PiSetup `json:"pi,omitempty"`
}

// File is a file to download (the Raspberry Pi first-boot files).
type File struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// Added is a new join code, shown once.
type Added struct {
	Node    View      `json:"node"`
	Code    string    `json:"code"`
	Expires time.Time `json:"expires"`
	Command string    `json:"command"`
	Files   []File    `json:"files,omitempty"`
	Warning string    `json:"warning,omitempty"`
}

// Add makes a node that waits for its machine, and its join code.
func (s *Service) Add(req AddRequest) (Added, error) {
	if s.URL == "" {
		return Added{}, fmt.Errorf("%w: nodes need this server's public address: start Xhaos with -node-url https://… (see docs/nodes.md)", apperr.Invalid)
	}
	name, err := cleanName(req.Name)
	if err != nil {
		return Added{}, err
	}
	if req.Pi != nil {
		if err := req.Pi.check(); err != nil {
			return Added{}, err
		}
	}
	labels, _ := json.Marshal(cleanLabels(req.Labels))
	res, err := s.db.Exec(`INSERT INTO nodes (workspace_id, name, labels, created_at) VALUES (?, ?, ?, ?)`, req.WorkspaceID, name, string(labels), sqlitedb.Stamp(s.now()))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return Added{}, fmt.Errorf("%w: there is already a node named %s", apperr.Conflict, name)
		}
		return Added{}, err
	}
	id, _ := res.LastInsertId()
	out, err := s.code(req.WorkspaceID, id, req.Pi)
	if err != nil {
		return Added{}, err
	}
	s.changed(req.WorkspaceID, id)
	return out, nil
}

type CodeRequest struct {
	WorkspaceID string   `json:"workspaceId"`
	NodeID      int64    `json:"nodeId"`
	Pi          *PiSetup `json:"pi,omitempty"`
}

// NewCode replaces a waiting node's join code (the old one stops working).
func (s *Service) NewCode(req CodeRequest) (Added, error) {
	if s.URL == "" {
		return Added{}, fmt.Errorf("%w: nodes need this server's public address (-node-url)", apperr.Invalid)
	}
	v, err := s.view(req.WorkspaceID, req.NodeID)
	if err != nil {
		return Added{}, err
	}
	if v.State != Waiting {
		return Added{}, fmt.Errorf("%w: %s has joined already; to join another machine under this name, remove it and add it again", apperr.Conflict, v.Name)
	}
	if req.Pi != nil {
		if err := req.Pi.check(); err != nil {
			return Added{}, err
		}
	}
	out, err := s.code(req.WorkspaceID, req.NodeID, req.Pi)
	if err == nil {
		s.changed(req.WorkspaceID, req.NodeID)
	}
	return out, err
}

// codeAlphabet has no look-alike characters (0/O, 1/I/L).
const codeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

func newCode() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	var sb strings.Builder
	for i, c := range b {
		if i > 0 && i%4 == 0 {
			sb.WriteByte('-')
		}
		sb.WriteByte(codeAlphabet[int(c)%len(codeAlphabet)])
	}
	return sb.String()
}

// hashCode is what is stored: the code without dashes or case.
func hashCode(code string) string {
	c := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(code), "-", ""))
	sum := sha256.Sum256([]byte("xhaos-node-code\x00" + c))
	return hex.EncodeToString(sum[:])
}

func (s *Service) code(ws string, id int64, pi *PiSetup) (Added, error) {
	s.purge()
	code := newCode()
	ttl := CodeTTL
	if pi != nil {
		ttl = PiCodeTTL
	}
	exp := s.now().Add(ttl)
	tx, err := s.db.Begin()
	if err != nil {
		return Added{}, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM node_codes WHERE node_id = ?`, id); err != nil {
		return Added{}, err
	}
	if _, err := tx.Exec(`INSERT INTO node_codes (hash, node_id, expires_at) VALUES (?, ?, ?)`, hashCode(code), id, sqlitedb.Stamp(exp)); err != nil {
		return Added{}, err
	}
	if err := tx.Commit(); err != nil {
		return Added{}, err
	}
	v, err := s.view(ws, id)
	if err != nil {
		return Added{}, err
	}
	out := Added{Node: v, Code: code, Expires: exp, Command: InstallCommand(s.URL, code, s.Version)}
	if pi != nil {
		id := make([]byte, 8)
		_, _ = rand.Read(id)
		out.Files = piFiles(*pi, s.URL, code, s.Version, hex.EncodeToString(id), exp)
		out.Command = FlashCommand(s.Version)
		out.Warning = "These files hold your Wi-Fi password and this join code in plain text, on a partition any computer can read, until the Pi has joined (it then deletes them from the card). The code works once and expires in 1 hour, used or not."
	}
	return out, nil
}

// InstallCommand is what to run on a machine to join it (as root: it
// creates the xhaos-node user and its service). A release server pins the
// node to its own release, so both speak the same protocol.
func InstallCommand(url, code, version string) string {
	return "curl -fsSL " + ScriptURL("install-node.sh", version) + " | sudo sh -s -- " + installArgs(url, code, version)
}

func installArgs(url, code, version string) string {
	args := fmt.Sprintf("--server %s --code %s", url, code)
	if releaseRE.MatchString(version) {
		args += " --version " + version
	}
	return args
}

// ScriptURL is where a node script (install-node.sh, flash-node.sh) is
// downloaded from: the server's own release tag, so a release server never
// hands root a script from a moving branch; main only for development builds.
func ScriptURL(name, version string) string {
	ref := "main"
	if releaseRE.MatchString(version) {
		ref = version
	}
	return "https://raw.githubusercontent.com/devark28/xhaos/" + ref + "/" + name
}

var releaseRE = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+([-.+][0-9A-Za-z.-]+)?$`)

// FlashCommand writes Raspberry Pi OS and a Pi's first-boot files (from the
// downloads folder) to an SD card on a Linux computer, the Pi's Command.
func FlashCommand(version string) string {
	return "curl -fsSL " + ScriptURL("flash-node.sh", version) + " | sudo sh -s -- --files ~/Downloads"
}

type UpdateRequest struct {
	WorkspaceID string   `json:"workspaceId"`
	NodeID      int64    `json:"nodeId"`
	Name        string   `json:"name"`
	Labels      []string `json:"labels"`
}

// Update renames a node and sets its labels.
func (s *Service) Update(req UpdateRequest) (View, error) {
	name, err := cleanName(req.Name)
	if err != nil {
		return View{}, err
	}
	labels, _ := json.Marshal(cleanLabels(req.Labels))
	res, err := s.db.Exec(`UPDATE nodes SET name = ?, labels = ? WHERE workspace_id = ? AND id = ?`, name, string(labels), req.WorkspaceID, req.NodeID)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return View{}, fmt.Errorf("%w: there is already a node named %s", apperr.Conflict, name)
		}
		return View{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return View{}, fmt.Errorf("%w: node %d", apperr.NotFound, req.NodeID)
	}
	s.hub.rename(req.NodeID, name)
	s.changed(req.WorkspaceID, req.NodeID)
	return s.view(req.WorkspaceID, req.NodeID)
}

type NodeRequest struct {
	WorkspaceID string `json:"workspaceId"`
	NodeID      int64  `json:"nodeId"`
}

// Remove forgets a node: its key, codes and grants. Its connection is
// closed and any later one refused.
func (s *Service) Remove(req NodeRequest) error {
	res, err := s.db.Exec(`DELETE FROM nodes WHERE workspace_id = ? AND id = ?`, req.WorkspaceID, req.NodeID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: node %d", apperr.NotFound, req.NodeID)
	}
	s.hub.drop(req.NodeID, "this node was removed from Xhaos")
	s.changed(req.WorkspaceID, req.NodeID)
	return nil
}

type GrantRequest struct {
	WorkspaceID string `json:"workspaceId"`
	NodeID      int64  `json:"nodeId"`
	AgentID     int64  `json:"agentId"`
	Granted     bool   `json:"granted"`
}

// Grant lets an agent use a node, or stops it.
func (s *Service) Grant(req GrantRequest) (View, error) {
	if _, err := s.view(req.WorkspaceID, req.NodeID); err != nil {
		return View{}, err
	}
	if req.Granted {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM agents WHERE workspace_id = ? AND id = ?`, req.WorkspaceID, req.AgentID).Scan(&n); err != nil || n == 0 {
			return View{}, fmt.Errorf("%w: agent #%d", apperr.NotFound, req.AgentID)
		}
		if _, err := s.db.Exec(`INSERT OR IGNORE INTO node_grants (node_id, agent_id) VALUES (?, ?)`, req.NodeID, req.AgentID); err != nil {
			return View{}, err
		}
	} else if _, err := s.db.Exec(`DELETE FROM node_grants WHERE node_id = ? AND agent_id = ?`, req.NodeID, req.AgentID); err != nil {
		return View{}, err
	}
	s.changed(req.WorkspaceID, req.NodeID)
	return s.view(req.WorkspaceID, req.NodeID)
}

type JobRequest struct {
	WorkspaceID string `json:"workspaceId"`
	NodeID      int64  `json:"nodeId"`
	JobID       string `json:"jobId"`
}

// StopJob stops a job on a node (the Stop on its card).
func (s *Service) StopJob(req JobRequest) error {
	if _, err := s.view(req.WorkspaceID, req.NodeID); err != nil {
		return err
	}
	_, err := s.hub.jobAction(req.NodeID, wire.Msg{Type: wire.TJob, JobID: req.JobID, Action: wire.ActKill})
	return err
}
