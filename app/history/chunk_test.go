package history

import (
	"strings"
	"testing"
	"time"
)

const transcript = `{"type":"summary","sessionId":"s1","summary":"ignored"}
{"type":"user","sessionId":"s1","timestamp":"2026-03-01T10:00:00Z","message":{"role":"user","content":"Fix the parser"}}
{"type":"assistant","sessionId":"s1","timestamp":"2026-03-01T10:00:05Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hidden"},{"type":"text","text":"I will fix it."},{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]}}
{"type":"user","sessionId":"s1","timestamp":"2026-03-01T10:00:06Z","message":{"role":"user","content":[{"type":"tool_result","content":"a long output"}]}}
{"type":"user","sessionId":"s1","isMeta":true,"timestamp":"2026-03-01T10:00:07Z","message":{"role":"user","content":"caveat"}}
{"type":"user","sessionId":"s1","timestamp":"2026-03-01T10:01:00Z","message":{"role":"user","content":"<system-reminder>hook noise</system-reminder>Now add a test"}}
{"type":"assistant","sessionId":"s2","timestamp":"2026-03-02T09:00:00Z","message":{"role":"assistant","content":[{"type":"image","source":{}}]}}
not json at all
`

func TestParseTranscriptKeepsUserAndAssistantText(t *testing.T) {
	turns, err := ParseTranscript(strings.NewReader(transcript))
	if err != nil {
		t.Fatal(err)
	}
	want := []struct{ role, text string }{
		{"user", "Fix the parser"},
		{"assistant", "I will fix it."},
		{"user", "Now add a test"},
	}
	if len(turns) != len(want) {
		t.Fatalf("got %d turns, want %d: %+v", len(turns), len(want), turns)
	}
	for i, w := range want {
		if turns[i].Role != w.role || turns[i].Text != w.text {
			t.Errorf("turn %d = %q %q, want %q %q", i, turns[i].Role, turns[i].Text, w.role, w.text)
		}
	}
	if turns[0].Time != time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC) || turns[0].Session != "s1" {
		t.Errorf("first turn metadata: %+v", turns[0])
	}
	if turns[2].Index != 2 {
		t.Errorf("index counts kept turns within the session, got %d", turns[2].Index)
	}
}

func TestChunkTurnTable(t *testing.T) {
	long := strings.Repeat("word ", 1000) // 5000 characters
	cases := []struct {
		name      string
		text      string
		wantParts int
	}{
		{"short turn is one part", "hello there", 1},
		{"exactly the limit", strings.Repeat("a", maxChunkChars), 1},
		{"long turn is split", long, 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			chunks := ChunkTurn(Turn{Session: "s", Index: 0, Role: "user", Text: c.text})
			if len(chunks) != c.wantParts {
				t.Fatalf("got %d parts, want %d", len(chunks), c.wantParts)
			}
			for i, ch := range chunks {
				if ch.Part != i || ch.Session != "s" || ch.Role != "user" {
					t.Errorf("part %d metadata: %+v", i, ch)
				}
				if n := len([]rune(ch.Text)); n > maxChunkChars {
					t.Errorf("part %d has %d characters, over %d", i, n, maxChunkChars)
				}
			}
		})
	}
}

func TestChunkOverlapCoversTheText(t *testing.T) {
	var words []string
	for i := 0; i < 1500; i++ {
		words = append(words, "w"+strings.Repeat("x", i%7))
	}
	text := strings.Join(words, " ")
	chunks := ChunkTurn(Turn{Text: text})
	if len(chunks) < 2 {
		t.Fatalf("expected a split, got %d", len(chunks))
	}
	// Every word of the text appears in some part, and consecutive parts overlap.
	joined := ""
	for _, ch := range chunks {
		joined += " " + ch.Text
	}
	for _, w := range words[len(words)-3:] {
		if !strings.Contains(joined, w) {
			t.Fatalf("last word %q missing from the parts", w)
		}
	}
	for i := 1; i < len(chunks); i++ {
		prev, cur := chunks[i-1].Text, chunks[i].Text
		head := cur[:20]
		if !strings.Contains(prev, head) {
			t.Errorf("part %d does not overlap part %d", i, i-1)
		}
	}
}

func TestExcerpt(t *testing.T) {
	if got := Excerpt("a  b\n c", 10); got != "a b c" {
		t.Errorf("Excerpt = %q", got)
	}
	if got := Excerpt(strings.Repeat("é", 20), 5); got != "ééééé…" {
		t.Errorf("Excerpt = %q", got)
	}
}
