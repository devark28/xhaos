// Package localhttp reaches the server's local helper services (the
// command classifier, the warm dictation service) over HTTP, on a Unix
// socket when the address is unix:/path/to.sock.
//
// A Unix socket in a folder only the server's user can reach is what keeps
// a sandboxed command from answering in a service's place: on a loopback
// TCP port, any process may bind it while the service is down or restarting
// (commands that are not sealed may listen), and the server would trust
// whatever answers there, an "allow" from the classifier included. Sandboxed
// commands cannot open Unix sockets (seccomp) or reach the service's folder
// (Landlock), and cannot create a socket where the service's would be.
package localhttp

import (
	"context"
	"net"
	"net/http"
	"strings"
)

// IsUnix reports whether addr names a Unix socket (unix:/path or unix:///path).
func IsUnix(addr string) bool { return socketPath(addr) != "" }

func socketPath(addr string) string {
	p, ok := strings.CutPrefix(addr, "unix:")
	if !ok {
		return ""
	}
	if rest, ok := strings.CutPrefix(p, "//"); ok {
		p = rest
	}
	if !strings.HasPrefix(p, "/") {
		return ""
	}
	return p
}

// New returns the base URL to put paths after and the client to send the
// requests with, for a service at addr: unix:/path/to.sock, or an
// http(s):// base URL used as it is.
func New(addr string) (base string, client *http.Client) {
	sock := socketPath(addr)
	if sock == "" {
		return strings.TrimRight(addr, "/"), &http.Client{}
	}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
		DisableCompression: true,
	}
	return "http://localhost", &http.Client{Transport: tr}
}
