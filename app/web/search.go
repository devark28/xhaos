package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Search backends.
const (
	Off        = "off"
	DuckDuckGo = "duckduckgo" // no key: reads DuckDuckGo's HTML results page (best effort)
	Brave      = "brave"      // Brave Search API, with a key
	SearXNG    = "searxng"    // your own SearXNG (JSON output enabled)
)

// Endpoints, replaceable in tests.
var (
	DuckDuckGoURL = "https://html.duckduckgo.com/html/"
	BraveURL      = "https://api.search.brave.com/res/v1/web/search"
)

type Result struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

var tags = regexp.MustCompile(`<[^>]+>`)

func clean(s string) string {
	return strings.TrimSpace(spaces.ReplaceAllString(html.UnescapeString(tags.ReplaceAllString(s, "")), " "))
}

func get(ctx context.Context, c *http.Client, u string, header map[string]string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	res, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, maxBody))
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 400 {
		return nil, fmt.Errorf("search answered %s: %s", res.Status, clean(string(body[:min(len(body), 200)])))
	}
	return body, nil
}

// Search runs query on a backend; key and base are the backend's settings.
func Search(ctx context.Context, c *http.Client, backend, key, base, query string, count int) ([]Result, error) {
	count = max(1, min(count, 20))
	switch backend {
	case DuckDuckGo:
		body, err := get(ctx, c, DuckDuckGoURL+"?q="+url.QueryEscape(query), map[string]string{"Accept": "text/html"})
		if err != nil {
			return nil, err
		}
		return duckDuckGo(string(body), count), nil
	case Brave:
		if key == "" {
			return nil, fmt.Errorf("Brave Search needs an API key (Settings → Tools → Web)")
		}
		body, err := get(ctx, c, fmt.Sprintf("%s?q=%s&count=%d", BraveURL, url.QueryEscape(query), count),
			map[string]string{"Accept": "application/json", "X-Subscription-Token": key})
		if err != nil {
			return nil, err
		}
		var r struct {
			Web struct {
				Results []struct{ Title, URL, Description string }
			}
		}
		if err := json.Unmarshal(body, &r); err != nil {
			return nil, fmt.Errorf("unexpected answer from Brave Search: %v", err)
		}
		var out []Result
		for _, x := range r.Web.Results {
			out = append(out, Result{Title: clean(x.Title), URL: x.URL, Snippet: clean(x.Description)})
		}
		return first(out, count), nil
	case SearXNG:
		if base == "" {
			return nil, fmt.Errorf("SearXNG needs its address (Settings → Tools → Web)")
		}
		body, err := get(ctx, c, strings.TrimRight(base, "/")+"/search?format=json&q="+url.QueryEscape(query), map[string]string{"Accept": "application/json"})
		if err != nil {
			return nil, err
		}
		var r struct {
			Results []struct{ Title, URL, Content string }
		}
		if err := json.Unmarshal(body, &r); err != nil {
			return nil, fmt.Errorf("unexpected answer from SearXNG (is its JSON format enabled?): %v", err)
		}
		var out []Result
		for _, x := range r.Results {
			out = append(out, Result{Title: clean(x.Title), URL: x.URL, Snippet: clean(x.Content)})
		}
		return first(out, count), nil
	}
	return nil, fmt.Errorf("web search is off; the user can choose a search backend in Settings → Tools → Web")
}

func first(r []Result, n int) []Result {
	if len(r) > n {
		return r[:n]
	}
	return r
}

func hasClass(n *html.Node, class string) bool {
	return strings.Contains(" "+attr(n, "class")+" ", " "+class+" ")
}

func textOf(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return clean(b.String())
}

// duckDuckGo reads results from DuckDuckGo's HTML page (ads left out).
func duckDuckGo(page string, count int) []Result {
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		return nil
	}
	var out []Result
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if len(out) >= count {
			return
		}
		if n.Type == html.ElementNode && n.DataAtom == atom.Div && hasClass(n, "result") && !hasClass(n, "result--ad") {
			var r Result
			var find func(*html.Node)
			find = func(m *html.Node) {
				if m.Type == html.ElementNode && m.DataAtom == atom.A {
					switch {
					case hasClass(m, "result__a"):
						r.Title, r.URL = textOf(m), attr(m, "href")
					case hasClass(m, "result__snippet"):
						r.Snippet = textOf(m)
					}
				}
				for c := m.FirstChild; c != nil; c = c.NextSibling {
					find(c)
				}
			}
			find(n)
			// Links go through DuckDuckGo's redirect: //duckduckgo.com/l/?uddg=<target>
			if u, err := url.Parse(r.URL); err == nil && u.Query().Get("uddg") != "" {
				r.URL = u.Query().Get("uddg")
			}
			if r.Title != "" && strings.HasPrefix(r.URL, "http") {
				out = append(out, r)
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return out
}
