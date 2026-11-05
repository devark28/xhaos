package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"xhaos/app/llm"
)

const sse = `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":12,"cache_read_input_tokens":300,"cache_creation_input_tokens":40,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-abc"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Hello"}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":" world"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":7}}

event: message_stop
data: {"type":"message_stop"}

`

func fakeAPI(t *testing.T) (*httptest.Server, *[]string) {
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The SDK sends the key as a bearer token; accept x-api-key too.
		if r.Header.Get("Authorization") != "Bearer sk-test" && r.Header.Get("X-Api-Key") != "sk-test" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`)
			return
		}
		switch r.URL.Path {
		case "/v1/messages":
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			bodies = append(bodies, string(body))
			mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, sse)
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"data":[{"id":"claude-opus-5-5","type":"model","display_name":"Claude Opus 5.5","created_at":"2026-01-01T00:00:00Z"}],"has_more":false,"first_id":"claude-opus-5-5","last_id":"claude-opus-5-5"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &bodies
}

func TestStreamThenReplayVerbatim(t *testing.T) {
	srv, bodies := fakeAPI(t)
	creds := llm.Credentials{APIKey: "sk-test", BaseURL: srv.URL}
	a := Adapter{}
	var deltas []string
	turn := llm.Turn{Model: "claude-opus-5-5", System: "You are agent #1.", Messages: []llm.Message{{Role: "user", Text: "Hi"}}}
	res, err := a.Stream(context.Background(), creds, turn, func(d string) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "Hello world" || strings.Join(deltas, "") != "Hello world" || res.StopReason != "end_turn" || res.OutputTokens != 7 {
		t.Fatalf("result = %+v, deltas %q", res, deltas)
	}
	if !strings.Contains(string(res.Raw), "sig-abc") {
		t.Fatalf("raw reply lost the thinking signature: %s", res.Raw)
	}
	first := (*bodies)[0]
	for _, want := range []string{`"model":"claude-opus-5-5"`, `"stream":true`, `"effort":"high"`, `You are agent #1.`} {
		if !strings.Contains(first, want) {
			t.Errorf("first request missing %s: %s", want, first)
		}
	}

	turn.Messages = append(turn.Messages,
		llm.Message{Role: "assistant", Text: res.Text, RawKind: Kind, Raw: res.Raw},
		llm.Message{Role: "user", Text: "And then?"})
	if _, err := a.Stream(context.Background(), creds, turn, nil); err != nil {
		t.Fatal(err)
	}
	second := (*bodies)[1]
	if !strings.Contains(second, `"signature":"sig-abc"`) || !strings.Contains(second, `"type":"thinking"`) {
		t.Fatalf("second request did not replay the thinking block: %s", second)
	}
}

func TestModelsChecksKey(t *testing.T) {
	srv, _ := fakeAPI(t)
	ids, err := Adapter{}.Models(context.Background(), llm.Credentials{APIKey: "sk-test", BaseURL: srv.URL})
	if err != nil || len(ids) != 1 || ids[0] != "claude-opus-5-5" {
		t.Fatalf("Models = %v, %v", ids, err)
	}
	if _, err := (Adapter{}).Models(context.Background(), llm.Credentials{APIKey: "wrong", BaseURL: srv.URL}); err == nil {
		t.Fatal("a bad key must fail")
	}
}

func TestParamsStripThinkingOnlyWhenAsked(t *testing.T) {
	raw := json.RawMessage(`{"id":"m","type":"message","role":"assistant","model":"x","content":[{"type":"thinking","thinking":"hmm","signature":"sig"},{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	for _, strip := range []bool{false, true} {
		out, err := params([]llm.Message{{Role: "user", Text: "q"}, {Role: "assistant", Text: "hi", RawKind: Kind, Raw: raw, StripThinking: strip}})
		if err != nil {
			t.Fatal(err)
		}
		blocks := out[1].OfAssistant.Content
		hasThinking := false
		for _, b := range blocks {
			hasThinking = hasThinking || b.OfThinking != nil
		}
		if hasThinking == strip || blocks[len(blocks)-1].OfText == nil {
			t.Fatalf("strip=%v: blocks = %+v", strip, blocks)
		}
	}
}

func TestReplayedToolUseOmitsEmptyToolsetName(t *testing.T) {
	// What a stored reply looks like after the SDK round trip.
	raw := json.RawMessage(`{"id":"m","type":"message","role":"assistant","model":"x","content":[{"type":"tool_use","id":"toolu_1","name":"list_dir","input":{},"toolset_name":"","caller":{"type":"direct"}}],"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":1}}`)
	out, err := params([]llm.Message{{Role: "user", Text: "q"}, {Role: "assistant", RawKind: Kind, Raw: raw}})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(out[1])
	if strings.Contains(string(body), "toolset_name") || !strings.Contains(string(body), `"toolu_1"`) {
		t.Fatalf("replayed tool_use = %s", body)
	}
}

func TestFilesBecomeImageAndDocumentBlocks(t *testing.T) {
	ps, err := params([]llm.Message{{Role: "user", Text: "look", Files: []llm.File{
		{Name: "a.png", Type: "image/png", Data: []byte("PNG")}, {Name: "b.pdf", Type: "application/pdf", Data: []byte("%PDF")},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(ps)
	s := string(raw)
	if !strings.Contains(s, `"media_type":"image/png"`) || !strings.Contains(s, `"data":"UE5H"`) || !strings.Contains(s, `"type":"document"`) || !strings.Contains(s, `"data":"JVBERg=="`) {
		t.Fatalf("params = %s", s)
	}
}

// Agent turns ask for automatic prompt caching and count cached prompt
// tokens as input; a small side call (a title) asks for none.
func TestPromptCachingAndCacheCounts(t *testing.T) {
	srv, bodies := fakeAPI(t)
	creds := llm.Credentials{APIKey: "sk-test", BaseURL: srv.URL}
	turn := llm.Turn{Model: "claude-opus-5-5", System: "s", Messages: []llm.Message{{Role: "user", Text: "Hi"}}}
	res, err := Adapter{}.Stream(context.Background(), creds, turn, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains((*bodies)[0], `"cache_control":{"type":"ephemeral"}`) {
		t.Fatalf("no automatic cache breakpoint: %s", (*bodies)[0])
	}
	if res.InputTokens != res.CacheReadTokens+res.CacheWriteTokens+12 || res.CacheReadTokens != 300 || res.CacheWriteTokens != 40 {
		t.Fatalf("counts = %+v", res)
	}
	turn.Small = true
	if _, err := (Adapter{}).Stream(context.Background(), creds, turn, func(string) {}); err != nil {
		t.Fatal(err)
	}
	if b := (*bodies)[1]; strings.Contains(b, `"cache_control"`) || !strings.Contains(b, `"effort":"low"`) || !strings.Contains(b, `"max_tokens":1024`) {
		t.Fatalf("small call: %s", b)
	}
}
