package channel

// Published channels: the user puts a channel on a link,
// /xhaos/channel/<workspace>/<channel>, and gets a passphrase, shown once.
// With it, a program outside Xhaos (a script, another Claude Code session)
// reads the channel and sends messages into it; the channel's agents get
// them like any channel message, marked as coming from outside.
//
// The passphrase is random (192 bits), so it is stored as a plain SHA-256
// hash: there is nothing to guess and nothing to salt. Publishing again
// always replaces it (and the expiry); unpublishing deletes it.
//
// A link lasts as long as the owner chose when publishing, or has no expiry
// (expires_at NULL, which must be chosen explicitly). An expired link is
// refused like a wrong passphrase, but its row stays, so the channel still
// shows as published ("link expired") until the owner publishes again or
// unpublishes.

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/devark28/xhaos/app/apperr"
	"github.com/devark28/xhaos/app/auth"
	"github.com/devark28/xhaos/app/danger"
	"github.com/devark28/xhaos/app/eventlog"
	"github.com/devark28/xhaos/app/sqlitedb"
)

const linkSchema = `
CREATE TABLE IF NOT EXISTS channel_links (
	channel_id INTEGER PRIMARY KEY REFERENCES channels(id),
	pass_hash  TEXT NOT NULL,
	created_at TEXT NOT NULL,
	expires_at TEXT -- NULL: no expiry
);`

// migrateLinks adds the links table, and lets channel messages come from
// outside: older databases required every sender to be an agent, so the
// messages table is rebuilt once with sender_id nullable.
func migrateLinks(db *sql.DB) error {
	if _, err := db.Exec(linkSchema); err != nil {
		return err
	}
	// Older databases have no expires_at: the column is added NULL, so links
	// already published keep working without an expiry.
	if _, err := db.Exec(`ALTER TABLE channel_links ADD COLUMN expires_at TEXT`); err != nil && !strings.Contains(err.Error(), "duplicate column") {
		return err
	}
	var has int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('channel_messages') WHERE name = 'sender_name'`).Scan(&has); err != nil || has > 0 {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`CREATE TABLE channel_messages_new (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			channel_id  INTEGER NOT NULL REFERENCES channels(id),
			sender_id   INTEGER REFERENCES agents(id),
			sender_name TEXT NOT NULL DEFAULT '',
			body        TEXT NOT NULL,
			created_at  TEXT NOT NULL
		)`,
		`INSERT INTO channel_messages_new (id, channel_id, sender_id, sender_name, body, created_at)
			SELECT id, channel_id, sender_id, '', body, created_at FROM channel_messages`,
		`DROP TABLE channel_messages`,
		`ALTER TABLE channel_messages_new RENAME TO channel_messages`,
	} {
		if _, err := tx.Exec(q); err != nil {
			return fmt.Errorf("channel messages migration: %w", err)
		}
	}
	return tx.Commit()
}

const passPrefix = "xhc_"

func hashPass(p string) string { s := sha256.Sum256([]byte(p)); return hex.EncodeToString(s[:]) }

// LinkPath is where a channel's link takes requests.
func LinkPath(ws string, id int64) string { return fmt.Sprintf("%s%s/%d", auth.LinkPrefix, ws, id) }

// Published is what Publish returns: the passphrase, this once.
type Published struct {
	Channel    Channel `json:"channel"`
	Path       string  `json:"path"`
	Passphrase string  `json:"passphrase"`
}

// A link's own duration range; "no expiry" is the only way past the top.
const (
	MinLinkDuration = 10 * time.Minute
	MaxLinkDuration = 30 * 24 * time.Hour
)

// PublishRequest publishes a channel (or publishes it again). Duration is how
// long the link works, <n><unit> like 1h or 7d (10 minutes to 30 days);
// NoExpiry: it never expires. Exactly one of the two must be given.
type PublishRequest struct {
	WorkspaceID string `json:"workspaceId"`
	ChannelID   int64  `json:"channelId"`
	Duration    string `json:"duration,omitempty"`
	NoExpiry    bool   `json:"noExpiry,omitempty"`
}

// issueLink gives a channel a new passphrase and expiry (the old ones, if
// any, stop working); no expiry when expiry is zero.
func (s *Service) issueLink(ws string, id int64, expiry time.Duration) (Published, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Published{}, err
	}
	defer tx.Rollback()
	c, err := s.load(tx, ws, id)
	if err != nil {
		return Published{}, err
	}
	if c.Closed {
		return Published{}, fmt.Errorf("%w: channel #%d is closed", apperr.Invalid, id)
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return Published{}, err
	}
	pass := passPrefix + base64.RawURLEncoding.EncodeToString(raw)
	at := s.now()
	var expires any // NULL: no expiry
	c.ExpiresAt = nil
	if expiry > 0 {
		t := at.Add(expiry)
		c.ExpiresAt = &t
		expires = sqlitedb.Stamp(t)
	}
	if _, err := tx.Exec(`INSERT INTO channel_links (channel_id, pass_hash, created_at, expires_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (channel_id) DO UPDATE SET pass_hash = excluded.pass_hash, created_at = excluded.created_at, expires_at = excluded.expires_at`,
		id, hashPass(pass), sqlitedb.Stamp(at), expires); err != nil {
		return Published{}, err
	}
	c.Published = true
	ev, err := eventlog.Append(tx, ws, 0, EventLink, c, at)
	if err != nil {
		return Published{}, err
	}
	if err := tx.Commit(); err != nil {
		return Published{}, err
	}
	s.publish(ev)
	return Published{Channel: c, Path: LinkPath(ws, id), Passphrase: pass}, nil
}

func (s *Service) dropLink(ws string, id int64) (Channel, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Channel{}, err
	}
	defer tx.Rollback()
	c, err := s.load(tx, ws, id)
	if err != nil {
		return Channel{}, err
	}
	if !c.Published {
		return c, nil
	}
	if _, err := tx.Exec(`DELETE FROM channel_links WHERE channel_id = ?`, id); err != nil {
		return Channel{}, err
	}
	c.Published, c.ExpiresAt = false, nil
	ev, err := eventlog.Append(tx, ws, 0, EventLink, c, s.now())
	if err != nil {
		return Channel{}, err
	}
	if err := tx.Commit(); err != nil {
		return Channel{}, err
	}
	s.publish(ev)
	return c, nil
}

// Publish puts a channel on its link with a new passphrase and expiry; the
// passphrase is returned this once (only its hash is kept). On a published
// channel, expired or not, it is the same action again: the old passphrase
// stops working at once.
func (o *Owner) Publish(req PublishRequest) (Published, error) {
	var expiry time.Duration
	switch {
	case req.NoExpiry && strings.TrimSpace(req.Duration) != "":
		return Published{}, fmt.Errorf("%w: give a duration or no expiry, not both", apperr.Invalid)
	case req.NoExpiry:
	case strings.TrimSpace(req.Duration) == "":
		return Published{}, fmt.Errorf("%w: choose how long the link lasts (a duration like 1d), or no expiry", apperr.Invalid)
	default:
		d, err := danger.ParseDurationBetween(req.Duration, MinLinkDuration, MaxLinkDuration)
		if err != nil {
			return Published{}, err
		}
		expiry = d
	}
	return o.s.issueLink(req.WorkspaceID, req.ChannelID, expiry)
}

// Unpublish takes a channel off its link; its passphrase stops working.
func (o *Owner) Unpublish(req CloseRequest) (Channel, error) {
	return o.s.dropLink(req.WorkspaceID, req.ChannelID)
}

var errPass = errors.New("wrong or missing passphrase, or the channel is not published")

// linked loads a published channel whose passphrase is pass and whose link
// has not expired (expiry is judged by the service's clock).
func (s *Service) linked(ws string, id int64, pass string) (Channel, error) {
	var stored string
	var expires sql.NullString
	err := s.db.QueryRow(`SELECT l.pass_hash, l.expires_at FROM channel_links l JOIN channels c ON c.id = l.channel_id WHERE c.workspace_id = ? AND c.id = ?`, ws, id).Scan(&stored, &expires)
	if err != nil || subtle.ConstantTimeCompare([]byte(stored), []byte(hashPass(pass))) != 1 {
		return Channel{}, errPass
	}
	if expires.Valid && !s.now().Before(sqlitedb.ParseStamp(expires.String)) {
		return Channel{}, errPass
	}
	return s.load(s.db, ws, id)
}

// sendOutside posts a message from outside Xhaos into a published channel.
func (s *Service) sendOutside(c Channel, from, body string) (Message, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return Message{}, errors.New("a message needs content")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Message{}, err
	}
	defer tx.Rollback()
	header := fmt.Sprintf("[channel #%d %q] message from outside Xhaos (%s), sent through the channel's link by someone with its passphrase:", c.ID, c.Subject, from)
	return s.deliver(tx, c, 0, from, header, body)
}

// ---- HTTP --------------------------------------------------------------------

// Links serves published channels at /xhaos/channel/<workspace>/<channel>:
//
//	GET  ?after=<message id>&wait=<seconds>   the channel and its messages after that id;
//	                                          wait (up to 60) holds the request until one arrives
//	POST {"from": "name", "body": "text"}     sends a message (a text/plain body works too)
//
// Both need "Authorization: Bearer <passphrase>". Wrong passphrases (and
// expired links, which are refused the same way) are counted per address and
// slowed down like sign-in attempts. A waiting GET checks the link again each
// half second, so a link that expires or is replaced stops it there.
func (s *Service) Links() http.Handler {
	var limits auth.Limiter
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, auth.LinkPrefix), "/")
		id, err := strconv.ParseInt(lastOr(parts, ""), 10, 64)
		if len(parts) != 2 || parts[0] == "" || err != nil {
			replyJSON(w, http.StatusNotFound, map[string]string{"error": "use " + auth.LinkPrefix + "<workspace>/<channel id>"})
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			replyJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET or POST"})
			return
		}
		addr := auth.ClientAddr(r)
		if d := limits.Wait(addr, time.Now()); d > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(d.Seconds())+1))
			replyJSON(w, http.StatusTooManyRequests, map[string]any{"error": "too many wrong passphrases; wait and try again", "retryAfter": int(d.Seconds()) + 1})
			return
		}
		pass, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		c, err := s.linked(parts[0], id, strings.TrimSpace(pass))
		if err != nil {
			limits.Fail(addr, time.Now())
			replyJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
			return
		}
		limits.Clear(addr)
		if r.Method == http.MethodPost {
			s.servePost(w, r, c)
			return
		}
		s.serveGet(w, r, c, strings.TrimSpace(pass))
	})
}

func lastOr(list []string, def string) string {
	if len(list) == 0 {
		return def
	}
	return list[len(list)-1]
}

func (s *Service) servePost(w http.ResponseWriter, r *http.Request, c Channel) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 256<<10+1))
	if err != nil || len(raw) > 256<<10 {
		replyJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "a message is at most 256 KB"})
		return
	}
	in := struct {
		From string `json:"from"`
		Body string `json:"body"`
	}{}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		if err := json.Unmarshal(raw, &in); err != nil {
			replyJSON(w, http.StatusBadRequest, map[string]string{"error": "body must be {\"from\": \"…\", \"body\": \"…\"}"})
			return
		}
	} else {
		in.Body, in.From = string(raw), r.Header.Get("X-From")
	}
	in.From = strings.Join(strings.Fields(in.From), " ")
	if in.From == "" {
		in.From = "unnamed"
	}
	if r := []rune(in.From); len(r) > 60 {
		in.From = string(r[:60])
	}
	msg, err := s.sendOutside(c, in.From, in.Body)
	if err != nil {
		replyJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	replyJSON(w, http.StatusCreated, map[string]any{"message": outsideView(msg, s, c.WorkspaceID)})
}

func (s *Service) serveGet(w http.ResponseWriter, r *http.Request, c Channel, pass string) {
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	wait, _ := strconv.Atoi(r.URL.Query().Get("wait"))
	deadline := time.Now().Add(time.Duration(min(max(wait, 0), 60)) * time.Second)
	if wait > 0 {
		_ = http.NewResponseController(w).SetWriteDeadline(deadline.Add(10 * time.Second))
	}
	for first := true; ; first = false {
		// The handler just checked the link; a wait checks it again on each
		// loop (not counted as a wrong try: the caller held a good passphrase).
		if !first {
			if _, err := s.linked(c.WorkspaceID, c.ID, pass); err != nil {
				replyJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
				return
			}
		}
		msgs, err := s.messagesAfter(c.WorkspaceID, c.ID, after, 200)
		if err != nil {
			replyJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if len(msgs) > 0 || !time.Now().Before(deadline) {
			out := []map[string]any{}
			for _, m := range msgs {
				out = append(out, outsideView(m, s, c.WorkspaceID))
			}
			members := []map[string]any{}
			for _, m := range c.Members {
				members = append(members, map[string]any{"id": m, "title": s.title(c.WorkspaceID, m)})
			}
			replyJSON(w, http.StatusOK, map[string]any{
				"channel":  map[string]any{"id": c.ID, "subject": c.Subject, "closed": c.Closed, "members": members},
				"messages": out,
			})
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// outsideView is a message as the link shows it.
func outsideView(m Message, s *Service, ws string) map[string]any {
	v := map[string]any{"id": m.ID, "body": m.Body, "createdAt": m.CreatedAt}
	if m.SenderID == 0 {
		v["from"] = map[string]any{"outside": m.SenderName}
	} else {
		v["from"] = map[string]any{"agent": m.SenderID, "title": s.title(ws, m.SenderID)}
	}
	return v
}

func (s *Service) title(ws string, id int64) string {
	var t string
	_ = s.db.QueryRow(`SELECT title FROM agents WHERE workspace_id = ? AND id = ?`, ws, id).Scan(&t)
	return t
}

func replyJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
