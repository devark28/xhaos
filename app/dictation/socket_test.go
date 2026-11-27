package dictation

import (
	"net"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/devark28/xhaos/app/workspace"
)

// The installer runs the warm service on a Unix socket in a private folder
// (a loopback port could be taken by a command while it is down): takes go
// there with -dictation-url unix:/path.
func TestWarmServiceOverUnixSocket(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "dictation.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("/transcribe", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("over the socket")) })
	srv := &http.Server{Handler: mux}
	go srv.Serve(l)
	defer srv.Close()

	s := NewService(db(t).db, Config{URL: "unix:" + sock})
	if h := s.Health(); !h.OK {
		t.Fatalf("health: %+v", h)
	}
	got, err := s.Transcribe(workspace.DefaultID, pcm(16000))
	if err != nil || got != "over the socket" {
		t.Fatalf("transcript = %q, %v", got, err)
	}
}
