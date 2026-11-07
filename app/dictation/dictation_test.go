package dictation

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/devark28/xhaos/app/apperr"
	"github.com/devark28/xhaos/app/sqlitedb"
	"github.com/devark28/xhaos/app/workspace"
)

// db opens a migrated database holding the default workspace.
func db(t *testing.T) *Service {
	t.Helper()
	conn, err := sqlitedb.Open(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := workspace.Migrate(conn); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(conn); err != nil {
		t.Fatal(err)
	}
	return NewService(conn, Config{})
}

// fakeSidecar writes a POSIX shell script with body into a temp dir and
// returns its path.
func fakeSidecar(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sidecar.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// pcm returns n bytes of audio (a non-zero ramp, so it is not silence).
func pcm(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i%251 + 1)
	}
	return b
}

func TestTranscribeReturnsTrimmedTranscript(t *testing.T) {
	s := db(t)
	marker := filepath.Join(t.TempDir(), "wav-path")
	// Checks that the WAV exists with a RIFF header, that {model} stays one
	// argument even with a space in it, then prints a padded transcript.
	s.cfg = Config{
		Program: fakeSidecar(t, fmt.Sprintf(`
case "$(head -c 4 "$1")" in RIFF) ;; *) echo "not a wav" >&2; exit 3;; esac
[ "$2" = "--model" ] || exit 4
[ "$3" = "/models/parakeet dir" ] || exit 5
echo "$1" > %q
echo "  hello world  "`, marker)),
		Args:  "{wav} --model {model}",
		Model: "/models/parakeet dir",
	}
	got, err := s.Transcribe(workspace.DefaultID, pcm(3200))
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello world" {
		t.Fatalf("transcript = %q", got)
	}
	raw, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	wav := strings.TrimSpace(string(raw))
	if _, err := os.Stat(wav); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("audio not deleted after transcription: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(wav)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temp dir not removed: %v", err)
	}
}

func TestRejectsBadAudioBeforeRunning(t *testing.T) {
	s := db(t)
	marker := filepath.Join(t.TempDir(), "ran")
	s.cfg = Config{Program: fakeSidecar(t, fmt.Sprintf("touch %q\necho x", marker))}
	for name, audio := range map[string][]byte{
		"empty":     nil,
		"odd bytes": pcm(3),
		"too long":  make([]byte, MaxBytes+2),
	} {
		_, err := s.Transcribe(workspace.DefaultID, audio)
		if !errors.Is(err, apperr.Invalid) {
			t.Errorf("%s: err = %v, want invalid", name, err)
		}
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("sidecar ran for rejected audio")
	}
}

func TestNotConfigured(t *testing.T) {
	s := db(t) // Config{}: no sidecar
	if s.Enabled() {
		t.Fatal("Enabled() = true without a sidecar")
	}
	if _, err := s.Transcribe(workspace.DefaultID, pcm(3200)); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("err = %v, want ErrNotConfigured", err)
	}
}

func TestFailureReportsStderr(t *testing.T) {
	s := db(t)
	s.cfg = Config{Program: fakeSidecar(t, "echo model missing >&2\nexit 2")}
	_, err := s.Transcribe(workspace.DefaultID, pcm(3200))
	if err == nil || !strings.Contains(err.Error(), "model missing") {
		t.Fatalf("err = %v, want the sidecar's stderr", err)
	}
	if errors.Is(err, ErrNotConfigured) {
		t.Fatal("a failing sidecar is not a configuration error")
	}
}

func TestTimeoutKillsProcessGroup(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("needs /proc to check the child process")
	}
	s := db(t)
	childFile := filepath.Join(t.TempDir(), "child")
	// The sidecar starts a child and waits; the child must die with it.
	s.cfg = Config{
		Program: fakeSidecar(t, fmt.Sprintf("sleep 30 &\necho $! > %q\nwait", childFile)),
		Timeout: 300 * time.Millisecond,
	}
	start := time.Now()
	_, err := s.Transcribe(workspace.DefaultID, pcm(3200))
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want a timeout", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("timeout took %s", elapsed)
	}
	raw, err := os.ReadFile(childFile)
	if err != nil {
		t.Fatal(err)
	}
	pid := strings.TrimSpace(string(raw))
	deadline := time.Now().Add(3 * time.Second)
	for alive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("child %s still running after the timeout", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// alive reports whether a process exists and is not a zombie.
func alive(pid string) bool {
	raw, err := os.ReadFile("/proc/" + pid + "/stat")
	if err != nil {
		return false
	}
	s := string(raw)
	i := strings.LastIndex(s, ")")
	return i >= 0 && i+2 < len(s) && s[i+2] != 'Z'
}

func TestTimeoutScalesWithLength(t *testing.T) {
	s := NewService(nil, Config{})
	const second = SampleRate * bytesPerSample
	cases := map[string]struct {
		n    int
		want time.Duration
	}{
		"1 s":     {second, 15*time.Second + 500*time.Millisecond},
		"10 s":    {10 * second, 20 * time.Second},
		"5 min":   {MaxBytes, 165 * time.Second},
		"0 bytes": {0, 15 * time.Second},
	}
	for name, c := range cases {
		if got := s.timeout(c.n); got != c.want {
			t.Errorf("%s: timeout %s, want %s", name, got, c.want)
		}
	}
}

func TestTimeoutOverrideWins(t *testing.T) {
	s := NewService(nil, Config{Timeout: 7 * time.Second})
	if got := s.timeout(MaxBytes); got != 7*time.Second {
		t.Fatalf("timeout %s, want the configured 7s", got)
	}
}

func TestKeepingIsOffByDefaultAndWritesRecordings(t *testing.T) {
	s := db(t)
	dir := filepath.Join(t.TempDir(), "dictation-debug")
	s.cfg = Config{Program: fakeSidecar(t, "echo hello"), Recordings: dir}
	on, err := s.Keeping()
	if err != nil || on {
		t.Fatalf("Keeping() = %v, %v; want off", on, err)
	}
	if _, err := s.Transcribe(workspace.DefaultID, pcm(3200)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recordings kept while the setting is off: %v", err)
	}
	if err := s.SetKeeping(true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Transcribe(workspace.DefaultID, pcm(3200)); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	wavs := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".wav" {
			wavs++
		}
	}
	if wavs != 1 {
		t.Fatalf("kept %d WAV files, want 1", wavs)
	}
	log, err := os.ReadFile(filepath.Join(dir, logFile))
	if err != nil || !strings.Contains(string(log), "hello") {
		t.Fatalf("log = %q, %v; want the transcript start", log, err)
	}
	if err := s.DeleteRecordings(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Delete all left the folder: %v", err)
	}
}

func TestWriteWAVHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "u.wav")
	data := pcm(640)
	if err := writeWAV(path, data); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 44+len(data) {
		t.Fatalf("size = %d, want %d", len(raw), 44+len(data))
	}
	if string(raw[0:4]) != "RIFF" || string(raw[8:12]) != "WAVE" || string(raw[36:40]) != "data" {
		t.Fatal("bad WAV chunk ids")
	}
	if rate := binary.LittleEndian.Uint32(raw[24:]); rate != SampleRate {
		t.Fatalf("sample rate = %d", rate)
	}
	if n := binary.LittleEndian.Uint32(raw[40:]); n != uint32(len(data)) {
		t.Fatalf("data size = %d", n)
	}
}

func TestArgvPutsHintsInsideTheirField(t *testing.T) {
	s := NewService(nil, Config{Args: "{wav} --prompt={prompt} {prompt} --model {model}", Model: "/m d"})
	got := s.argv("/tmp/u.wav", "Xhaos, Verlet, Pi Zero 2 W.")
	want := []string{"/tmp/u.wav", "--prompt=Xhaos, Verlet, Pi Zero 2 W.", "Xhaos, Verlet, Pi Zero 2 W.", "--model", "/m d"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("argv = %q, want %q", got, want)
	}
}

func TestArgvDropsAnEmptyPromptField(t *testing.T) {
	s := NewService(nil, Config{Args: "{wav} {prompt} --prompt={prompt}"})
	got := s.argv("/tmp/u.wav", "")
	want := []string{"/tmp/u.wav", "--prompt="}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("argv = %q, want %q", got, want)
	}
}

func TestHintsAreKeptOnOneLine(t *testing.T) {
	s := db(t)
	if err := s.SetHints(workspace.DefaultID, "  Xhaos,\n  Verlet   and the Duo.  "); err != nil {
		t.Fatal(err)
	}
	got, err := s.Hints(workspace.DefaultID)
	if err != nil {
		t.Fatal(err)
	}
	if got != "Xhaos, Verlet and the Duo." {
		t.Fatalf("hints = %q", got)
	}
	if err := s.SetHints(workspace.DefaultID, strings.Repeat("x", maxHints+1)); !errors.Is(err, apperr.Invalid) {
		t.Fatalf("long hints: err = %v, want invalid", err)
	}
}
