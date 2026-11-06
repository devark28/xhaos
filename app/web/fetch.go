// Package web gives agents the web: web_fetch reads a page as text, and
// web_search searches through the backend chosen in Settings (DuckDuckGo
// with no key, Brave Search with one, or your own SearXNG).
//
// The server makes the requests, not the agent's sandbox. Every connection
// is checked against the address it actually reaches (after DNS, on every
// redirect), so neither a link nor a DNS answer can turn a fetch toward
// localhost, the LAN, or a cloud metadata endpoint. What comes back is
// labelled as untrusted for the model: a page can contain instructions,
// and they are not the user's.
package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

const (
	maxBody   = 5 << 20 // bytes read from a response
	userAgent = "Mozilla/5.0 (compatible; Xhaos agent; +https://github.com/)"
)

// Public reports whether an address is on the open internet.
func Public(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsGlobalUnicast() && !a.IsPrivate() && !a.IsLoopback() && !a.IsLinkLocalUnicast() &&
		!netip.MustParsePrefix("100.64.0.0/10").Contains(a) && !netip.MustParsePrefix("fc00::/7").Contains(a)
}

// Client is an HTTP client that only connects to public addresses (unless
// allowPrivate, for tests), checked on the socket's actual address.
func Client(allowPrivate bool) *http.Client {
	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip, err := netip.ParseAddr(host)
			if err != nil {
				return err
			}
			if !allowPrivate && !Public(ip) {
				return fmt.Errorf("%s is not a public address; agents cannot reach local or private networks through the web tools", ip)
			}
			return nil
		},
	}
	transport := &http.Transport{
		Proxy:                 nil, // never through a configured proxy: it could reach anything
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		MaxIdleConns:          10,
		IdleConnTimeout:       30 * time.Second,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("redirect to %s refused", req.URL.Scheme)
			}
			return nil
		},
	}
}

// Page is a fetched page as text.
type Page struct {
	URL   string // after redirects
	Title string
	Type  string
	Text  string
}

// Fetch reads rawURL and returns it as text: HTML becomes readable text
// (headings, lists, links, code), other text formats come as they are.
func Fetch(ctx context.Context, c *http.Client, rawURL string) (Page, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return Page{}, fmt.Errorf("%q is not an http(s) URL", rawURL)
	}
	if u.User != nil {
		return Page{}, errors.New("URLs with a user name or password are not fetched")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Page{}, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain,application/json,text/markdown;q=0.9,*/*;q=0.5")
	res, err := c.Do(req)
	if err != nil {
		return Page{}, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, maxBody+1))
	if err != nil {
		return Page{}, err
	}
	cut := len(body) > maxBody
	if cut {
		body = body[:maxBody]
	}
	mediaType, _, _ := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if mediaType == "" {
		mediaType = http.DetectContentType(body)
		mediaType, _, _ = mime.ParseMediaType(mediaType)
	}
	p := Page{URL: res.Request.URL.String(), Type: mediaType}
	if res.StatusCode >= 400 {
		return p, fmt.Errorf("%s answered %s", p.URL, res.Status)
	}
	switch {
	case mediaType == "text/html" || mediaType == "application/xhtml+xml":
		p.Title, p.Text = HTMLText(string(body), res.Request.URL)
	case strings.HasPrefix(mediaType, "text/") || mediaType == "application/json" || strings.HasSuffix(mediaType, "+json") || strings.HasSuffix(mediaType, "+xml") || mediaType == "application/xml":
		p.Text = string(body)
	default:
		return p, fmt.Errorf("%s is %s, not text (%d bytes)", p.URL, mediaType, len(body))
	}
	if !utf8.ValidString(p.Text) {
		p.Text = strings.ToValidUTF8(p.Text, "�")
	}
	if cut {
		p.Text += "\n…[cut: the page is larger than 5 MB]"
	}
	return p, nil
}
