// Package egress is the proxy that sealed commands (commands holding
// secrets, package sandbox) reach the network through. The sandbox lets
// them connect only to this proxy's port; the proxy lets each command
// reach only the hosts its secrets allow.
//
// A command gets a lease: a random token, sent as the proxy's user name
// (HTTPS_PROXY=http://<token>:x@127.0.0.1:<port>, which curl, git, Go and
// npm all honour), naming the hosts it may reach. The proxy resolves names
// itself and refuses private, loopback and link-local addresses, so a lease
// for a public host cannot be turned toward the LAN or the server.
//
// It handles CONNECT (HTTPS, the usual case) and plain http:// requests.
package egress

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Rule lets a lease reach a host on some ports. Pattern is an exact name or
// IP address, or "*.example.com" for its subdomains. An exact pattern may
// resolve to a private (LAN) address: the owner named that host; a wildcard
// never may. Loopback is never reachable (that would be the server itself).
type Rule struct {
	Pattern string
	Ports   []int
}

// Web is the usual rule for a token: HTTPS, and HTTP.
func Web(pattern string) Rule { return Rule{Pattern: pattern, Ports: []int{443, 80}} }

type lease struct {
	rules []Rule
	conns map[net.Conn]bool // open tunnels, closed on release
}

type Proxy struct {
	ln     net.Listener
	mu     sync.Mutex
	leases map[string]lease
	// Resolve looks names up (tests replace it); Dial connects.
	Resolve func(ctx context.Context, host string) ([]netip.Addr, error)
	Dial    func(ctx context.Context, network, addr string) (net.Conn, error)
	// AllowPrivate lets tests reach 127.0.0.1.
	AllowPrivate bool
}

// Start listens on a random loopback port.
func Start() (*Proxy, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	var d net.Dialer
	p := &Proxy{ln: ln, leases: map[string]lease{}, Dial: d.DialContext}
	p.Resolve = func(ctx context.Context, host string) ([]netip.Addr, error) {
		return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	}
	srv := &http.Server{Handler: p, ReadHeaderTimeout: 20 * time.Second}
	go srv.Serve(ln)
	return p, nil
}

func (p *Proxy) Port() int { return p.ln.Addr().(*net.TCPAddr).Port }

func (p *Proxy) Close() error { return p.ln.Close() }

// Lease allows rules until release is called, and returns the proxy URL to
// give the command.
func (p *Proxy) Lease(rules []Rule) (proxyURL string, release func()) {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	token := hex.EncodeToString(b)
	clean := make([]Rule, 0, len(rules))
	for _, r := range rules {
		if r.Pattern = strings.ToLower(strings.TrimSpace(r.Pattern)); r.Pattern != "" {
			clean = append(clean, r)
		}
	}
	p.mu.Lock()
	p.leases[token] = lease{rules: clean, conns: map[net.Conn]bool{}}
	p.mu.Unlock()
	return fmt.Sprintf("http://%s:x@127.0.0.1:%d", token, p.Port()), func() {
		// Also end its open connections: a client reuses them, and a
		// process the command left behind must not keep a way out.
		p.mu.Lock()
		l := p.leases[token]
		delete(p.leases, token)
		p.mu.Unlock()
		for c := range l.conns {
			c.Close()
		}
	}
}

// Allowed reports whether host matches one of the patterns.
func Allowed(patterns []string, host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, p := range patterns {
		if p == host {
			return true
		}
		if suffix, ok := strings.CutPrefix(p, "*."); ok && strings.HasSuffix(host, "."+suffix) {
			return true
		}
	}
	return false
}

// track records an open connection under the lease, unless it was released.
func (p *Proxy) track(token string, c net.Conn, open bool) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	l, ok := p.leases[token]
	if !ok {
		return false
	}
	if open {
		l.conns[c] = true
	} else {
		delete(l.conns, c)
	}
	return true
}

func (p *Proxy) leaseOf(r *http.Request) (string, lease, bool) {
	auth, ok := strings.CutPrefix(r.Header.Get("Proxy-Authorization"), "Basic ")
	if !ok {
		return "", lease{}, false
	}
	raw, err := base64.StdEncoding.DecodeString(auth)
	if err != nil {
		return "", lease{}, false
	}
	token, _, _ := strings.Cut(string(raw), ":")
	p.mu.Lock()
	defer p.mu.Unlock()
	l, ok := p.leases[token]
	return token, l, ok
}

// match finds the rule letting host:port through.
func match(rules []Rule, host string, port int) (Rule, string) {
	var names []string
	for _, r := range rules {
		if Allowed([]string{r.Pattern}, host) {
			for _, p := range r.Ports {
				if p == port {
					return r, ""
				}
			}
			return Rule{}, fmt.Sprintf("port %d is not allowed for %s (only %v)", port, host, r.Ports)
		}
		names = append(names, r.Pattern)
	}
	if len(names) == 0 {
		return Rule{}, "this command's secrets allow no network"
	}
	return Rule{}, fmt.Sprintf("%s is not among this command's allowed hosts (%s)", host, strings.Join(names, ", "))
}

// lan reports whether an address is a private network's (reachable only
// for a host the owner named exactly).
func lan(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsPrivate() || netip.MustParsePrefix("100.64.0.0/10").Contains(a)
}

// public reports whether an address is on the open internet.
func public(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsGlobalUnicast() && !a.IsPrivate() && !a.IsLoopback() && !a.IsLinkLocalUnicast() &&
		!netip.MustParsePrefix("100.64.0.0/10").Contains(a) && !netip.MustParsePrefix("fc00::/7").Contains(a)
}

// dial connects to host:port for a lease, after the checks.
func (p *Proxy) dial(ctx context.Context, l lease, hostport string) (net.Conn, error) {
	host, portText, err := net.SplitHostPort(hostport)
	if err != nil {
		return nil, err
	}
	port, _ := strconv.Atoi(portText)
	rule, why := match(l.rules, host, port)
	if why != "" {
		return nil, errors.New(why)
	}
	exact := !strings.HasPrefix(rule.Pattern, "*.")
	var addrs []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{ip}
	} else if addrs, err = p.Resolve(ctx, host); err != nil {
		return nil, err
	}
	for _, a := range addrs {
		if p.AllowPrivate || public(a) || (exact && lan(a)) {
			continue
		}
		if lan(a) {
			return nil, fmt.Errorf("%s resolves to %s, a private address; name the host exactly (not *.) to allow it", host, a)
		}
		return nil, fmt.Errorf("%s resolves to %s, which is not reachable through the proxy", host, a)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("%s has no addresses", host)
	}
	// Connect to the address we checked (not the name again), so a second
	// lookup cannot point somewhere else.
	return p.Dial(ctx, "tcp", net.JoinHostPort(addrs[0].Unmap().String(), portText))
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token, l, ok := p.leaseOf(r)
	if !ok {
		w.Header().Set("Proxy-Authenticate", `Basic realm="xhaos"`)
		http.Error(w, "xhaos egress: this proxy only serves commands that were given secrets", http.StatusProxyAuthRequired)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if r.Method == http.MethodConnect {
		upstream, err := p.dial(ctx, l, r.Host)
		if err != nil {
			slog.Warn("egress: refused", "host", r.Host, "err", err)
			http.Error(w, "xhaos egress: "+err.Error(), http.StatusForbidden)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			upstream.Close()
			http.Error(w, "no hijack", http.StatusInternalServerError)
			return
		}
		client, buf, err := hj.Hijack()
		if err != nil {
			upstream.Close()
			return
		}
		if !p.track(token, client, true) || !p.track(token, upstream, true) {
			client.Close()
			upstream.Close()
			return
		}
		defer p.track(token, client, false)
		defer p.track(token, upstream, false)
		fmt.Fprint(client, "HTTP/1.1 200 Connection established\r\n\r\n")
		pipe(client, buf, upstream)
		return
	}
	// Plain http://host/path through the proxy.
	if r.URL.Host == "" || r.URL.Scheme != "http" {
		http.Error(w, "xhaos egress: use https (CONNECT) or an absolute http:// URL", http.StatusBadRequest)
		return
	}
	hostport := r.URL.Host
	if r.URL.Port() == "" {
		hostport += ":80"
	}
	upstream, err := p.dial(ctx, l, hostport)
	if err != nil {
		http.Error(w, "xhaos egress: "+err.Error(), http.StatusForbidden)
		return
	}
	defer upstream.Close()
	out := r.Clone(ctx)
	out.Header.Del("Proxy-Authorization")
	out.Header.Del("Proxy-Connection")
	out.RequestURI = ""
	if err := out.Write(upstream); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	res, err := http.ReadResponse(bufio.NewReader(upstream), out)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer res.Body.Close()
	for k, v := range res.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(res.StatusCode)
	io.Copy(w, res.Body)
}

func pipe(client net.Conn, buf *bufio.ReadWriter, upstream net.Conn) {
	done := make(chan struct{}, 2)
	go func() {
		// Bytes the client sent along with CONNECT are already buffered.
		if n := buf.Reader.Buffered(); n > 0 {
			b, _ := buf.Reader.Peek(n)
			upstream.Write(b)
		}
		io.Copy(upstream, client)
		closeWrite(upstream)
		done <- struct{}{}
	}()
	go func() { io.Copy(client, upstream); closeWrite(client); done <- struct{}{} }()
	<-done
	<-done
	client.Close()
	upstream.Close()
}

func closeWrite(c net.Conn) {
	if t, ok := c.(interface{ CloseWrite() error }); ok {
		t.CloseWrite()
	}
}
