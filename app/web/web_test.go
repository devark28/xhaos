package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
)

func TestPublicAddresses(t *testing.T) {
	for addr, want := range map[string]bool{
		"8.8.8.8": true, "2606:4700::1111": true, "93.184.216.34": true,
		"127.0.0.1": false, "10.1.2.3": false, "192.168.1.47": false, "172.16.0.1": false,
		"169.254.169.254": false, "100.64.0.1": false, "::1": false, "fd00::1": false, "fe80::1": false, "0.0.0.0": false,
		"::ffff:127.0.0.1": false,
	} {
		if got := Public(netip.MustParseAddr(addr)); got != want {
			t.Errorf("%s public = %v", addr, got)
		}
	}
}

func TestHTMLText(t *testing.T) {
	page := `<html><head><title>Docs · Thing</title><style>.x{}</style></head><body>
<nav><a href="/">Home</a> <a href="/about">About</a></nav>
<main><h1>Getting <em>started</em></h1><p>Install it with <code>go install x</code>, then
read the <a href="/guide#a">guide</a>.</p><ul><li>One</li><li>Two <b>bold</b></li></ul>
<pre><code>func main() {
	run()
}</code></pre><table><tr><th>Flag</th><th>Use</th></tr><tr><td>-v</td><td>verbose</td></tr></table>
<script>alert("no")</script><img src="a.png" alt="diagram"></main><footer>© 2026</footer></body></html>`
	base, _ := url.Parse("https://docs.example.com/start/")
	title, text := HTMLText(page, base)
	if title != "Docs · Thing" {
		t.Errorf("title = %q", title)
	}
	for _, want := range []string{"# Getting started", "Install it with `go install x`", "[guide](https://docs.example.com/guide#a)", "- One", "- Two **bold**", "```\nfunc main() {\n\trun()\n}\n```", "| Flag | Use |", "| -v | verbose |", "[image: diagram]"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	for _, not := range []string{"alert", "Home", "About", "© 2026", ".x{}"} {
		if strings.Contains(text, not) {
			t.Errorf("kept %q (chrome or script):\n%s", not, text)
		}
	}
}

func TestFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, "<title>T</title><p>Hello <a href='/x'>there</a></p>")
		case "/moved":
			http.Redirect(w, r, "/page", http.StatusFound)
		case "/data.json":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"a":1}`)
		case "/bin":
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Write([]byte{0, 1, 2})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	open := Client(true)
	if p, err := Fetch(ctx, open, srv.URL+"/moved"); err != nil || p.Title != "T" || !strings.Contains(p.Text, "Hello [there]("+srv.URL+"/x)") || !strings.HasSuffix(p.URL, "/page") {
		t.Fatalf("redirected page = %+v %v", p, err)
	}
	if p, err := Fetch(ctx, open, srv.URL+"/data.json"); err != nil || p.Text != `{"a":1}` {
		t.Fatalf("json = %+v %v", p, err)
	}
	if _, err := Fetch(ctx, open, srv.URL+"/bin"); err == nil {
		t.Fatal("fetched a binary file as text")
	}
	if _, err := Fetch(ctx, open, srv.URL+"/missing"); err == nil {
		t.Fatal("404 without an error")
	}
	// The real client refuses this machine and the LAN, also through redirects and names.
	strict := Client(false)
	for _, u := range []string{srv.URL + "/page", "http://localhost:1/", "http://169.254.169.254/latest/meta-data/", "http://[::1]:1/"} {
		if _, err := Fetch(ctx, strict, u); err == nil || !strings.Contains(err.Error(), "not a public address") {
			t.Errorf("%s = %v", u, err)
		}
	}
	for _, u := range []string{"file:///etc/passwd", "ftp://example.com/", "http://user:pw@example.com/", "not a url"} {
		if _, err := Fetch(ctx, strict, u); err == nil {
			t.Errorf("%s fetched", u)
		}
	}
}

func TestSearchBackends(t *testing.T) {
	ddg := `<div class="result results_links"><a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fgo.dev%2Fdoc%2F&rut=x">The Go <b>Docs</b></a>
<a class="result__snippet" href="#">Documentation for the Go language.</a></div>
<div class="result result--ad"><a class="result__a" href="https://ads.example/">Buy now</a></div>
<div class="result"><a class="result__a" href="https://pkg.go.dev/">pkg.go.dev</a></div>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/ddg":
			fmt.Fprint(w, ddg)
		case r.URL.Path == "/brave":
			if r.Header.Get("X-Subscription-Token") != "brave-key" {
				http.Error(w, "no", http.StatusUnauthorized)
				return
			}
			fmt.Fprintf(w, `{"web":{"results":[{"title":"Brave <strong>result</strong>","url":"https://a.example/","description":"About <strong>%s</strong>"}]}}`, r.URL.Query().Get("q"))
		case r.URL.Path == "/searx/search" && r.URL.Query().Get("format") == "json":
			fmt.Fprint(w, `{"results":[{"title":"Searx hit","url":"https://b.example/","content":"from searx"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	DuckDuckGoURL, BraveURL = srv.URL+"/ddg", srv.URL+"/brave"
	defer func() {
		DuckDuckGoURL, BraveURL = "https://html.duckduckgo.com/html/", "https://api.search.brave.com/res/v1/web/search"
	}()
	ctx, c := context.Background(), Client(true)
	r, err := Search(ctx, c, DuckDuckGo, "", "", "go docs", 5)
	if err != nil || len(r) != 2 || r[0].URL != "https://go.dev/doc/" || r[0].Title != "The Go Docs" || r[0].Snippet != "Documentation for the Go language." || r[1].URL != "https://pkg.go.dev/" {
		t.Fatalf("duckduckgo = %+v %v", r, err)
	}
	if r, err := Search(ctx, c, Brave, "brave-key", "", "golang", 5); err != nil || len(r) != 1 || r[0].Title != "Brave result" || r[0].Snippet != "About golang" {
		t.Fatalf("brave = %+v %v", r, err)
	}
	if _, err := Search(ctx, c, Brave, "", "", "x", 5); err == nil {
		t.Fatal("brave without a key")
	}
	if r, err := Search(ctx, c, SearXNG, "", srv.URL+"/searx/", "x", 5); err != nil || len(r) != 1 || r[0].Snippet != "from searx" {
		t.Fatalf("searxng = %+v %v", r, err)
	}
	if _, err := Search(ctx, c, Off, "", "", "x", 5); err == nil {
		t.Fatal("search while off")
	}
}
