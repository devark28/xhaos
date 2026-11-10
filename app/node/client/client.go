// Package client is the node side of nodes: what xhaos-node runs. It joins
// a server with a one-time code, then keeps one WebSocket open to it and
// serves its requests (commands as jobs, files in and out), always inside
// the node root and, unless the owner on this machine said otherwise, under
// Landlock.
package client

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/devark28/xhaos/app/node/wire"
	"github.com/devark28/xhaos/app/sandbox"
)

// Config is what a node keeps in its state folder (node.json), plus its key
// (node.key, beside it).
type Config struct {
	Server      string   `json:"server"` // https://nodes.example.com
	Node        int64    `json:"node"`
	Name        string   `json:"name"`
	Workspace   string   `json:"workspace"`
	Root        string   `json:"root"`
	Devices     []string `json:"devices,omitempty"`
	Unsandboxed bool     `json:"unsandboxed,omitempty"`
}

// Paths in the state folder.
func configPath(state string) string { return filepath.Join(state, "node.json") }
func keyPath(state string) string    { return filepath.Join(state, "node.key") }

// Load reads a joined node's config and key.
func Load(state string) (Config, ed25519.PrivateKey, error) {
	var c Config
	raw, err := os.ReadFile(configPath(state))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return c, nil, fmt.Errorf("this machine has not joined a server yet (no %s): run xhaos-node join", configPath(state))
		}
		return c, nil, err
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, nil, fmt.Errorf("%s: %w", configPath(state), err)
	}
	seed, err := os.ReadFile(keyPath(state))
	if err != nil {
		return c, nil, err
	}
	b, err := hex.DecodeString(strings.TrimSpace(string(seed)))
	if err != nil || len(b) != ed25519.SeedSize {
		return c, nil, fmt.Errorf("%s is not a node key", keyPath(state))
	}
	return c, ed25519.NewKeyFromSeed(b), nil
}

func save(state string, c Config, key ed25519.PrivateKey) error {
	if err := os.MkdirAll(state, 0o700); err != nil {
		return err
	}
	keyFile, configFile := Encode(c, key)
	if err := os.WriteFile(keyPath(state), keyFile, 0o600); err != nil {
		return err
	}
	return os.WriteFile(configPath(state), configFile, 0o600)
}

// Encode is what save writes: node.key and node.json.
func Encode(c Config, key ed25519.PrivateKey) (keyFile, configFile []byte) {
	raw, _ := json.MarshalIndent(c, "", "  ")
	return []byte(hex.EncodeToString(key.Seed()) + "\n"), append(raw, '\n')
}

// Leave forgets the server: deletes the key and the config.
func Leave(state string) error {
	for _, p := range []string{keyPath(state), configPath(state), filepath.Join(state, ConnectedFile)} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// endpoint turns the server's address into the node WebSocket's URL. Only
// HTTPS, except plain HTTP to this machine (development).
func endpoint(server string) (string, error) {
	u, err := url.Parse(strings.TrimRight(server, "/"))
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("%q is not a server address (e.g. https://nodes.example.com)", server)
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		host := u.Hostname()
		if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return "", fmt.Errorf("%s: nodes only connect over HTTPS (plain HTTP only to this machine)", server)
		}
		u.Scheme = "ws"
	default:
		return "", fmt.Errorf("%s: use an https:// address", server)
	}
	u.Path = wire.Path
	return u.String(), nil
}

func dial(ctx context.Context, server string) (*websocket.Conn, error) {
	u, err := endpoint(server)
	if err != nil {
		return nil, err
	}
	c, resp, err := websocket.Dial(ctx, u, nil)
	if err != nil {
		if resp != nil && resp.StatusCode != http.StatusSwitchingProtocols {
			return nil, fmt.Errorf("%s answered %s (is %s exposed by the reverse proxy?)", server, resp.Status, wire.Path)
		}
		return nil, err
	}
	c.SetReadLimit(wire.MaxFrame)
	return c, nil
}

// Join joins the server with a one-time code: makes this node's key, sends
// its public half, and saves what the server answers.
func Join(ctx context.Context, state string, c Config, code, version string) (Config, error) {
	if _, _, err := Load(state); err == nil {
		return c, fmt.Errorf("this machine has already joined a server (see %s); run xhaos-node leave first", configPath(state))
	}
	if err := os.MkdirAll(c.Root, 0o755); err != nil {
		return c, err
	}
	c, key, err := Enroll(ctx, c, code, version)
	if err != nil {
		return c, err
	}
	return c, save(state, c, key)
}

// Enroll is Join's network half: it makes a key and joins with the code, but
// touches no file (apply-usb runs as root and saves the result itself).
func Enroll(ctx context.Context, c Config, code, version string) (Config, ed25519.PrivateKey, error) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return c, nil, err
	}
	conn, err := dial(ctx, c.Server)
	if err != nil {
		return c, nil, err
	}
	defer conn.CloseNow()
	facts := Facts(c, version)
	if err := wsjson.Write(ctx, conn, wire.Msg{Type: wire.TJoin, Code: strings.TrimSpace(code), PublicKey: key.Public().(ed25519.PublicKey), Facts: &facts}); err != nil {
		return c, nil, err
	}
	var m wire.Msg
	for m.Type == "" || m.Type == wire.TChallenge { // the server greets every connection with one
		if err := wsjson.Read(ctx, conn, &m); err != nil {
			return c, nil, fmt.Errorf("joining: %w", err)
		}
	}
	if m.Type == wire.TError {
		return c, nil, refusal{m.Error} // trying again will not help: Refused(err)
	}
	if m.Type != wire.TJoined || m.Node == 0 {
		return c, nil, fmt.Errorf("joining: unexpected answer %q", m.Type)
	}
	c.Node, c.Name, c.Workspace = m.Node, m.Name, m.Workspace
	conn.Close(websocket.StatusNormalClosure, "joined")
	return c, key, nil
}

// Node serves one server, reconnecting as needed.
type Node struct {
	Config  Config
	Key     ed25519.PrivateKey
	Version string
	State   string
	Log     *log.Logger

	sb   sandbox.Config
	jobs *jobs

	mu   sync.Mutex
	conn *websocket.Conn
}

// Run serves until ctx ends; it reconnects with backoff (1 s up to 60 s).
// A refusal by the server (the node was removed) ends it with an error.
func (n *Node) Run(ctx context.Context) error {
	if n.Log == nil {
		n.Log = log.New(os.Stderr, "xhaos-node: ", log.LstdFlags)
	}
	if err := os.MkdirAll(n.Config.Root, 0o755); err != nil {
		return err
	}
	n.sb = sandbox.Config{Mode: sandbox.On, Cache: filepath.Join(n.State, "cache"), LimitDevices: true, Devices: n.Config.Devices}
	if n.Config.Unsandboxed {
		n.sb = sandbox.Config{Mode: sandbox.Off}
	}
	n.jobs = newJobs(n.sendJobs)
	defer n.jobs.killAll()
	backoff := time.Second
	for {
		start := time.Now()
		err := n.session(ctx)
		if ctx.Err() != nil {
			return nil
		}
		var refused refusal
		if errors.As(err, &refused) {
			return err
		}
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		n.Log.Printf("disconnected: %v; retrying in %s", err, backoff)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, time.Minute)
	}
}

// refusal is the server saying no (removed node, wrong key, old protocol,
// a wrong, used or expired join code).
type refusal struct{ why string }

func (r refusal) Error() string { return "the server refused this node: " + r.why }

// Refused reports whether Run or Join ended because the server refused this
// node (it was removed, or the join code is no good): retrying will not help.
func Refused(err error) bool {
	var r refusal
	return errors.As(err, &r)
}

func (n *Node) session(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	conn, err := dial(ctx, n.Config.Server)
	if err != nil {
		return err
	}
	defer conn.CloseNow()
	var m wire.Msg
	if err := wsjson.Read(ctx, conn, &m); err != nil {
		return err
	}
	if m.Type != wire.TChallenge {
		return fmt.Errorf("expected a challenge, got %q", m.Type)
	}
	facts := Facts(n.Config, n.Version)
	hello := wire.Msg{Type: wire.THello, Node: n.Config.Node, Sig: wire.Sign(n.Key, m.Nonce), Facts: &facts, Jobs: n.jobs.list()}
	// The server reads at most wire.MaxHello before the node has proven
	// itself: a long job list follows the welcome instead.
	jobsLater := false
	if raw, _ := json.Marshal(hello); len(raw) > wire.MaxHello-4096 {
		hello.Jobs, jobsLater = nil, true
	}
	if err := wsjson.Write(ctx, conn, hello); err != nil {
		return err
	}
	if err := wsjson.Read(ctx, conn, &m); err != nil {
		var ce websocket.CloseError
		if errors.As(err, &ce) && ce.Code == websocket.StatusPolicyViolation {
			return refusal{ce.Reason}
		}
		return err
	}
	if m.Type == wire.TError {
		return refusal{m.Error}
	}
	if m.Type != wire.TWelcome {
		return fmt.Errorf("expected welcome, got %q", m.Type)
	}
	n.Log.Printf("connected to %s as %s (node %d)", n.Config.Server, m.Name, n.Config.Node)
	n.markConnected()
	n.mu.Lock()
	n.conn = conn
	n.mu.Unlock()
	if jobsLater {
		n.sendJobs()
	}
	defer func() {
		n.mu.Lock()
		n.conn = nil
		n.mu.Unlock()
	}()
	s := &served{n: n, conn: conn, ctx: ctx, reqs: map[uint64]*request{}}
	go func() { // keepalive: a dead link is noticed within a minute
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				pctx, pc := context.WithTimeout(ctx, 20*time.Second)
				err := conn.Ping(pctx)
				pc()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
	return s.loop()
}

// ConnectedFile is written in the state folder after the node's first
// welcome: a Pi's first boot waits for it before deleting its first-boot
// files (the Wi-Fi password and the join code) from the card.
const ConnectedFile = "connected"

func (n *Node) markConnected() {
	if n.State == "" {
		return
	}
	p := filepath.Join(n.State, ConnectedFile)
	if _, err := os.Lstat(p); err == nil {
		return
	}
	stamp := time.Now().UTC().Format(time.RFC3339) + " " + n.Config.Server + "\n"
	if err := os.WriteFile(p, []byte(stamp), 0o600); err != nil {
		n.Log.Printf("writing %s: %v", p, err)
	}
}

// sendJobs tells the server the job list changed.
func (n *Node) sendJobs() {
	n.mu.Lock()
	conn := n.conn
	n.mu.Unlock()
	if conn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = wsjson.Write(ctx, conn, wire.Msg{Type: wire.TJobs, Jobs: n.jobs.list()})
}

// served is one connection's requests.
type served struct {
	n    *Node
	conn *websocket.Conn
	ctx  context.Context
	mu   sync.Mutex
	reqs map[uint64]*request
}

type request struct {
	cancel context.CancelFunc
	data   chan []byte   // binary frames of a put
	eof    chan wire.Msg // its eof
}

func (s *served) send(m wire.Msg) error {
	ctx, cancel := context.WithTimeout(s.ctx, time.Minute)
	defer cancel()
	return wsjson.Write(ctx, s.conn, m)
}

func (s *served) fail(id uint64, err error) {
	_ = s.send(wire.Msg{Type: wire.TError, ID: id, Error: err.Error()})
}

func (s *served) loop() error {
	for {
		typ, data, err := s.conn.Read(s.ctx)
		if err != nil {
			var ce websocket.CloseError
			if errors.As(err, &ce) && ce.Code == websocket.StatusPolicyViolation {
				return refusal{ce.Reason} // removed from the server
			}
			return err
		}
		if typ == websocket.MessageBinary {
			id, chunk, err := wire.Unframe(data)
			if err != nil {
				return err
			}
			s.mu.Lock()
			r := s.reqs[id]
			s.mu.Unlock()
			if r != nil && r.data != nil {
				select {
				case r.data <- chunk:
				case <-s.ctx.Done():
					return s.ctx.Err()
				}
			}
			continue
		}
		var m wire.Msg
		if err := json.Unmarshal(data, &m); err != nil {
			return err
		}
		if m.Type == wire.TError && m.ID == 0 {
			return refusal{m.Error} // removed from the server
		}
		s.dispatch(m)
	}
}

func (s *served) dispatch(m wire.Msg) {
	switch m.Type {
	case wire.TPing:
		go s.send(wire.Msg{Type: wire.TPong, ID: m.ID})
	case wire.TCancel:
		s.mu.Lock()
		if r := s.reqs[m.ID]; r != nil {
			r.cancel()
		}
		s.mu.Unlock()
	case wire.TEOF:
		s.mu.Lock()
		r := s.reqs[m.ID]
		s.mu.Unlock()
		if r != nil && r.eof != nil {
			r.eof <- m
		}
	case wire.TExec, wire.TJob, wire.TPut, wire.TGet:
		ctx, cancel := context.WithCancel(s.ctx)
		r := &request{cancel: cancel}
		if m.Type == wire.TPut {
			r.data, r.eof = make(chan []byte, 16), make(chan wire.Msg, 1)
		}
		s.mu.Lock()
		s.reqs[m.ID] = r
		s.mu.Unlock()
		go func() {
			defer func() {
				cancel()
				s.mu.Lock()
				delete(s.reqs, m.ID)
				s.mu.Unlock()
			}()
			var err error
			switch m.Type {
			case wire.TExec:
				err = s.exec(ctx, m)
			case wire.TJob:
				err = s.job(ctx, m)
			case wire.TPut:
				err = s.put(ctx, m, r)
			case wire.TGet:
				err = s.get(ctx, m)
			}
			if err != nil {
				s.fail(m.ID, err)
			}
		}()
	default:
		s.fail(m.ID, fmt.Errorf("this node does not know %q (update xhaos-node)", m.Type))
	}
}

func waitFor(ms int) time.Duration {
	if ms < 0 {
		return 0
	}
	return time.Duration(min(ms, wire.MaxWait)) * time.Millisecond
}

func (s *served) exec(ctx context.Context, m wire.Msg) error {
	if strings.TrimSpace(m.Command) == "" {
		return errors.New("no command")
	}
	n := s.n
	if !n.Config.Unsandboxed && sandbox.ABI() == 0 {
		return errors.New("this node's kernel has no Landlock, so it runs no commands; the owner of the machine can allow it with xhaos-node join --unsandboxed (or install-node.sh --unsandboxed)")
	}
	dir, err := wire.Confine(n.Config.Root, m.Dir)
	if err != nil {
		return err
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return fmt.Errorf("%s is not a folder on this node", m.Dir)
	}
	cmd, err := n.sb.Command(context.Background(), n.Config.Root, m.Command)
	if err != nil {
		return err
	}
	cmd.Dir = dir
	if n.Config.Unsandboxed {
		cmd.Env = append(os.Environ(), "XHAOS_NODE=1")
	} else {
		cmd.Env = append(cmd.Env, "XHAOS_NODE=1")
	}
	j, err := n.jobs.start(cmd, m.Command, m.Dir)
	if err != nil {
		return err
	}
	j.wait(ctx, waitFor(m.Wait))
	data, from, info := j.read(0)
	return s.send(wire.Msg{Type: wire.TJobInfo, ID: m.ID, Job: &info, Data: data, Offset: from})
}

func (s *served) job(ctx context.Context, m wire.Msg) error {
	if m.JobID == "" {
		return s.send(wire.Msg{Type: wire.TJobInfo, ID: m.ID, Jobs: s.n.jobs.list()})
	}
	j, err := s.n.jobs.get(m.JobID)
	if err != nil {
		return err
	}
	switch m.Action {
	case wire.ActKill:
		j.kill()
	case wire.ActWait:
		j.wait(ctx, waitFor(m.Wait))
	case wire.ActLogs, "":
	default:
		return fmt.Errorf("unknown job action %q (logs, wait, kill)", m.Action)
	}
	data, from, info := j.read(m.Offset)
	return s.send(wire.Msg{Type: wire.TJobInfo, ID: m.ID, Job: &info, Data: data, Offset: from})
}

// put receives a file (or a folder's tar) into the node root. The bytes go
// to a partial file beside the target; the SHA-256 is checked before the
// file takes its place (or the tar is unpacked).
func (s *served) put(ctx context.Context, m wire.Msg, r *request) error {
	// A refusal answers at once, so the server stops sending; the frames
	// already on their way are dropped until its eof.
	refuse := func(err error) error {
		s.fail(m.ID, err)
		drain(ctx, r)
		return nil
	}
	target, err := wire.Confine(s.n.Config.Root, m.Path)
	if err != nil {
		return refuse(err)
	}
	if real, _ := filepath.EvalSymlinks(s.n.Config.Root); (target == real || target == s.n.Config.Root) && !m.Tar {
		return refuse(errors.New("name a file to copy to, not the node's folder itself"))
	}
	parent := filepath.Dir(target)
	if m.Tar {
		parent = target
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return refuse(err)
	}
	tmp, err := os.CreateTemp(parent, ".xhaos-partial-*")
	if err != nil {
		return refuse(err)
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	var size int64
	for {
		select {
		case <-ctx.Done():
			tmp.Close()
			return ctx.Err()
		case chunk := <-r.data:
			size += int64(len(chunk))
			if size > wire.MaxCopy {
				tmp.Close()
				return refuse(fmt.Errorf("copy is larger than the limit (%d MiB)", wire.MaxCopy>>20))
			}
			h.Write(chunk)
			if _, err := tmp.Write(chunk); err != nil {
				tmp.Close()
				return refuse(err)
			}
		case eof := <-r.eof:
			// Frames sent before the eof may still be queued.
			for len(r.data) > 0 {
				chunk := <-r.data
				size += int64(len(chunk))
				h.Write(chunk)
				if _, err := tmp.Write(chunk); err != nil {
					tmp.Close()
					return err
				}
			}
			if eof.Error != "" {
				tmp.Close()
				return errors.New(eof.Error)
			}
			sum := hex.EncodeToString(h.Sum(nil))
			if sum != eof.SHA256 {
				tmp.Close()
				return fmt.Errorf("SHA-256 mismatch: received %s, sender has %s; nothing was written", sum, eof.SHA256)
			}
			if m.Tar {
				if _, err := tmp.Seek(0, io.SeekStart); err != nil {
					tmp.Close()
					return err
				}
				_, err := wire.ExtractTar(tmp, target, func(rel string) (string, error) {
					return wire.Confine(s.n.Config.Root, filepath.Join(target, rel))
				})
				tmp.Close()
				if err != nil {
					return err
				}
			} else {
				mode := os.FileMode(m.Mode).Perm()
				if mode == 0 {
					mode = 0o644
				}
				if err := tmp.Chmod(mode); err != nil {
					tmp.Close()
					return err
				}
				if err := tmp.Close(); err != nil {
					return err
				}
				if fi, err := os.Lstat(target); err == nil && fi.IsDir() {
					return fmt.Errorf("%s is a folder on the node", m.Path)
				}
				if err := os.Rename(tmp.Name(), target); err != nil {
					return err
				}
			}
			return s.send(wire.Msg{Type: wire.TDone, ID: m.ID, Size: size, SHA256: sum})
		}
	}
}

// drain discards a refused put's frames until its eof.
func drain(ctx context.Context, r *request) {
	t := time.NewTimer(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-r.data:
		case <-r.eof:
			return
		case <-ctx.Done():
			return
		case <-t.C:
			return
		}
	}
}

// get sends a file, or a folder as a tar stream, then its SHA-256.
func (s *served) get(ctx context.Context, m wire.Msg) error {
	src, err := wire.Confine(s.n.Config.Root, m.Path)
	if err != nil {
		return err
	}
	fi, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("%s: not found on the node", m.Path)
	}
	h := sha256.New()
	w := &frameWriter{s: s, ctx: ctx, id: m.ID, h: h}
	if fi.IsDir() {
		if err := s.send(wire.Msg{Type: wire.TMeta, ID: m.ID, Tar: true, Size: -1}); err != nil {
			return err
		}
		files, err := wire.WalkFiles(src)
		if err != nil {
			return err
		}
		if err := wire.WriteTar(w, src, files); err != nil {
			return err
		}
	} else {
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", m.Path)
		}
		if fi.Size() > wire.MaxCopy {
			return fmt.Errorf("%s is %d MiB, over the copy limit (%d MiB)", m.Path, fi.Size()>>20, wire.MaxCopy>>20)
		}
		if err := s.send(wire.Msg{Type: wire.TMeta, ID: m.ID, Mode: uint32(fi.Mode().Perm()), Size: fi.Size()}); err != nil {
			return err
		}
		f, err := os.Open(src)
		if err != nil {
			return err
		}
		_, err = io.Copy(w, f)
		f.Close()
		if err != nil {
			return err
		}
	}
	if err := w.flush(); err != nil {
		return err
	}
	return s.send(wire.Msg{Type: wire.TEOF, ID: m.ID, SHA256: hex.EncodeToString(h.Sum(nil)), Size: w.n})
}

// frameWriter sends what is written as binary frames of wire.Chunk bytes.
type frameWriter struct {
	s   *served
	ctx context.Context
	id  uint64
	h   io.Writer
	buf []byte
	n   int64
}

func (w *frameWriter) Write(p []byte) (int, error) {
	w.n += int64(len(p))
	if w.n > wire.MaxCopy {
		return 0, fmt.Errorf("copy is larger than the limit (%d MiB)", wire.MaxCopy>>20)
	}
	w.h.Write(p)
	w.buf = append(w.buf, p...)
	for len(w.buf) >= wire.Chunk {
		if err := w.s.conn.Write(w.ctx, websocket.MessageBinary, wire.Frame(w.id, w.buf[:wire.Chunk])); err != nil {
			return 0, err
		}
		w.buf = append(w.buf[:0], w.buf[wire.Chunk:]...)
	}
	return len(p), nil
}

func (w *frameWriter) flush() error {
	if len(w.buf) == 0 {
		return nil
	}
	err := w.s.conn.Write(w.ctx, websocket.MessageBinary, wire.Frame(w.id, w.buf))
	w.buf = w.buf[:0]
	return err
}
