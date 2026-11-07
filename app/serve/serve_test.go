package serve

import (
	"bufio"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func get(t *testing.T, url string, header map[string]string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	// A transport that does not decompress for us, to see the wire.
	res, err := (&http.Transport{DisableCompression: true}).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var body io.Reader = res.Body
	if res.Header.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		body = zr
	}
	b, _ := io.ReadAll(body)
	return res, string(b)
}

func TestCompressesWhatShrinksAndNothingElse(t *testing.T) {
	big := strings.Repeat(`{"event":"tool_call","text":"hello"},`, 2000)
	mux := http.NewServeMux()
	mux.HandleFunc("/json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, big)
	})
	mux.HandleFunc("/small", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Length", "2")
		io.WriteString(w, "hi")
	})
	mux.HandleFunc("/png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		io.WriteString(w, strings.Repeat("x", 5000))
	})
	mux.HandleFunc("/sniffed", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "<!doctype html><p>"+strings.Repeat("page ", 1000))
	})
	mux.HandleFunc("/missing", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, strings.Repeat("not found ", 500), http.StatusNotFound)
	})
	srv := httptest.NewServer(Compress(mux))
	defer srv.Close()
	gz := map[string]string{"Accept-Encoding": "gzip, br"}

	res, body := get(t, srv.URL+"/json", gz)
	if res.Header.Get("Content-Encoding") != "gzip" || body != big || res.Header.Get("Content-Length") == "75000" {
		t.Errorf("json: encoding %q, intact %v", res.Header.Get("Content-Encoding"), body == big)
	}
	if !strings.Contains(res.Header.Get("Vary"), "Accept-Encoding") {
		t.Error("json: no Vary: Accept-Encoding")
	}
	if res, body := get(t, srv.URL+"/json", nil); res.Header.Get("Content-Encoding") != "" || body != big {
		t.Error("compressed for a client that did not ask")
	}
	if res, _ := get(t, srv.URL+"/json", map[string]string{"Accept-Encoding": "gzip;q=0, deflate"}); res.Header.Get("Content-Encoding") != "" {
		t.Error("compressed although gzip;q=0")
	}
	if res, body := get(t, srv.URL+"/small", gz); res.Header.Get("Content-Encoding") != "" || body != "hi" {
		t.Error("compressed a 2-byte body")
	}
	if res, _ := get(t, srv.URL+"/png", gz); res.Header.Get("Content-Encoding") != "" {
		t.Error("compressed an image")
	}
	if res, body := get(t, srv.URL+"/sniffed", gz); res.Header.Get("Content-Encoding") != "gzip" || !strings.HasPrefix(body, "<!doctype html>") {
		t.Errorf("untyped html: %q", res.Header.Get("Content-Encoding"))
	}
	if res, _ := get(t, srv.URL+"/missing", gz); res.StatusCode != 404 || res.Header.Get("Content-Encoding") != "" {
		t.Errorf("error response: %d %q", res.StatusCode, res.Header.Get("Content-Encoding"))
	}
}

// Server-Sent Events stream through uncompressed, each event as it is flushed.
func TestEventStreamsPassThrough(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(Compress(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		rc := http.NewResponseController(w)
		io.WriteString(w, "data: one\n\n")
		if err := rc.Flush(); err != nil {
			t.Errorf("flush: %v", err)
		}
		<-release
		io.WriteString(w, "data: two\n\n")
	})))
	defer srv.Close()
	defer close(release)
	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Accept-Encoding", "gzip")
	res, err := (&http.Transport{DisableCompression: true}).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.Header.Get("Content-Encoding") != "" {
		t.Fatal("compressed an event stream")
	}
	line := make(chan string, 1)
	go func() { l, _ := bufio.NewReader(res.Body).ReadString('\n'); line <- l }()
	select {
	case l := <-line:
		if l != "data: one\n" {
			t.Errorf("first event = %q", l)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the first event was held back until the response ended")
	}
}

func TestETagAnswersNotModified(t *testing.T) {
	calls := 0
	static := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/javascript")
		io.WriteString(w, strings.Repeat("console.log(1);", 500))
	})
	skip := func(p string) bool { return strings.HasPrefix(p, "/xhaos/") }
	srv := httptest.NewServer(Compress(ETag(skip, static)))
	defer srv.Close()
	gz := map[string]string{"Accept-Encoding": "gzip"}

	res, body := get(t, srv.URL+"/dist/index.js", gz)
	tag := res.Header.Get("ETag")
	if res.StatusCode != 200 || tag == "" || len(body) != 15*500 {
		t.Fatalf("first load: %d etag %q, %d bytes", res.StatusCode, tag, len(body))
	}
	res, body = get(t, srv.URL+"/dist/index.js", map[string]string{"Accept-Encoding": "gzip", "If-None-Match": tag})
	if res.StatusCode != http.StatusNotModified || body != "" || res.Header.Get("ETag") == "" {
		t.Errorf("revalidation: %d, %d bytes", res.StatusCode, len(body))
	}
	if res, _ := get(t, srv.URL+"/dist/index.js", map[string]string{"If-None-Match": `"other"`}); res.StatusCode != 200 {
		t.Errorf("a stale tag got %d", res.StatusCode)
	}
	// Live and per-user paths are left alone.
	if res, _ := get(t, srv.URL+"/xhaos/events", nil); res.Header.Get("ETag") != "" {
		t.Error("tagged a skipped path")
	}
	if calls != 4 {
		t.Errorf("handler ran %d times", calls)
	}
}
