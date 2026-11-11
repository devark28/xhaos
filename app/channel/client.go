package channel

// What an MCP client (package mcpserver) does with channels, on the owner's
// behalf: read them, post into them under its token's name, and open a
// channel with one agent. These take no acting agent, so they are on
// Service, which is never bound to the page.

import (
	"errors"
	"fmt"
	"strings"

	"github.com/devark28/xhaos/app/agent"
	"github.com/devark28/xhaos/app/apperr"
)

// ViaMCP marks a message an MCP client posted (Message.Via).
const ViaMCP = "mcp"

// KindClient is a channel an MCP client opened with one agent.
const KindClient = "mcp"

// List lists a workspace's channels.
func (s *Service) List(ws string) ([]Channel, error) { return s.channelsOf(ws, 0) }

// Get loads one channel.
func (s *Service) Get(ws string, id int64) (Channel, error) { return s.load(s.db, ws, id) }

// MessagesAfter lists up to limit messages with ids above after, oldest
// first (the latest ones when there are more).
func (s *Service) MessagesAfter(ws string, id, after int64, limit int) ([]Message, error) {
	if _, err := s.load(s.db, ws, id); err != nil {
		return nil, err
	}
	return s.messagesAfter(ws, id, after, limit)
}

// Count is how many messages a channel has, and the id of its last one.
func (s *Service) Count(ws string, id int64) (n int, last int64, err error) {
	err = s.db.QueryRow(`SELECT COUNT(*), COALESCE(MAX(m.id), 0) FROM channel_messages m JOIN channels c ON c.id = m.channel_id
		WHERE c.workspace_id = ? AND m.channel_id = ?`, ws, id).Scan(&n, &last)
	return n, last, err
}

// PostFromClient posts a message from an MCP client, named by its token,
// into channel id: every member gets it and wakes, as with any message.
func (s *Service) PostFromClient(ws string, id int64, client, body string) (Message, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return Message{}, fmt.Errorf("%w: a message needs content", apperr.Invalid)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Message{}, err
	}
	defer tx.Rollback()
	c, err := s.load(tx, ws, id)
	if err != nil {
		return Message{}, err
	}
	header := fmt.Sprintf("[channel #%d %q] message from %q, an MCP client acting for the user (it reads this channel; answer it with channel_send to channel #%d):", c.ID, c.Subject, client, c.ID)
	return s.deliverVia(tx, c, 0, client, ViaMCP, header, body)
}

// OpenForClient opens a channel between an MCP client and one top-level or
// sub agent: the agent is its creator and only member, and the client reads
// it and posts into it (PostFromClient).
func (s *Service) OpenForClient(a agent.Agent, subject string) (Channel, error) {
	if a.Status == agent.StatusTerminated {
		return Channel{}, fmt.Errorf("%w: agent #%d is terminated", apperr.Conflict, a.ID)
	}
	if strings.TrimSpace(subject) == "" {
		return Channel{}, errors.New("a channel needs a subject")
	}
	return s.Create(a, subject, nil, KindClient)
}
