package classifier

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// Client asks a local llama-server (its OpenAI-compatible API) for a verdict
// on one command. It knows nothing of the deny floor or the policy: see
// Floor and Classifier.
type Client struct {
	url     string // base URL, without a trailing slash
	timeout time.Duration
	http    *http.Client
}

const (
	maxReason = 200     // characters of the model's reason kept
	maxBody   = 1 << 16 // bytes of a reply read
)

// NewClient returns a client for the llama-server at url (for example
// http://127.0.0.1:8081). Each request is cut off after timeout (0: 15 s).
func NewClient(url string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &Client{url: strings.TrimRight(url, "/"), timeout: timeout, http: &http.Client{}}
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Messages       []chatMessage   `json:"messages"`
	Temperature    float64         `json:"temperature"`
	TopK           int             `json:"top_k"`
	Seed           int             `json:"seed"`
	CachePrompt    bool            `json:"cache_prompt"`
	MaxTokens      int             `json:"max_tokens"`
	Stream         bool            `json:"stream"`
	ResponseFormat json.RawMessage `json:"response_format"`
}

// The same command should get the same answer every time: greedy decoding and
// a fixed seed. cache_prompt lets the server reuse the prompt's processing
// between requests (it is the same for every command). Measured on the
// 98-command held-out set, cache on: no verdict changed between two passes
// for the 1.5B model, and none for the 3B and 7B models except where pass 1
// hit a 30 s timeout (a timeout is an ask). Answers with the cache off were
// not measured on that set.
const (
	decodeSeed = 1
	decodeTopK = 1
)

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

type answer struct {
	Verdict string `json:"verdict"`
	Reason  string `json:"reason"`
}

// responseFormat is the json_schema response format around schema.
var responseFormat = json.RawMessage(`{"type":"json_schema","json_schema":{"name":"command_verdict","strict":true,"schema":` + schema + `}}`)

// Ask returns the model's verdict on text (the command as the model may read
// it, see modelText): allow true only when the model answered "allow". Any
// failure (timeout, transport error, a status other than 200, malformed JSON,
// an unknown verdict) returns allow false with a reason for the owner and a
// non-nil error.
func (c *Client) Ask(ctx context.Context, text string) (allow bool, reason string, err error) {
	quoted, err := json.Marshal(text)
	if err != nil {
		return false, "the command could not be encoded for the classifier", err
	}
	messages := append([]chatMessage{{Role: "system", Content: systemPrompt}}, exampleMessages()...)
	messages = append(messages, chatMessage{Role: "user", Content: userPrefix + string(quoted)})
	body, err := json.Marshal(chatRequest{
		Messages:       messages,
		Temperature:    0,
		TopK:           decodeTopK,
		Seed:           decodeSeed,
		CachePrompt:    true,
		MaxTokens:      maxTokens,
		ResponseFormat: responseFormat,
	})
	if err != nil {
		return false, "the classifier request could not be built", err
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return false, "the classifier URL is not valid", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return false, fmt.Sprintf("the classifier did not answer within %s", c.timeout), err
		}
		return false, "the classifier could not be reached", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return false, fmt.Sprintf("the classifier did not answer within %s", c.timeout), err
		}
		return false, "the classifier's answer could not be read", err
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Sprintf("the classifier answered with status %d", resp.StatusCode), fmt.Errorf("classifier: status %d", resp.StatusCode)
	}
	var chat chatResponse
	if err := json.Unmarshal(raw, &chat); err != nil || len(chat.Choices) == 0 {
		if err == nil {
			err = errors.New("classifier: no choices in the answer")
		}
		return false, "the classifier's answer was not valid", err
	}
	var a answer
	dec := json.NewDecoder(strings.NewReader(chat.Choices[0].Message.Content))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return false, "the classifier's answer was not valid", fmt.Errorf("classifier: %w", err)
	}
	reason = cleanReason(a.Reason)
	switch a.Verdict {
	case "allow":
		if reason == "" {
			reason = "an ordinary project command"
		}
		return true, reason, nil
	case "ask":
		if reason == "" {
			reason = "the classifier was not sure it is safe"
		}
		return false, reason, nil
	}
	return false, "the classifier gave an unknown verdict", fmt.Errorf("classifier: unknown verdict %q", a.Verdict)
}

// cleanReason puts the model's reason on one line, without control
// characters, and cuts it to maxReason characters.
func cleanReason(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > maxReason {
		s = string([]rune(s)[:maxReason]) + "…"
	}
	return s
}

// Health asks llama-server whether its model is loaded: GET {url}/health
// answers 200 when it is ready (503 while the model loads).
func (c *Client) Health(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("the classifier could not be reached: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBody))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the classifier is not ready (status %d)", resp.StatusCode)
	}
	return nil
}
