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
	"unicode"
	"unicode/utf8"
)

// Client asks a local llama-server for a verdict on one command. It knows
// nothing of the deny floor or the policy: see Floor and Classifier.
//
// The model answers with one token after a prefilled `{"verdict":"`: "allow"
// allows, anything else asks. The reason comes from a second request (Explain),
// which Xhaos makes in the background after an allow. Both send a raw prompt
// (llama-server's /completion) built as ChatML, with no chat template and no
// grammar.
type Client struct {
	url  string // base URL, without a trailing slash
	http *http.Client
	// timeout is the limit for one verdict or reason; Warm has its own.
	timeout time.Duration
}

const (
	maxReason = 200     // characters of the model's reason kept
	maxBody   = 1 << 16 // bytes of a reply read

	// verdictTokens bounds the verdict: one token is enough for allow or ask.
	verdictTokens = 2
	// reasonTokens bounds the reason: one short sentence, cut at the closing quote.
	reasonTokens = 48
	// warmTimeout is the limit for the warm-up request at service start: the
	// model may still be loading, and the first request is the slowest.
	warmTimeout = 120 * time.Second
	// warmCommand is the command the warm-up judges: its answer is not used.
	warmCommand = "go test ./..."
)

// The verdict and reason prefills. The examples in the prompt (see prompt.go)
// use the JSON form with spaces; the verdict's prefill does not, as measured.
const (
	verdictPrefill = `{"verdict":"`
	allowPrefill   = `{"verdict": "allow", "reason": "`
)

// NewClient returns a client for the llama-server at url (for example
// http://127.0.0.1:8081). Each verdict or reason is cut off after timeout (0: 15 s).
func NewClient(url string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &Client{url: strings.TrimRight(url, "/"), timeout: timeout, http: &http.Client{}}
}

// completionRequest is llama-server's /completion body. The seed and top_k
// fix the decoding: the same prompt gives the same first token.
type completionRequest struct {
	Prompt      string   `json:"prompt"`
	NPredict    int      `json:"n_predict"`
	Temperature float64  `json:"temperature"`
	TopK        int      `json:"top_k"`
	Seed        int      `json:"seed"`
	CachePrompt bool     `json:"cache_prompt"`
	Stream      bool     `json:"stream"`
	Stop        []string `json:"stop,omitempty"`
}

const (
	decodeSeed = 1
	decodeTopK = 1
)

type completionResponse struct {
	Content string `json:"content"`
}

// complete sends prompt and returns the model's text. The failure reason is
// for the owner; the error is for the log.
func (c *Client) complete(ctx context.Context, prompt string, nPredict int, stop []string, limit time.Duration) (text, reason string, err error) {
	body, err := json.Marshal(completionRequest{
		Prompt:      prompt,
		NPredict:    nPredict,
		Temperature: 0,
		TopK:        decodeTopK,
		Seed:        decodeSeed,
		CachePrompt: true,
		Stream:      false,
		Stop:        stop,
	})
	if err != nil {
		return "", "the classifier request could not be built", err
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url+"/completion", bytes.NewReader(body))
	if err != nil {
		return "", "the classifier URL is not valid", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", fmt.Sprintf("the classifier did not answer within %s", limit), err
		}
		return "", "the classifier could not be reached", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", fmt.Sprintf("the classifier did not answer within %s", limit), err
		}
		return "", "the classifier's answer could not be read", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Sprintf("the classifier answered with status %d", resp.StatusCode), fmt.Errorf("classifier: status %d", resp.StatusCode)
	}
	var out completionResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", "the classifier's answer was not valid", fmt.Errorf("classifier: %w", err)
	}
	return out.Content, "", nil
}

// Ask returns the model's verdict on text (the command as the model may read
// it, see modelText): allow true only when the model answered "allow". An
// answer that is neither allow nor ask is an unknown verdict, and asks. Any
// failure (timeout, transport error, a status other than 200) returns allow
// false with a reason for the owner and a non-nil error.
func (c *Client) Ask(ctx context.Context, text string) (allow bool, reason string, err error) {
	content, reason, err := c.complete(ctx, buildPrompt(text, verdictPrefill), verdictTokens, nil, c.timeout)
	if err != nil {
		return false, reason, err
	}
	switch leadingWord(content) {
	case "allow":
		return true, "", nil
	case "ask":
		return false, "", nil
	}
	return false, "the classifier gave an unknown verdict", fmt.Errorf("classifier: unknown verdict %q", content)
}

// Explain asks the model why it allows text. It is only asked after an allow
// (see Ask), in the background. The reason is one line, cut to maxReason
// characters; an empty reason is returned as "" and the caller decides what to show.
func (c *Client) Explain(ctx context.Context, text string) (string, error) {
	content, _, err := c.complete(ctx, buildPrompt(text, allowPrefill), reasonTokens, []string{`"`}, c.timeout)
	if err != nil {
		return "", err
	}
	return cleanReason(content), nil
}

// Warm asks the model one fixed command, so the prompt is processed before the
// first real command arrives. It waits up to warmTimeout for the model to load
// (llama-server answers 503 while it loads) and then makes the request.
func (c *Client) Warm(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, warmTimeout)
	defer cancel()
	for c.Health(ctx) != nil {
		select {
		case <-ctx.Done():
			return fmt.Errorf("classifier: not ready within %s", warmTimeout)
		case <-time.After(time.Second):
		}
	}
	_, reason, err := c.complete(ctx, buildPrompt(warmCommand, verdictPrefill), verdictTokens, nil, warmTimeout)
	if err != nil {
		return fmt.Errorf("classifier: warm-up: %s: %w", reason, err)
	}
	return nil
}

// leadingWord is the run of letters at the start of s, after spaces.
func leadingWord(s string) string {
	s = strings.TrimLeftFunc(s, unicode.IsSpace)
	i := 0
	for i < len(s) && s[i] >= 'a' && s[i] <= 'z' {
		i++
	}
	return s[:i]
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
