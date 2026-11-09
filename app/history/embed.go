package history

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

// Embedder turns texts into unit-length vectors.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// Counter counts the tokens the model sees in a text.
type Counter interface {
	Count(ctx context.Context, text string) (int, error)
}

// maxPassageTokens is the most tokens a passage may have. bge-small takes 512;
// the server gives each parallel slot 512 (-c 2048 -np 4), and the margin
// covers the [CLS] and [SEP] tokens. Longer passages are split on import.
const maxPassageTokens = 500

// queryPrefix is the instruction bge-small-en-v1.5 expects in front of a
// search query (passages get none).
const queryPrefix = "Represent this sentence for searching relevant passages: "

// Client asks a local llama-server started with --embedding for vectors
// (its OpenAI-compatible /v1/embeddings).
type Client struct {
	url  string
	http *http.Client
	// batch is how many texts go in one request.
	batch int
}

// NewClient returns a client for the llama-server at url (for example
// http://127.0.0.1:8082). Each request is cut off after 60 s.
func NewClient(url string) *Client {
	return &Client{url: strings.TrimRight(url, "/"), http: &http.Client{Timeout: 60 * time.Second}, batch: 16}
}

type embedRequest struct {
	Input []string `json:"input"`
}

type embedResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
}

// Embed returns one unit-length vector per text, in order.
func (c *Client) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += c.batch {
		end := min(start+c.batch, len(texts))
		vecs, err := c.request(ctx, texts[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, vecs...)
	}
	return out, nil
}

func (c *Client) request(ctx context.Context, texts []string) ([][]float32, error) {
	body, err := json.Marshal(embedRequest{Input: texts})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("embedding URL: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embedding server unreachable: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("embedding answer: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embedding server answered with status %d", resp.StatusCode)
	}
	var r embedResponse
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("embedding answer: %w", err)
	}
	if len(r.Data) != len(texts) {
		return nil, fmt.Errorf("embedding answer has %d vectors for %d texts", len(r.Data), len(texts))
	}
	out := make([][]float32, len(texts))
	for _, d := range r.Data {
		if d.Index < 0 || d.Index >= len(texts) || len(d.Embedding) == 0 {
			return nil, errors.New("embedding answer has a bad vector")
		}
		out[d.Index] = normalize(d.Embedding)
	}
	return out, nil
}

// Count returns how many tokens the server's tokenizer makes of text
// (POST /tokenize).
func (c *Client) Count(ctx context.Context, text string) (int, error) {
	body, err := json.Marshal(map[string]string{"content": text})
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url+"/tokenize", bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("embedding URL: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("embedding server unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("embedding server tokenizer answered with status %d", resp.StatusCode)
	}
	var r struct {
		Tokens []int `json:"tokens"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&r); err != nil {
		return 0, fmt.Errorf("tokenizer answer: %w", err)
	}
	return len(r.Tokens), nil
}

// Health reports whether the server answers (GET /health).
func (c *Client) Health(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("embedding server unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("embedding server status %d", resp.StatusCode)
	}
	return nil
}

// normalize scales v to unit length, so a dot product is a cosine.
func normalize(v []float32) []float32 {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	if s == 0 {
		return v
	}
	inv := float32(1 / math.Sqrt(s))
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = x * inv
	}
	return out
}
