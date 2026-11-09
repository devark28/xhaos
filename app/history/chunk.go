package history

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Turn is one kept message of a session: a user prompt or assistant text.
type Turn struct {
	Session string
	Time    time.Time
	Index   int // the turn's place in its session, counting kept turns from 0
	Role    string
	Text    string
}

// Chunk is a piece of a turn: what gets scrubbed, embedded and stored.
type Chunk struct {
	Session string
	Time    time.Time
	Turn    int
	Part    int // 0-based part of a long turn
	Role    string
	Text    string
	// Scrubbed: the turn this chunk came from had a secret removed. A long
	// turn marks all its parts, so the count is per turn, not per part.
	Scrubbed bool
}

const (
	// maxChunkChars is about 500 tokens of English text (4 characters a token).
	maxChunkChars = 2000
	// overlapChars repeats the end of a part at the start of the next one.
	overlapChars = 200
)

// line is one line of a Claude Code transcript (JSONL). Only the fields kept
// here are read; everything else (tool inputs and outputs, thinking, images,
// snapshots, summaries) is dropped.
type line struct {
	Type      string          `json:"type"`
	SessionID string          `json:"sessionId"`
	Timestamp string          `json:"timestamp"`
	IsMeta    bool            `json:"isMeta"`
	Message   json.RawMessage `json:"message"`
}

type message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type block struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

var reminder = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`)

// ParseTranscript reads one session transcript (JSONL) and returns its kept
// turns in order. Malformed lines are skipped; an error is returned only if
// reading itself fails.
func ParseTranscript(r io.Reader) ([]Turn, error) {
	var turns []Turn
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 64<<20) // a single line can hold a large tool output
	for sc.Scan() {
		t, ok := turnOf(sc.Bytes())
		if ok {
			t.Index = len(turns)
			turns = append(turns, t)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("transcript: %w", err)
	}
	// Index counts kept turns within a session, not the whole file.
	counts := map[string]int{}
	for i := range turns {
		turns[i].Index = counts[turns[i].Session]
		counts[turns[i].Session]++
	}
	return turns, nil
}

// turnOf turns one JSONL line into a kept turn, if it is a user or assistant
// message with text left after dropping what is not kept.
func turnOf(raw []byte) (Turn, bool) {
	var l line
	if err := json.Unmarshal(raw, &l); err != nil || l.IsMeta {
		return Turn{}, false
	}
	if l.Type != "user" && l.Type != "assistant" {
		return Turn{}, false
	}
	var m message
	if err := json.Unmarshal(l.Message, &m); err != nil || len(m.Content) == 0 {
		return Turn{}, false
	}
	text := textOf(m.Content)
	text = strings.TrimSpace(reminder.ReplaceAllString(text, ""))
	if text == "" {
		return Turn{}, false
	}
	role := m.Role
	if role == "" {
		role = l.Type
	}
	at, _ := time.Parse(time.RFC3339Nano, l.Timestamp)
	return Turn{Session: l.SessionID, Time: at, Role: role, Text: text}, true
}

// textOf returns the text of a message's content: a string, or the text
// blocks of a block list (tool_use, tool_result, thinking and images are
// dropped).
func textOf(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []block
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// ChunkTurn splits a turn into parts of at most about 500 tokens with a small
// overlap, cutting at a space where it can. Short turns are one part.
func ChunkTurn(t Turn) []Chunk {
	var out []Chunk
	rest := []rune(t.Text)
	for part := 0; len(rest) > 0; part++ {
		n := maxChunkChars
		cut := len(rest)
		if len(rest) > maxChunkChars {
			cut = n
			// Back up to a space in the last quarter of the window.
			for i := n; i > n*3/4; i-- {
				if rest[i] == ' ' || rest[i] == '\n' {
					cut = i
					break
				}
			}
		}
		text := strings.TrimSpace(string(rest[:cut]))
		if text != "" {
			out = append(out, Chunk{Session: t.Session, Time: t.Time, Turn: t.Index, Part: part, Role: t.Role, Text: text})
		}
		if cut == len(rest) {
			break
		}
		next := cut - overlapChars
		if next <= 0 {
			next = cut
		}
		rest = rest[next:]
	}
	return out
}

// ChunkTurns chunks every turn of a session in order.
func ChunkTurns(turns []Turn) []Chunk {
	var out []Chunk
	for _, t := range turns {
		out = append(out, ChunkTurn(t)...)
	}
	return out
}

// Excerpt returns the first n characters of s on one line.
func Excerpt(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}
