// Package llm is the provider-neutral model interface. Adapters (anthropic,
// later openai-compatible) translate a Turn to their API; the harness owns the
// loop and never depends on a provider's message format.
package llm

import (
	"context"
	"encoding/json"
)

// Credentials for one provider config, resolved on the server.
type Credentials struct {
	APIKey  string
	BaseURL string // empty: the provider's default endpoint
}

// Message is one conversation entry. Raw, when set, is the adapter's native
// assistant message (for example with thinking blocks); the adapter that
// produced it replays it verbatim so history stays append-only.
type Message struct {
	Role    string // "user" | "assistant"
	Text    string
	RawKind string
	Raw     json.RawMessage
	// Calls are the tool calls an assistant message made (used when Raw is
	// not replayable by this adapter). Results answer them in the next user
	// message, before its Text.
	Calls   []ToolCall
	Results []ToolResult
	// StripThinking replays Raw without its reasoning blocks. Providers bind
	// those blocks to the system prompt and tools they were made under; the
	// harness sets this, deterministically, when those have changed since.
	StripThinking bool
	// Files are images and PDFs sent with a user message, after its Text.
	Files []File
}

// File is an image or PDF the model reads natively.
type File struct {
	Name string
	Type string // image/png, image/jpeg, image/gif, image/webp, application/pdf
	Data []byte
}

// Tool is a function the model may call. Schema is a JSON Schema object.
type Tool struct {
	Name        string
	Description string
	Schema      ToolSchema
}

type ToolSchema struct {
	Properties map[string]any
	Required   []string
}

type ToolCall struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type ToolResult struct {
	CallID  string `json:"callId"`
	Content string `json:"content"`
	IsError bool   `json:"isError,omitempty"`
}

type Turn struct {
	Model    string
	System   string // must stay identical across a conversation's turns
	Tools    []Tool // must stay identical too, so the prompt prefix is stable
	Messages []Message
	// Small marks a short side call (a title): low effort and a small output
	// limit where the provider supports them, and no prompt caching.
	Small bool
	// Purpose names a side call ("title"), for adapters that script replies.
	Purpose string
}

type Result struct {
	Text       string
	Calls      []ToolCall
	StopReason string // provider value, e.g. "end_turn", "tool_use", "max_tokens", "refusal"
	Raw        json.RawMessage
	// InputTokens is the whole prompt, cached parts included; the cache
	// counts say how much of it was read from or written to the cache.
	InputTokens      int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	OutputTokens     int64
}

type Adapter interface {
	// Kind is the provider kind this adapter serves, e.g. "anthropic".
	Kind() string
	// Stream runs one model turn, calling onDelta with text as it arrives.
	Stream(ctx context.Context, creds Credentials, turn Turn, onDelta func(string)) (Result, error)
	// Models lists model IDs the credentials can use; it doubles as a key check.
	Models(ctx context.Context, creds Credentials) ([]string, error)
}
