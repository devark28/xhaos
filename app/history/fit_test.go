package history

import (
	"context"
	"strings"
	"testing"
	"time"
)

// wordCounter stands in for the tokenizer: one token per word.
type wordCounter struct{ fakeEmbedder }

func (w *wordCounter) Count(_ context.Context, text string) (int, error) {
	return len(strings.Fields(text)), nil
}

// A chunk longer than the model takes is split into pieces that each fit, in
// order, and the parts of its turn are numbered from 0.
func TestFitSplitsLongChunksAndNumbersParts(t *testing.T) {
	words := make([]string, 0, 1200)
	for i := 0; i < 1200; i++ {
		words = append(words, "w"+string(rune('a'+i%26)))
	}
	long := strings.Join(words, " ")
	in := []Chunk{
		{Session: "s1", Time: time.Unix(0, 0), Turn: 4, Part: 0, Role: "user", Text: long},
		{Session: "s1", Time: time.Unix(0, 0), Turn: 4, Part: 1, Role: "user", Text: "short tail"},
		{Session: "s1", Time: time.Unix(0, 0), Turn: 5, Part: 0, Role: "assistant", Text: "next turn"},
	}
	out, err := fit(context.Background(), &wordCounter{fakeEmbedder{dim: 8}}, in)
	if err != nil {
		t.Fatal(err)
	}
	var parts []int
	var rebuilt []string
	for _, c := range out {
		if c.Turn == 4 {
			parts = append(parts, c.Part)
			if n := len(strings.Fields(c.Text)); n > maxPassageTokens {
				t.Fatalf("piece has %d tokens, over %d", n, maxPassageTokens)
			}
			rebuilt = append(rebuilt, c.Text)
		}
	}
	if len(parts) < 4 {
		t.Fatalf("1200 words should give at least 3 pieces plus the tail, got %d", len(parts))
	}
	for i, p := range parts {
		if p != i {
			t.Fatalf("parts of turn 4 are %v, want 0,1,2,...", parts)
		}
	}
	if got := strings.Join(strings.Fields(strings.Join(rebuilt[:len(rebuilt)-1], " ")), " "); !strings.HasPrefix(long, got) {
		t.Fatal("the pieces do not give back the text in order")
	}
	last := out[len(out)-1]
	if last.Turn != 5 || last.Part != 0 {
		t.Fatalf("the next turn's first part should restart at 0, got turn %d part %d", last.Turn, last.Part)
	}
}

// An embedder that cannot count tokens keeps the chunks as they are.
func TestFitWithoutACounterKeepsChunks(t *testing.T) {
	in := []Chunk{{Session: "s", Turn: 1, Part: 0, Text: strings.Repeat("word ", 3000)}}
	out, err := fit(context.Background(), &fakeEmbedder{dim: 8}, in)
	if err != nil || len(out) != 1 || out[0].Text != in[0].Text {
		t.Fatalf("fit changed the chunks without a counter: %d chunks, err %v", len(out), err)
	}
}
