package dictation

// The warm service: a dictation server that keeps its model loaded between
// takes (scripts/dictation/stt.py --serve, on the loopback). Xhaos sends each
// take to it over HTTP, so no take pays the model load. Set with -dictation-url.
// When it is down or fails, the take goes to the command instead, if
// -dictation-cmd is also set (see Service.transcribe).

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const healthTimeout = 5 * time.Second // the page's "Check again" waits this long at most

// Health is the warm service's state, for the settings' indicator.
type Health struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"` // why it is not OK
}

// ServiceEnabled reports whether a warm service is set (-dictation-url).
func (s *Service) ServiceEnabled() bool { return s.cfg.URL != "" }

// Health checks the warm service (GET {url}/health). The model is loaded
// before the service listens, so an answer of 200 means it is ready.
func (s *Service) Health() Health {
	if s.cfg.URL == "" {
		return Health{Detail: "no dictation service is configured: start the server with -dictation-url"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), healthTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.cfg.URL+"/health", nil)
	if err != nil {
		return Health{Detail: "the dictation service URL is not valid"}
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return Health{Detail: "the dictation service could not be reached"}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxOutput))
	if resp.StatusCode != http.StatusOK {
		return Health{Detail: fmt.Sprintf("the dictation service is not ready (status %d)", resp.StatusCode)}
	}
	return Health{OK: true}
}

// serve sends one take to the warm service and returns its transcript. An
// error is a failure to answer (down, or an error status); when ctx ends first,
// the error is ctx's, and the caller reports the timeout.
func (s *Service) serve(ctx context.Context, pcm []byte, language, prompt string) (string, error) {
	query := url.Values{"language": {language}, "prompt": {prompt}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.URL+"/transcribe?"+query.Encode(), bytes.NewReader(wavBytes(pcm)))
	if err != nil {
		return "", fmt.Errorf("the dictation service URL is not valid: %w", err)
	}
	req.Header.Set("Content-Type", "audio/wav")
	resp, err := s.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("the dictation service could not be reached: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxOutput)))
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("the dictation service's answer could not be read: %w", err)
	}
	text := strings.TrimSpace(string(raw))
	if resp.StatusCode != http.StatusOK {
		if utf8.RuneCountInString(text) > maxErrText {
			text = string([]rune(text)[:maxErrText]) + "…"
		}
		return "", fmt.Errorf("the dictation service failed (status %d): %s", resp.StatusCode, text)
	}
	return text, nil
}
