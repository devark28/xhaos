package node

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/devark28/xhaos/app/auth"
	"github.com/devark28/xhaos/app/node/wire"
	"github.com/devark28/xhaos/app/sqlitedb"
)

// Hub serves the nodes' connections (wire.Path, mounted before the session
// check: a node proves itself with its key, or joins with a code) and runs
// requests on them.
type Hub struct {
	s      *Service
	notify Notifier
	limit  auth.Limiter // wrong codes and keys, per address
	mu     sync.Mutex
	conns  map[int64]*conn
}

func newHub(s *Service, live Notifier) *Hub {
	return &Hub{s: s, notify: live, conns: map[int64]*conn{}}
}

type liveNode struct {
	facts wire.Facts
	jobs  []wire.Job
}

func (h *Hub) live(id int64) (liveNode, bool) {
	h.mu.Lock()
	c := h.conns[id]
	h.mu.Unlock()
	if c == nil {
		return liveNode{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return liveNode{c.facts, append([]wire.Job{}, c.jobs...)}, true
}

// Online reports whether a node is connected.
func (h *Hub) Online(id int64) bool {
	_, ok := h.live(id)
	return ok
}

func (h *Hub) rename(id int64, name string) {
	h.mu.Lock()
	c := h.conns[id]
	h.mu.Unlock()
	if c != nil {
		c.mu.Lock()
		c.name = name
		c.mu.Unlock()
	}
}

// drop closes a node's connection (it was removed).
func (h *Hub) drop(id int64, why string) {
	h.mu.Lock()
	c := h.conns[id]
	h.mu.Unlock()
	if c != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = wsjson.Write(ctx, c.c, wire.Msg{Type: wire.TError, Error: why})
		cancel()
		c.c.Close(websocket.StatusPolicyViolation, why)
	}
}

// Close ends every node connection (the server is stopping).
func (h *Hub) Close() {
	h.mu.Lock()
	conns := make([]*conn, 0, len(h.conns))
	for _, c := range h.conns {
		conns = append(conns, c)
	}
	h.mu.Unlock()
	for _, c := range conns {
		c.c.Close(websocket.StatusGoingAway, "server stopping")
	}
}

func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != wire.Path {
		http.NotFound(w, r)
		return
	}
	addr := auth.ClientAddr(r)
	if wait := h.limit.Wait(addr, time.Now()); wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		http.Error(w, "too many wrong join codes or keys from this address; try again later", http.StatusTooManyRequests)
		return
	}
	w = hijackable(w)
	// The connection outlives the server's read and write timeouts.
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Time{})
	_ = rc.SetWriteDeadline(time.Time{})
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	c.SetReadLimit(wire.MaxFrame + 64)
	defer c.CloseNow()
	h.serve(c, addr)
}

// hijackable is the writer a WebSocket can take over: the server's own,
// below any wrappers. A wrapper may hold the status line back (Wails's
// content type sniffer keeps WriteHeader(101) until it sees a Content-Type
// or a body); a hijacked connection writes neither, so the 101 would never
// go out and the node would read its first frame as a broken HTTP reply.
func hijackable(w http.ResponseWriter) http.ResponseWriter {
	for {
		if _, ok := w.(http.Hijacker); ok {
			return w
		}
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return w
		}
		w = u.Unwrap()
	}
}

// refused is a node that must stop trying (its client exits).
type refused struct{ why string }

func (r refused) Error() string { return r.why }

func (h *Hub) serve(c *websocket.Conn, addr string) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	nonce := make([]byte, 32)
	_, _ = rand.Read(nonce)
	hctx, hcancel := context.WithTimeout(ctx, 30*time.Second)
	defer hcancel()
	if err := wsjson.Write(hctx, c, wire.Msg{Type: wire.TChallenge, Nonce: nonce}); err != nil {
		return
	}
	var m wire.Msg
	if err := wsjson.Read(hctx, c, &m); err != nil {
		return
	}
	switch m.Type {
	case wire.TJoin:
		_ = wsjson.Write(hctx, c, h.join(addr, m))
		c.Close(websocket.StatusNormalClosure, "")
		return
	case wire.THello:
	default:
		c.Close(websocket.StatusProtocolError, "expected join or hello")
		return
	}
	nc, err := h.hello(hctx, c, addr, nonce, m)
	if err != nil {
		var r refused
		if errors.As(err, &r) {
			_ = wsjson.Write(hctx, c, wire.Msg{Type: wire.TError, Error: r.why})
			c.Close(websocket.StatusNormalClosure, "")
		} else {
			slog.Error("node: hello", "node", m.Node, "err", err)
			c.Close(websocket.StatusInternalError, "server error")
		}
		return
	}
	hcancel()
	nc.run(ctx)
}

// join takes a one-time code and the node's public key.
func (h *Hub) join(addr string, m wire.Msg) wire.Msg {
	fail := func(why string) wire.Msg { return wire.Msg{Type: wire.TError, Error: why} }
	if m.Facts == nil || m.Facts.Protocol != wire.Protocol {
		return fail(fmt.Sprintf("this xhaos-node is too old or too new for the server (it speaks protocol %d, the server %d): install the same release as the server", protocolOf(m.Facts), wire.Protocol))
	}
	if len(m.PublicKey) != ed25519.PublicKeySize {
		return fail("no public key")
	}
	now := h.s.now()
	h.s.purge()
	tx, err := h.s.db.Begin()
	if err != nil {
		return fail("server error")
	}
	defer tx.Rollback()
	var (
		id       int64
		ws, name string
	)
	err = tx.QueryRow(`SELECT n.id, n.workspace_id, n.name FROM node_codes c JOIN nodes n ON n.id = c.node_id
		WHERE c.hash = ? AND c.expires_at > ? AND n.public_key IS NULL`, hashCode(m.Code), sqlitedb.Stamp(now)).Scan(&id, &ws, &name)
	if errors.Is(err, sql.ErrNoRows) {
		h.limit.Fail(addr, now)
		return fail("this join code is wrong, used or expired: on Xhaos's Nodes tab, make a new code for the node")
	}
	if err != nil {
		return fail("server error")
	}
	facts, _ := json.Marshal(m.Facts)
	if _, err := tx.Exec(`DELETE FROM node_codes WHERE node_id = ?`, id); err != nil {
		return fail("server error")
	}
	if _, err := tx.Exec(`UPDATE nodes SET public_key = ?, facts = ?, joined_at = ?, last_seen = ? WHERE id = ?`,
		[]byte(m.PublicKey), string(facts), sqlitedb.Stamp(now), sqlitedb.Stamp(now), id); err != nil {
		return fail("server error")
	}
	if err := tx.Commit(); err != nil {
		return fail("server error")
	}
	h.limit.Clear(addr)
	h.s.changed(ws, id)
	return wire.Msg{Type: wire.TJoined, Node: id, Workspace: ws, Name: name}
}

func protocolOf(f *wire.Facts) int {
	if f == nil {
		return 0
	}
	return f.Protocol
}

// hello checks a joined node's signature and makes it the node's connection.
func (h *Hub) hello(ctx context.Context, c *websocket.Conn, addr string, nonce []byte, m wire.Msg) (*conn, error) {
	now := h.s.now()
	var (
		ws, name string
		key      []byte
	)
	err := h.s.db.QueryRow(`SELECT workspace_id, name, public_key FROM nodes WHERE id = ?`, m.Node).Scan(&ws, &name, &key)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && key == nil) {
		h.limit.Fail(addr, now)
		return nil, refused{"this node is not on the server (it was removed): join again with a new code"}
	}
	if err != nil {
		return nil, err
	}
	if !wire.Verify(ed25519.PublicKey(key), nonce, m.Sig) {
		h.limit.Fail(addr, now)
		return nil, refused{"this node's key does not match the one it joined with: join again with a new code"}
	}
	if protocolOf(m.Facts) != wire.Protocol {
		return nil, refused{fmt.Sprintf("this xhaos-node speaks protocol %d, the server %d: install the same release as the server", protocolOf(m.Facts), wire.Protocol)}
	}
	h.limit.Clear(addr)
	facts, _ := json.Marshal(m.Facts)
	if _, err := h.s.db.Exec(`UPDATE nodes SET facts = ?, last_seen = ? WHERE id = ?`, string(facts), sqlitedb.Stamp(now), m.Node); err != nil {
		return nil, err
	}
	nc := &conn{h: h, id: m.Node, ws: ws, name: name, c: c, facts: *m.Facts, jobs: m.Jobs, calls: map[uint64]*call{}, done: make(chan struct{})}
	if nc.jobs == nil {
		nc.jobs = []wire.Job{}
	}
	if err := wsjson.Write(ctx, c, wire.Msg{Type: wire.TWelcome, Name: name}); err != nil {
		return nil, err
	}
	h.mu.Lock()
	old := h.conns[m.Node]
	h.conns[m.Node] = nc
	h.mu.Unlock()
	if old != nil {
		old.c.Close(websocket.StatusNormalClosure, "replaced by a newer connection")
	}
	h.notify(ws, Notice{ID: nc.id, State: Online, LastSeen: &now, Facts: m.Facts, Jobs: nc.jobs})
	return nc, nil
}

// conn is a connected node.
type conn struct {
	h      *Hub
	id     int64
	ws     string
	c      *websocket.Conn
	done   chan struct{} // closed when the connection ends
	mu     sync.Mutex
	name   string
	facts  wire.Facts
	jobs   []wire.Job
	lastID uint64
	calls  map[uint64]*call
}

// call is one request's answers, in order: messages and binary data.
type call struct {
	id   uint64
	in   chan item
	gone chan struct{} // closed when the requester is done
	once sync.Once
}

type item struct {
	msg  *wire.Msg
	data []byte
}

func (c *conn) run(ctx context.Context) {
	defer c.h.gone(c)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { // a dead link is noticed within a minute
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				pctx, pc := context.WithTimeout(ctx, 20*time.Second)
				err := c.c.Ping(pctx)
				pc()
				if err != nil {
					c.c.CloseNow()
					return
				}
			}
		}
	}()
	for {
		typ, data, err := c.c.Read(ctx)
		if err != nil {
			return
		}
		if typ == websocket.MessageBinary {
			id, chunk, err := wire.Unframe(data)
			if err != nil {
				return
			}
			c.deliver(id, item{data: chunk})
			continue
		}
		var m wire.Msg
		if err := json.Unmarshal(data, &m); err != nil {
			return
		}
		switch m.Type {
		case wire.TJobs:
			c.mu.Lock()
			c.jobs = m.Jobs
			if c.jobs == nil {
				c.jobs = []wire.Job{}
			}
			jobs := append([]wire.Job{}, c.jobs...)
			facts := c.facts
			c.mu.Unlock()
			c.h.notify(c.ws, Notice{ID: c.id, State: Online, Facts: &facts, Jobs: jobs})
		case wire.TPing, wire.TPong:
		default:
			if m.ID != 0 {
				c.deliver(m.ID, item{msg: &m})
			}
		}
	}
}

// deliver hands an answer to its request; a slow requester holds the
// connection back (that is the flow control of copies).
func (c *conn) deliver(id uint64, it item) {
	c.mu.Lock()
	cl := c.calls[id]
	c.mu.Unlock()
	if cl == nil {
		return
	}
	select {
	case cl.in <- it:
	case <-cl.gone:
	case <-c.done:
	}
}

// gone forgets a connection that ended.
func (h *Hub) gone(c *conn) {
	close(c.done)
	h.mu.Lock()
	current := h.conns[c.id] == c
	if current {
		delete(h.conns, c.id)
	}
	h.mu.Unlock()
	if !current {
		return
	}
	now := h.s.now()
	_, _ = h.s.db.Exec(`UPDATE nodes SET last_seen = ? WHERE id = ?`, sqlitedb.Stamp(now), c.id)
	h.notify(c.ws, Notice{ID: c.id, State: Offline, LastSeen: &now, Jobs: []wire.Job{}})
}

func (c *conn) send(ctx context.Context, m wire.Msg) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	return wsjson.Write(ctx, c.c, m)
}

func (c *conn) request(ctx context.Context, m wire.Msg) (*call, error) {
	c.mu.Lock()
	c.lastID++
	cl := &call{id: c.lastID, in: make(chan item, 32), gone: make(chan struct{})}
	c.calls[cl.id] = cl
	c.mu.Unlock()
	m.ID = cl.id
	if err := c.send(ctx, m); err != nil {
		c.finish(cl)
		return nil, c.offline(err)
	}
	return cl, nil
}

func (c *conn) finish(cl *call) {
	cl.once.Do(func() {
		c.mu.Lock()
		delete(c.calls, cl.id)
		c.mu.Unlock()
		close(cl.gone)
	})
}

// next waits for a request's next answer. If ctx ends first, the node is
// told to stop the request.
func (c *conn) next(ctx context.Context, cl *call) (item, error) {
	select {
	case it := <-cl.in:
		return it, nil
	default:
	}
	select {
	case it := <-cl.in:
		return it, nil
	case <-c.done:
		return item{}, c.offline(nil)
	case <-ctx.Done():
		_ = c.send(context.Background(), wire.Msg{Type: wire.TCancel, ID: cl.id})
		return item{}, ctx.Err()
	}
}

func (c *conn) offline(err error) error {
	select {
	case <-c.done:
		c.mu.Lock()
		name := c.name
		c.mu.Unlock()
		return fmt.Errorf("%s went offline", name)
	default:
	}
	return err
}

// get finds a connected node.
func (h *Hub) get(id int64) (*conn, error) {
	h.mu.Lock()
	c := h.conns[id]
	h.mu.Unlock()
	if c == nil {
		var name string
		_ = h.s.db.QueryRow(`SELECT name FROM nodes WHERE id = ?`, id).Scan(&name)
		if name == "" {
			name = fmt.Sprintf("node %d", id)
		}
		return nil, fmt.Errorf("%s is offline", name)
	}
	return c, nil
}

// ask sends a request that has one answer.
func (h *Hub) ask(ctx context.Context, id int64, m wire.Msg) (wire.Msg, error) {
	c, err := h.get(id)
	if err != nil {
		return wire.Msg{}, err
	}
	cl, err := c.request(ctx, m)
	if err != nil {
		return wire.Msg{}, err
	}
	defer c.finish(cl)
	for {
		it, err := c.next(ctx, cl)
		if err != nil {
			return wire.Msg{}, err
		}
		if it.msg == nil {
			continue
		}
		if it.msg.Type == wire.TError {
			return wire.Msg{}, errors.New(it.msg.Error)
		}
		return *it.msg, nil
	}
}

// exec starts a command; it answers when the command ends or wait passes.
func (h *Hub) exec(ctx context.Context, id int64, command, dir string, wait time.Duration) (wire.Msg, error) {
	ctx, cancel := context.WithTimeout(ctx, wait+time.Minute)
	defer cancel()
	return h.ask(ctx, id, wire.Msg{Type: wire.TExec, Command: command, Dir: dir, Wait: int(wait / time.Millisecond)})
}

// jobAction reads, waits for or stops a job ("" JobID: lists the jobs).
func (h *Hub) jobAction(id int64, m wire.Msg) (wire.Msg, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(wire.MaxWait)*time.Millisecond+time.Minute)
	defer cancel()
	m.Type = wire.TJob
	return h.ask(ctx, id, m)
}

// download reads a file (or a folder's tar) from a node. Read returns the
// bytes as they come and, at the end, an error unless their SHA-256 matches
// the node's.
type download struct {
	ctx  context.Context
	c    *conn
	cl   *call
	Meta wire.Msg
	h    hashWriter
	n    int64
	buf  []byte
	err  error
	Sum  string // set at a good end
}

type hashWriter interface {
	io.Writer
	Sum([]byte) []byte
}

func (h *Hub) open(ctx context.Context, id int64, path string) (*download, error) {
	c, err := h.get(id)
	if err != nil {
		return nil, err
	}
	cl, err := c.request(ctx, wire.Msg{Type: wire.TGet, Path: path})
	if err != nil {
		return nil, err
	}
	it, err := c.next(ctx, cl)
	if err != nil {
		c.finish(cl)
		return nil, err
	}
	if it.msg == nil || it.msg.Type != wire.TMeta {
		c.finish(cl)
		if it.msg != nil && it.msg.Type == wire.TError {
			return nil, errors.New(it.msg.Error)
		}
		return nil, errors.New("the node answered a copy out of order")
	}
	return &download{ctx: ctx, c: c, cl: cl, Meta: *it.msg, h: sha256.New()}, nil
}

func (d *download) Read(p []byte) (int, error) {
	for len(d.buf) == 0 {
		if d.err != nil {
			return 0, d.err
		}
		it, err := d.c.next(d.ctx, d.cl)
		switch {
		case err != nil:
			d.err = err
		case it.msg == nil:
			d.n += int64(len(it.data))
			if d.n > wire.MaxCopy {
				d.err = fmt.Errorf("copy is larger than the limit (%d MiB)", wire.MaxCopy>>20)
				break
			}
			d.h.Write(it.data)
			d.buf = it.data
		case it.msg.Type == wire.TEOF:
			sum := hex.EncodeToString(d.h.Sum(nil))
			if sum != it.msg.SHA256 {
				d.err = fmt.Errorf("SHA-256 mismatch: received %s, the node sent %s", sum, it.msg.SHA256)
			} else {
				d.Sum, d.err = sum, io.EOF
			}
		case it.msg.Type == wire.TError:
			d.err = errors.New(it.msg.Error)
		default:
			d.err = fmt.Errorf("the node sent %q in the middle of a copy", it.msg.Type)
		}
	}
	n := copy(p, d.buf)
	d.buf = d.buf[n:]
	return n, nil
}

// Close ends the download; one cut short is cancelled on the node.
func (d *download) Close() {
	if d.err != io.EOF {
		_ = d.c.send(context.Background(), wire.Msg{Type: wire.TCancel, ID: d.cl.id})
	}
	d.c.finish(d.cl)
}

// put sends a file (or a folder's tar) to a node. It answers the node's
// done: the size and SHA-256 it wrote, checked against what was sent.
func (h *Hub) put(ctx context.Context, id int64, path string, mode uint32, tar bool, r io.Reader) (wire.Msg, error) {
	c, err := h.get(id)
	if err != nil {
		return wire.Msg{}, err
	}
	cl, err := c.request(ctx, wire.Msg{Type: wire.TPut, Path: path, Mode: mode, Tar: tar})
	if err != nil {
		return wire.Msg{}, err
	}
	defer c.finish(cl)
	sum := sha256.New()
	buf := make([]byte, wire.Chunk)
	var (
		n       int64
		readErr error
	)
	answer := func(it item) (wire.Msg, error) {
		if it.msg != nil && it.msg.Type == wire.TError {
			return wire.Msg{}, errors.New(it.msg.Error)
		}
		if it.msg == nil || it.msg.Type != wire.TDone {
			return wire.Msg{}, errors.New("the node answered a copy out of order")
		}
		return *it.msg, nil
	}
send:
	for {
		select { // the node refused (bad path, full disk): stop sending
		case it := <-cl.in:
			m, err := answer(it)
			if err == nil {
				err = errors.New("the node answered before the copy ended")
			}
			_ = c.send(ctx, wire.Msg{Type: wire.TEOF, ID: cl.id, Error: "cancelled"})
			return m, err
		default:
		}
		k, err := io.ReadFull(r, buf)
		if k > 0 {
			n += int64(k)
			if n > wire.MaxCopy {
				readErr = fmt.Errorf("copy is larger than the limit (%d MiB)", wire.MaxCopy>>20)
				break send
			}
			sum.Write(buf[:k])
			wctx, cancel := context.WithTimeout(ctx, time.Minute)
			err := c.c.Write(wctx, websocket.MessageBinary, wire.Frame(cl.id, buf[:k]))
			cancel()
			if err != nil {
				return wire.Msg{}, c.offline(err)
			}
		}
		switch {
		case err == io.EOF || err == io.ErrUnexpectedEOF:
			break send
		case err != nil:
			readErr = err
			break send
		}
	}
	want := hex.EncodeToString(sum.Sum(nil))
	eof := wire.Msg{Type: wire.TEOF, ID: cl.id, SHA256: want, Size: n}
	if readErr != nil {
		eof.Error = readErr.Error()
	}
	if err := c.send(ctx, eof); err != nil {
		return wire.Msg{}, c.offline(err)
	}
	if readErr != nil {
		return wire.Msg{}, readErr
	}
	it, err := c.next(ctx, cl)
	if err != nil {
		return wire.Msg{}, err
	}
	m, err := answer(it)
	if err != nil {
		return m, err
	}
	if m.SHA256 != want {
		return m, fmt.Errorf("SHA-256 mismatch: sent %s, the node wrote %s", want, m.SHA256)
	}
	return m, nil
}

// jobsOf is the node's jobs as last reported.
func (h *Hub) jobsOf(id int64) []wire.Job {
	l, _ := h.live(id)
	return slices.Clone(l.jobs)
}
