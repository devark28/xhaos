// Package serve makes the page load fast over slow links: it compresses
// responses (gzip) and lets browsers keep static files (ETag and 304 Not
// Modified). Streams pass through untouched: Server-Sent Events are written
// and flushed as they come, and WebSocket upgrades are never wrapped.
package serve

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// minSize is the smallest body worth compressing.
const minSize = 1024

var gzips = sync.Pool{New: func() any { w, _ := gzip.NewWriterLevel(nil, gzip.DefaultCompression); return w }}

// Compress gzips responses whose type compresses well (text, JSON,
// JavaScript, CSS, SVG, WebAssembly) for clients that accept it.
func Compress(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !acceptsGzip(r) || r.Header.Get("Upgrade") != "" || r.Header.Get("Range") != "" || r.Method == http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		cw := &compressWriter{ResponseWriter: w}
		defer cw.finish()
		next.ServeHTTP(cw, r)
	})
}

// acceptsGzip reads Accept-Encoding: gzip (or *) with a quality above zero.
func acceptsGzip(r *http.Request) bool {
	ok := false
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		fields := strings.Split(part, ";")
		name := strings.ToLower(strings.TrimSpace(fields[0]))
		if name != "gzip" && name != "*" {
			continue
		}
		q := 1.0
		for _, p := range fields[1:] {
			if k, v, found := strings.Cut(strings.TrimSpace(p), "="); found && strings.TrimSpace(k) == "q" {
				if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
					q = f
				}
			}
		}
		if name == "gzip" {
			return q > 0 // an explicit gzip entry decides
		}
		ok = q > 0
	}
	return ok
}

// Compressible reports whether a body of this media type is worth gzipping.
func Compressible(contentType string) bool {
	t, _, _ := strings.Cut(strings.ToLower(contentType), ";")
	t = strings.TrimSpace(t)
	if t == "text/event-stream" {
		return false
	}
	return strings.HasPrefix(t, "text/") || strings.HasSuffix(t, "+json") || strings.HasSuffix(t, "+xml") ||
		t == "application/json" || t == "application/javascript" || t == "application/x-javascript" ||
		t == "application/xml" || t == "image/svg+xml" || t == "application/wasm" || t == "font/ttf" || t == "font/otf"
}

type compressWriter struct {
	http.ResponseWriter
	decided bool
	gz      *gzip.Writer
	code    int
}

func (c *compressWriter) decide(first []byte) {
	if c.decided {
		return
	}
	c.decided = true
	h := c.Header()
	if c.code == 0 {
		c.code = http.StatusOK
	}
	if h.Get("Content-Type") == "" && len(first) > 0 {
		h.Set("Content-Type", http.DetectContentType(first))
	}
	small := false
	if n, err := strconv.Atoi(h.Get("Content-Length")); err == nil && n < minSize {
		small = true
	}
	if c.code >= 200 && c.code < 300 && c.code != http.StatusNoContent && c.code != http.StatusPartialContent &&
		h.Get("Content-Encoding") == "" && !small && Compressible(h.Get("Content-Type")) {
		h.Del("Content-Length")
		h.Set("Content-Encoding", "gzip")
		if etag := h.Get("ETag"); etag != "" && !strings.HasPrefix(etag, "W/") {
			h.Set("ETag", "W/"+etag) // the compressed bytes differ from the original
		}
		c.gz = gzips.Get().(*gzip.Writer)
		c.gz.Reset(c.ResponseWriter)
	}
	h.Add("Vary", "Accept-Encoding")
	c.ResponseWriter.WriteHeader(c.code)
}

func (c *compressWriter) WriteHeader(code int) {
	if c.decided {
		return
	}
	if code >= 100 && code < 200 { // informational responses go straight through
		c.ResponseWriter.WriteHeader(code)
		return
	}
	c.code = code
	// A response without a body (304, 204, redirects…) is decided now.
	if code == http.StatusNotModified || code == http.StatusNoContent || code < 200 || code >= 300 {
		c.decide(nil)
	}
}

func (c *compressWriter) Write(b []byte) (int, error) {
	c.decide(b)
	if c.gz != nil {
		return c.gz.Write(b)
	}
	return c.ResponseWriter.Write(b)
}

// Flush sends what is buffered: Server-Sent Events are never compressed, but
// a compressed response that flushes still reaches the client promptly.
func (c *compressWriter) Flush() {
	if !c.decided {
		c.decide(nil)
	}
	if c.gz != nil {
		_ = c.gz.Flush()
	}
	_ = http.NewResponseController(c.ResponseWriter).Flush()
}

func (c *compressWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if c.decided {
		return nil, nil, errors.New("serve: response already started")
	}
	return http.NewResponseController(c.ResponseWriter).Hijack()
}

func (c *compressWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

func (c *compressWriter) finish() {
	if !c.decided {
		// The handler wrote nothing (or only a status): send that status.
		if c.code == 0 {
			return
		}
		c.decide(nil)
	}
	if c.gz != nil {
		_ = c.gz.Close()
		c.gz.Reset(nil)
		gzips.Put(c.gz)
		c.gz = nil
	}
}

// maxTagged is the largest body ETag buffers to hash.
const maxTagged = 16 << 20

// ETag gives successful GET responses of static files an entity tag, the
// hash of their body, and answers a matching If-None-Match with 304 Not
// Modified, so a reload revalidates each file in one round trip instead of
// downloading it again. skip names paths that are not static (live streams,
// API calls, per-user data).
func ETag(skip func(path string) bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (r.Method != http.MethodGet && r.Method != http.MethodHead) || r.Header.Get("Upgrade") != "" || r.Header.Get("Range") != "" || skip(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		b := &bufferWriter{header: http.Header{}}
		next.ServeHTTP(b, r)
		h := w.Header()
		for k, v := range b.header {
			h[k] = v
		}
		if b.code == 0 {
			b.code = http.StatusOK
		}
		if b.code != http.StatusOK || b.overflow != nil {
			w.WriteHeader(b.code)
			w.Write(b.body.Bytes())
			if b.overflow != nil {
				w.Write(b.overflow)
			}
			return
		}
		sum := sha256.Sum256(b.body.Bytes())
		tag := `"` + base64.RawURLEncoding.EncodeToString(sum[:12]) + `"`
		h.Set("ETag", tag)
		if match(r.Header.Get("If-None-Match"), tag) {
			for _, k := range []string{"Content-Length", "Content-Type", "Content-Encoding"} {
				h.Del(k)
			}
			w.WriteHeader(http.StatusNotModified)
			return
		}
		h.Set("Content-Length", strconv.Itoa(b.body.Len()))
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			w.Write(b.body.Bytes())
		}
	})
}

func match(ifNoneMatch, tag string) bool {
	for _, t := range strings.Split(ifNoneMatch, ",") {
		t = strings.TrimPrefix(strings.TrimSpace(t), "W/")
		if t == "*" || t == tag {
			return true
		}
	}
	return false
}

// bufferWriter holds a response to hash it; a body past maxTagged is passed
// on untagged.
type bufferWriter struct {
	header   http.Header
	code     int
	body     bytes.Buffer
	overflow []byte
}

func (b *bufferWriter) Header() http.Header { return b.header }
func (b *bufferWriter) WriteHeader(code int) {
	if b.code == 0 {
		b.code = code
	}
}
func (b *bufferWriter) Write(p []byte) (int, error) {
	if b.overflow != nil || b.body.Len()+len(p) > maxTagged {
		b.overflow = append(b.overflow, p...)
		return len(p), nil
	}
	return b.body.Write(p)
}
