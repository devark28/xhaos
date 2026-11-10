package dictation

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/devark28/xhaos/app/workspace"
)

// fakeService is a dictation server for the tests: it checks the request
// shape, then answers with what the test's handler says.
func fakeService(t *testing.T, handle func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("/transcribe", handle)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

// closedURL is a loopback address with nothing listening on it.
func closedURL(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return "http://" + addr
}

func TestWarmServiceAnswersTheTake(t *testing.T) {
	s := db(t)
	marker := filepath.Join(t.TempDir(), "ran")
	ts := fakeService(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		if got := r.URL.Query().Get("language"); got != "de" {
			t.Errorf("language = %q", got)
		}
		if got := r.URL.Query().Get("prompt"); got != "Xhaos, Pi Zero 2 W" {
			t.Errorf("prompt = %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		if string(body[:4]) != "RIFF" || len(body) != 44+32000 {
			t.Errorf("body is not the take as WAV: %d bytes", len(body))
		}
		if binary.LittleEndian.Uint32(body[40:]) != 32000 {
			t.Errorf("data size in the WAV header is wrong")
		}
		w.Write([]byte("  hallo welt \n"))
	})
	s.cfg = Config{URL: ts.URL, Program: fakeSidecar(t, fmt.Sprintf("touch %q", marker))}
	if err := s.SetLanguage("de"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetHints(workspace.DefaultID, "Xhaos, Pi Zero 2 W"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Transcribe(workspace.DefaultID, pcm(32000))
	if err != nil {
		t.Fatal(err)
	}
	if got != "hallo welt" {
		t.Fatalf("transcript = %q", got)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the command ran although the service answered")
	}
}

func TestWarmServiceSlowTakeTimesOutWithoutFallback(t *testing.T) {
	s := db(t)
	marker := filepath.Join(t.TempDir(), "ran")
	ts := fakeService(t, func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body) // the server notices the client leaving only once the body is read
		select {           // a slow take: the client gives up first
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})
	s.cfg = Config{URL: ts.URL, Program: fakeSidecar(t, fmt.Sprintf("touch %q\necho late", marker)), Timeout: 300 * time.Millisecond}
	start := time.Now()
	_, err := s.Transcribe(workspace.DefaultID, pcm(32000))
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want a timeout", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("timeout took %s", elapsed)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a timed-out take was retried with the command")
	}
	// The cut take is a warm sample, a lower bound.
	samples, err := s.samples()
	if err != nil || len(samples) != 1 || !samples[0].Cut || !samples[0].Warm {
		t.Fatalf("samples = %+v, %v; want one cut warm take", samples, err)
	}
}

func TestWarmServiceErrorFallsBackToTheCommand(t *testing.T) {
	s := db(t)
	ts := fakeService(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model exploded", http.StatusInternalServerError)
	})
	s.cfg = Config{URL: ts.URL, Program: fakeSidecar(t, "echo from the command")}
	got, err := s.Transcribe(workspace.DefaultID, pcm(32000))
	if err != nil {
		t.Fatal(err)
	}
	if got != "from the command" {
		t.Fatalf("transcript = %q, want the command's", got)
	}
	samples, _ := s.samples()
	if len(samples) != 1 || samples[0].Warm {
		t.Fatalf("samples = %+v; the command's take must not count as warm", samples)
	}
}

// A busy service (one take at a time) answers 503 at once: the take runs with
// the command instead of waiting behind the other one.
func TestWarmServiceBusyFallsBackToTheCommand(t *testing.T) {
	s := db(t)
	ts := fakeService(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "busy with another take", http.StatusServiceUnavailable)
	})
	s.cfg = Config{URL: ts.URL, Program: fakeSidecar(t, "echo from the command")}
	got, err := s.Transcribe(workspace.DefaultID, pcm(32000))
	if err != nil {
		t.Fatal(err)
	}
	if got != "from the command" {
		t.Fatalf("transcript = %q, want the command's", got)
	}
}

func TestWarmServiceDownFallsBackToTheCommand(t *testing.T) {
	s := db(t)
	s.cfg = Config{URL: closedURL(t), Program: fakeSidecar(t, "echo from the command")}
	got, err := s.Transcribe(workspace.DefaultID, pcm(32000))
	if err != nil || got != "from the command" {
		t.Fatalf("got %q, %v; want the command's transcript", got, err)
	}
}

func TestWarmServiceErrorWithoutCommandIsReported(t *testing.T) {
	s := db(t)
	ts := fakeService(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model exploded", http.StatusInternalServerError)
	})
	s.cfg = Config{URL: ts.URL}
	_, err := s.Transcribe(workspace.DefaultID, pcm(32000))
	if err == nil || !strings.Contains(err.Error(), "status 500") || !strings.Contains(err.Error(), "model exploded") {
		t.Fatalf("err = %v, want the service's status and message", err)
	}
	if errors.Is(err, ErrNotConfigured) {
		t.Fatal("a failing service is not a configuration error")
	}
}

func TestWarmServiceDownWithoutCommandIsReported(t *testing.T) {
	s := db(t)
	s.cfg = Config{URL: closedURL(t)}
	_, err := s.Transcribe(workspace.DefaultID, pcm(32000))
	if err == nil || !strings.Contains(err.Error(), "could not be reached") {
		t.Fatalf("err = %v, want the service unreachable", err)
	}
}

func TestServiceAloneEnablesDictation(t *testing.T) {
	s := db(t)
	s.cfg = Config{URL: "http://127.0.0.1:8190"}
	if !s.Enabled() || !s.ServiceEnabled() {
		t.Fatal("a service alone should enable dictation")
	}
	s.cfg = Config{}
	if s.Enabled() || s.ServiceEnabled() {
		t.Fatal("nothing configured should be off")
	}
}

func TestServiceHealth(t *testing.T) {
	s := db(t)
	if h := s.Health(); h.OK || !strings.Contains(h.Detail, "-dictation-url") {
		t.Fatalf("no service: %+v", h)
	}
	ts := fakeService(t, func(w http.ResponseWriter, r *http.Request) {})
	s.cfg = Config{URL: ts.URL}
	if h := s.Health(); !h.OK {
		t.Fatalf("answering service: %+v", h)
	}
	busy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "loading", http.StatusServiceUnavailable)
	}))
	defer busy.Close()
	s.cfg = Config{URL: busy.URL}
	if h := s.Health(); h.OK || !strings.Contains(h.Detail, "status 503") {
		t.Fatalf("loading service: %+v", h)
	}
	s.cfg = Config{URL: closedURL(t)}
	if h := s.Health(); h.OK || h.Detail == "" {
		t.Fatalf("down service: %+v", h)
	}
}

func TestEstimateUsesTheKindOfTakeTheServerTakes(t *testing.T) {
	s := db(t)
	// Command takes: slow, with a load in each. Warm takes: fast.
	for _, l := range []float64{5, 30} {
		s.recordSpeed(l, 60+l, false, false)
	}
	for _, l := range []float64{5, 30} {
		s.recordSpeed(l, 1+l/10, false, true)
	}
	s.cfg = Config{URL: "http://127.0.0.1:8190"}
	if got, _ := s.Estimate(30); got != 4 {
		t.Fatalf("warm estimate for 30 s = %v, want 4", got)
	}
	s.cfg = Config{}
	if got, _ := s.Estimate(30); got != 90 {
		t.Fatalf("command estimate for 30 s = %v, want 90", got)
	}
}

func TestEstimateWithOnlyCommandTakesUsesThem(t *testing.T) {
	s := db(t)
	s.recordSpeed(10, 20, false, false)
	s.cfg = Config{URL: "http://127.0.0.1:8190"}
	if got, _ := s.Estimate(10); got != 20 {
		t.Fatalf("estimate = %v, want the command's 20 until warm takes exist", got)
	}
}

func TestKeptRecordingSaysWhichPathTookIt(t *testing.T) {
	s := db(t)
	dir := filepath.Join(t.TempDir(), "dictation-debug")
	ts := fakeService(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("hi")) })
	s.cfg = Config{URL: ts.URL, Recordings: dir}
	if err := s.SetKeeping(true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Transcribe(workspace.DefaultID, pcm(32000)); err != nil {
		t.Fatal(err)
	}
	log, err := os.ReadFile(filepath.Join(dir, logFile))
	if err != nil || !strings.Contains(string(log), "service") {
		t.Fatalf("log = %q, %v; want it to say the service took the take", log, err)
	}
}
