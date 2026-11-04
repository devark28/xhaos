// Package openai adapts OpenAI-compatible Chat Completions APIs (OpenAI,
// DeepSeek, and local servers such as Ollama, llama.cpp, or vLLM) to the
// provider-neutral llm interface. It speaks plain HTTP with server-sent
// events, so any server that implements /chat/completions and /models works.
package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"xhaos/app/llm"
)

const Kind = "openai"

// DefaultBaseURL is used when a provider config names no endpoint.
const DefaultBaseURL = "https://api.openai.com/v1"

type Adapter struct {
	// Client sends the requests; nil uses http.DefaultClient.
	Client *http.Client
}

func (Adapter) Kind() string { return Kind }

// KeyOptional reports that this kind may run without an API key: local
// servers usually take none.
func (Adapter) KeyOptional() bool { return true }

func (a Adapter) do(ctx context.Context, creds llm.Credentials, method, path string, body any) (*http.Response, error) {
	base := strings.TrimRight(creds.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if creds.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+creds.APIKey)
	}
	client := a.Client
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode/100 != 2 {
		defer res.Body.Close()
		data, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return nil, apiError(res.Status, data)
	}
	return res, nil
}

// apiError turns an error response into a short message.
func apiError(status string, body []byte) error {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
		return fmt.Errorf("%s: %s", status, e.Error.Message)
	}
	if text := strings.TrimSpace(string(body)); text != "" {
		return fmt.Errorf("%s: %s", status, text)
	}
	return errors.New(status)
}

// ---- request ------------------------------------------------------------------

type message struct {
	Role       string     `json:"role"`
	Content    any        `json:"content"` // *string, or []part for a user message with images
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type toolCall struct {
	Index    *int   `json:"index,omitempty"` // in stream deltas only
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type tool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description,omitempty"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

func text(s string) *string { return &s }

// messages converts a turn to Chat Completions messages. Tool results become
// "tool" messages, which must directly follow the assistant message whose
// tool_calls they answer; an owner's text in the same turn comes after them.
func messages(turn llm.Turn) []message {
	out := []message{}
	if turn.System != "" {
		out = append(out, message{Role: "system", Content: text(turn.System)})
	}
	for _, m := range turn.Messages {
		switch m.Role {
		case "user":
			for _, r := range m.Results {
				content := r.Content
				if r.IsError {
					content = "Error: " + content
				}
				out = append(out, message{Role: "tool", ToolCallID: r.CallID, Content: text(content)})
			}
			if len(m.Files) > 0 {
				out = append(out, message{Role: "user", Content: userParts(m)})
			} else if m.Text != "" {
				out = append(out, message{Role: "user", Content: text(m.Text)})
			}
		case "assistant":
			msg := message{Role: "assistant"}
			if m.Text != "" {
				msg.Content = text(m.Text)
			}
			for _, c := range m.Calls {
				tc := toolCall{ID: c.ID, Type: "function"}
				tc.Function.Name, tc.Function.Arguments = c.Name, string(c.Input)
				msg.ToolCalls = append(msg.ToolCalls, tc)
			}
			if m.Text == "" && len(msg.ToolCalls) == 0 {
				continue // an empty reply (e.g. only reasoning) adds nothing
			}
			out = append(out, msg)
		}
	}
	return out
}

// userParts sends text and images as content parts. Chat Completions has no
// portable way to send a PDF, so a PDF becomes a note.
func userParts(m llm.Message) []map[string]any {
	parts := []map[string]any{}
	note := ""
	for _, f := range m.Files {
		if f.Type == "application/pdf" {
			note += fmt.Sprintf("\n\n(Attached PDF %q could not be sent: this provider takes images and text only.)", f.Name)
			continue
		}
		url := "data:" + f.Type + ";base64," + base64.StdEncoding.EncodeToString(f.Data)
		parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}})
	}
	if t := m.Text + note; t != "" {
		parts = append([]map[string]any{{"type": "text", "text": t}}, parts...)
	}
	return parts
}

func tools(specs []llm.Tool) []tool {
	out := []tool{}
	for _, s := range specs {
		t := tool{Type: "function"}
		t.Function.Name, t.Function.Description = s.Name, s.Description
		props := s.Schema.Properties
		if props == nil {
			props = map[string]any{}
		}
		params := map[string]any{"type": "object", "properties": props}
		if len(s.Schema.Required) > 0 {
			params["required"] = s.Schema.Required
		}
		t.Function.Parameters = params
		out = append(out, t)
	}
	return out
}

// ---- streaming ------------------------------------------------------------------

type chunk struct {
	Choices []struct {
		Delta struct {
			Content   string     `json:"content"`
			ToolCalls []toolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// stopReasons maps finish reasons to the harness's (Anthropic-style) names.
var stopReasons = map[string]string{"stop": "end_turn", "tool_calls": "tool_use", "function_call": "tool_use", "length": "max_tokens", "content_filter": "refusal"}

func (a Adapter) Stream(ctx context.Context, creds llm.Credentials, turn llm.Turn, onDelta func(string)) (llm.Result, error) {
	body := map[string]any{
		"model":          turn.Model,
		"messages":       messages(turn),
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
	}
	if len(turn.Tools) > 0 {
		body["tools"] = tools(turn.Tools)
	}
	res, err := a.do(ctx, creds, http.MethodPost, "/chat/completions", body)
	if err != nil {
		return llm.Result{}, err
	}
	defer res.Body.Close()

	var out llm.Result
	var reply strings.Builder
	type pending struct {
		id, name string
		args     strings.Builder
	}
	calls := map[int]*pending{}
	finish := ""
	scanner := bufio.NewScanner(res.Body)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var c chunk
		if err := json.Unmarshal([]byte(data), &c); err != nil {
			return llm.Result{}, fmt.Errorf("bad stream chunk: %w", err)
		}
		if c.Error != nil {
			return llm.Result{}, errors.New(c.Error.Message)
		}
		if c.Usage != nil {
			out.InputTokens, out.OutputTokens = c.Usage.PromptTokens, c.Usage.CompletionTokens
		}
		for _, choice := range c.Choices {
			if choice.Delta.Content != "" {
				reply.WriteString(choice.Delta.Content)
				onDelta(choice.Delta.Content)
			}
			for _, tc := range choice.Delta.ToolCalls {
				i := len(calls)
				if tc.Index != nil {
					i = *tc.Index
				}
				p := calls[i]
				if p == nil {
					p = &pending{}
					calls[i] = p
				}
				if tc.ID != "" {
					p.id = tc.ID
				}
				if tc.Function.Name != "" {
					p.name = tc.Function.Name
				}
				p.args.WriteString(tc.Function.Arguments)
			}
			if choice.FinishReason != "" {
				finish = choice.FinishReason
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return llm.Result{}, err
	}
	if ctx.Err() != nil {
		return llm.Result{}, ctx.Err()
	}

	out.Text = reply.String()
	indexes := make([]int, 0, len(calls))
	for i := range calls {
		indexes = append(indexes, i)
	}
	sort.Ints(indexes)
	for n, i := range indexes {
		p := calls[i]
		if p.id == "" {
			p.id = fmt.Sprintf("call_%d", n)
		}
		out.Calls = append(out.Calls, llm.ToolCall{ID: p.id, Name: p.name, Input: arguments(p.args.String())})
	}
	out.StopReason = stopReasons[finish]
	if out.StopReason == "" {
		out.StopReason = finish
	}
	if len(out.Calls) > 0 && out.StopReason != "max_tokens" {
		out.StopReason = "tool_use" // some servers report "stop" with tool calls
	}
	return out, nil
}

// arguments returns a call's JSON arguments. Arguments that are not a JSON
// object (cut off, or malformed by a small model) are wrapped so they still
// record cleanly; the tool then rejects them and the model sees why.
func arguments(raw string) json.RawMessage {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return json.RawMessage(`{}`)
	}
	var obj map[string]any
	if json.Unmarshal([]byte(raw), &obj) == nil {
		return json.RawMessage(raw)
	}
	wrapped, _ := json.Marshal(map[string]string{"invalid_arguments": raw})
	return wrapped
}

// Models lists the model IDs the endpoint offers; it doubles as a key check.
func (a Adapter) Models(ctx context.Context, creds llm.Credentials) ([]string, error) {
	res, err := a.do(ctx, creds, http.MethodGet, "/models", nil)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(&list); err != nil {
		return nil, fmt.Errorf("bad model list: %w", err)
	}
	ids := make([]string, 0, len(list.Data))
	for _, m := range list.Data {
		ids = append(ids, m.ID)
	}
	sort.Strings(ids)
	return ids, nil
}
