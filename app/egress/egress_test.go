package egress

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
)

func setup(t *testing.T) (*Proxy, string, string) {
	t.Helper()
	p, err := Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "secure hello from ", r.Host) }))
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "plain hello; proxy auth seen: ", r.Header.Get("Proxy-Authorization") != "")
	}))
	t.Cleanup(tlsSrv.Close)
	t.Cleanup(plain.Close)
	// Names resolve to the local test servers; ports are mapped by name.
	_, tlsPort, _ := net.SplitHostPort(strings.TrimPrefix(tlsSrv.URL, "https://"))
	_, plainPort, _ := net.SplitHostPort(strings.TrimPrefix(plain.URL, "http://"))
	p.Resolve = func(_ context.Context, host string) ([]netip.Addr, error) {
		if host == "lan.test" {
			return []netip.Addr{netip.MustParseAddr("192.168.1.10")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	var d net.Dialer
	p.Dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, _ := net.SplitHostPort(addr)
		switch port {
		case "443":
			port = tlsPort
		case "80":
			port = plainPort
		}
		return d.DialContext(ctx, network, net.JoinHostPort(host, port))
	}
	p.AllowPrivate = true
	return p, tlsSrv.URL, plain.URL
}

func client(proxyURL string) *http.Client {
	u, _ := url.Parse(proxyURL)
	return &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(u), TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
}

func get(c *http.Client, u string) (string, error) {
	res, err := c.Get(u)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 {
		return string(b), fmt.Errorf("status %d", res.StatusCode)
	}
	return string(b), nil
}

func TestLeasesAllowOnlyTheirHosts(t *testing.T) {
	p, _, _ := setup(t)
	proxy, release := p.Lease([]string{"api.github.test", "*.githubusercontent.test"})
	c := client(proxy)
	if out, err := get(c, "https://api.github.test/user"); err != nil || !strings.Contains(out, "secure hello") {
		t.Fatalf("allowed host = %q %v", out, err)
	}
	if out, err := get(c, "https://raw.githubusercontent.test/x"); err != nil {
		t.Fatalf("allowed subdomain = %q %v", out, err)
	}
	if _, err := get(c, "https://evil.test/steal"); err == nil {
		t.Fatal("reached a host outside the lease")
	}
	if _, err := get(c, "https://githubusercontent.test/"); err == nil {
		t.Fatal("*.example matched the bare domain")
	}
	if out, err := get(c, "http://api.github.test/plain"); err != nil || !strings.Contains(out, "proxy auth seen: false") {
		t.Fatalf("plain http = %q %v (the proxy credential must not be forwarded)", out, err)
	}
	// Without a lease, or after release, nothing goes through.
	if _, err := get(client(strings.Replace(proxy, "http://", "http://nope", 1)), "https://api.github.test/"); err == nil {
		t.Fatal("an unknown token went through")
	}
	release()
	if _, err := get(c, "https://api.github.test/user"); err == nil {
		t.Fatal("a released lease still works")
	}
}

func TestPrivateAddressesAndPorts(t *testing.T) {
	p, _, _ := setup(t)
	p.AllowPrivate = false
	proxy, release := p.Lease([]string{"lan.test", "api.github.test"})
	defer release()
	c := client(proxy)
	if _, err := get(c, "https://lan.test/"); err == nil {
		t.Fatal("reached a LAN address")
	}
	if _, err := get(c, "https://api.github.test/"); err == nil {
		t.Fatal("reached loopback (the server itself)")
	}
	p.AllowPrivate = true
	if _, err := get(c, "https://api.github.test:8443/"); err == nil {
		t.Fatal("reached a port other than 443/80")
	}
}

func TestAllowedPatterns(t *testing.T) {
	cases := []struct {
		host string
		ok   bool
	}{{"github.com", true}, {"GitHub.com.", true}, {"api.github.com", false}, {"x.githubusercontent.com", true}, {"githubusercontent.com", false}, {"evilgithub.com", false}}
	for _, c := range cases {
		if got := Allowed([]string{"github.com", "*.githubusercontent.com"}, c.host); got != c.ok {
			t.Errorf("%s = %v", c.host, got)
		}
	}
}
