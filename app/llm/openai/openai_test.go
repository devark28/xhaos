package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"xhaos/app/llm"
)

// server answers /chat/completions with the given SSE lines and records the
// last request body; /models lists two models.
func server(t *testing.T, lines ...string) (*httptest.Server, *map[string]any, *http.Header) {
	t.Helper()
	var got map[string]any
	var headers http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers = r.Header.Clone()
		switch r.URL.Path {
		case "/v1/models":
			fmt.Fprint(w, `{"data":[{"id":"m-b"},{"id":"m-a"}]}`)
		case "/v1/chat/completions":
			body, _ := io.ReadAll(r.Body)
			json.Unmarshal(body, &got)
			w.Header().Set("Content-Type", "text/event-stream")
			for _, l := range lines {
				fmt.Fprintf(w, "data: %s\n\n", l)
			}
			fmt.Fprint(w, "data: [DONE]\n\n")
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":{"message":"no such route"}}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &got, &headers
}

func TestStreamsTextAndUsage(t *testing.T) {
	srv, _, headers := server(t,
		`{"choices":[{"delta":{"content":"Hel"}}]}`,
		`{"choices":[{"delta":{"content":"lo"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":7,"completion_tokens":2}}`)
	var deltas []string
	res, err := Adapter{}.Stream(context.Background(), llm.Credentials{APIKey: "k", BaseURL: srv.URL + "/v1/"}, llm.Turn{Model: "m", System: "sys", Messages: []llm.Message{{Role: "user", Text: "hi"}}}, func(s string) { deltas = append(deltas, s) })
	if err != nil || res.Text != "Hello" || res.StopReason != "end_turn" || res.InputTokens != 7 || res.OutputTokens != 2 {
		t.Fatalf("result = %+v, %v", res, err)
	}
	if strings.Join(deltas, "|") != "Hel|lo" {
		t.Fatalf("deltas = %v", deltas)
	}
	if (*headers).Get("Authorization") != "Bearer k" {
		t.Fatal("key not sent as a bearer token")
	}
}

func TestToolCallsStreamInPiecesAndReplay(t *testing.T) {
	srv, got, headers := server(t,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"read_file","arguments":""}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"bash","arguments":"{\"comm"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.txt\"}"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"and\":\"ls\"}"}}]},"finish_reason":"tool_calls"}]}`)
	turn := llm.Turn{
		Model: "m",
		Tools: []llm.Tool{{Name: "read_file", Description: "Read", Schema: llm.ToolSchema{Properties: map[string]any{"path": map[string]any{"type": "string"}}, Required: []string{"path"}}}},
		Messages: []llm.Message{
			{Role: "user", Text: "go"},
			{Role: "assistant", Text: "Looking.", Calls: []llm.ToolCall{{ID: "old", Name: "list_dir", Input: json.RawMessage(`{}`)}}},
			{Role: "user", Results: []llm.ToolResult{{CallID: "old", Content: "denied", IsError: true}}, Text: "also this"},
		},
	}
	res, err := Adapter{}.Stream(context.Background(), llm.Credentials{BaseURL: srv.URL + "/v1"}, turn, func(string) {})
	if err != nil || res.StopReason != "tool_use" || len(res.Calls) != 2 {
		t.Fatalf("result = %+v, %v", res, err)
	}
	if res.Calls[0].ID != "call_a" || res.Calls[0].Name != "read_file" || string(res.Calls[0].Input) != `{"path":"a.txt"}` {
		t.Fatalf("call 0 = %+v (%s)", res.Calls[0], res.Calls[0].Input)
	}
	if res.Calls[1].Name != "bash" || string(res.Calls[1].Input) != `{"command":"ls"}` {
		t.Fatalf("call 1 = %+v (%s)", res.Calls[1], res.Calls[1].Input)
	}
	if (*headers).Get("Authorization") != "" {
		t.Fatal("a keyless provider sent an Authorization header")
	}

	// The request replays the history in Chat Completions form.
	msgs := (*got)["messages"].([]any)
	roles := []string{}
	for _, m := range msgs {
		roles = append(roles, m.(map[string]any)["role"].(string))
	}
	if strings.Join(roles, ",") != "user,assistant,tool,user" {
		t.Fatalf("roles = %v", roles)
	}
	assistant := msgs[1].(map[string]any)
	call := assistant["tool_calls"].([]any)[0].(map[string]any)
	if assistant["content"] != "Looking." || call["id"] != "old" || call["function"].(map[string]any)["arguments"] != "{}" {
		t.Fatalf("assistant replay = %v", assistant)
	}
	toolMsg := msgs[2].(map[string]any)
	if toolMsg["tool_call_id"] != "old" || toolMsg["content"] != "Error: denied" {
		t.Fatalf("tool result = %v", toolMsg)
	}
	tools := (*got)["tools"].([]any)
	fn := tools[0].(map[string]any)["function"].(map[string]any)
	params := fn["parameters"].(map[string]any)
	if fn["name"] != "read_file" || params["type"] != "object" || params["required"].([]any)[0] != "path" {
		t.Fatalf("tools = %v", tools)
	}
}

func TestMalformedArgumentsAreWrapped(t *testing.T) {
	if got := string(arguments(`{"path": "a`)); got != `{"invalid_arguments":"{\"path\": \"a"}` {
		t.Fatalf("arguments = %s", got)
	}
	if got := string(arguments("")); got != "{}" {
		t.Fatalf("empty arguments = %s", got)
	}
}

func TestModelsAndErrors(t *testing.T) {
	srv, _, _ := server(t)
	models, err := Adapter{}.Models(context.Background(), llm.Credentials{BaseURL: srv.URL + "/v1"})
	if err != nil || strings.Join(models, ",") != "m-a,m-b" {
		t.Fatalf("models = %v, %v", models, err)
	}
	_, err = Adapter{}.Models(context.Background(), llm.Credentials{BaseURL: srv.URL + "/nope"})
	if err == nil || !strings.Contains(err.Error(), "no such route") {
		t.Fatalf("error = %v", err)
	}
	errSrv, _, _ := server(t, `{"error":{"message":"model overloaded"}}`)
	if _, err := (Adapter{}).Stream(context.Background(), llm.Credentials{BaseURL: errSrv.URL + "/v1"}, llm.Turn{Model: "m"}, func(string) {}); err == nil || err.Error() != "model overloaded" {
		t.Fatalf("stream error = %v", err)
	}
}
