// Package dictation turns one spoken utterance into text. The browser records
// the whole utterance (the user clicks the microphone, speaks, clicks again)
// and sends it as 16 kHz mono signed 16-bit little-endian PCM; the reply is
// the English transcript. There is no live or streaming text.
//
// Transcription runs in an external sidecar program, so the server stays
// pure Go (no cgo). The server writes the utterance to a WAV file in a private
// temp directory, runs the sidecar on it, reads the transcript from its
// stdout, and deletes the audio before returning. Audio is never stored
// anywhere else.
//
// Configuration (cmd/xhaos flags, passed as Options.Dictation):
//
//	-dictation-cmd   the sidecar program. Empty turns dictation off.
//	-dictation-args  the argument template, split on spaces. {wav} is replaced
//	                 with the WAV path and {model} with -dictation-model. Empty
//	                 means the WAV path alone.
//	-dictation-model the model directory.
//
// Installing a sidecar (not part of this package): build or install an
// offline speech-to-text program that reads a WAV file and prints the
// transcript on stdout. One option is a sherpa-onnx offline recognizer with
// an NVIDIA Parakeet TDT int8 model (English). Put its model files in a
// directory and point -dictation-model at it. The exact sidecar CLI flags are
// UNVERIFIED here: check the sherpa-onnx docs for your version and write the
// template to match. Nothing in this package downloads a model.
//
// The sidecar gets a timeout that grows with the utterance (capped at 60s). On
// timeout its whole process group is killed.
package dictation

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/devark28/xhaos/app/apperr"
	"github.com/devark28/xhaos/app/workspace"
)

// SampleRate is in Hz (mono); bytesPerSample is signed 16-bit little-endian.
const (
	SampleRate     = 16000
	bytesPerSample = 2
)

// MaxDuration is the longest utterance accepted.
const MaxDuration = 5 * time.Minute

// MaxBytes is the largest audio accepted (MaxDuration of 16 kHz mono PCM16).
const MaxBytes = int(MaxDuration/time.Second) * SampleRate * bytesPerSample

// Timeouts for the sidecar: a base plus twice the utterance length, capped.
const (
	baseTimeout = 5 * time.Second
	maxTimeout  = 60 * time.Second
	waitDelay   = 5 * time.Second // after a kill, wait this long for pipes to close
	maxOutput   = 1 << 20         // stdout kept, in bytes
	maxErrText  = 500             // stderr quoted in errors, in bytes
)

// ErrNotConfigured means no sidecar is set (-dictation-cmd is empty).
var ErrNotConfigured = errors.New("dictation is not configured: start the server with -dictation-cmd")

// Config says how to run the sidecar. See the package comment.
type Config struct {
	Program string // sidecar executable; "" turns dictation off
	Args    string // argument template: fields split on spaces; {wav}, {model}
	Model   string // model directory, for {model}
}

// Service is the client-facing API.
type Service struct {
	db   *sql.DB
	cfg  Config
	wait time.Duration // tests only: fixed sidecar timeout (0: from the audio length)
}

// NewService returns the dictation service. It needs the database only to
// check that the workspace exists.
func NewService(db *sql.DB, cfg Config) *Service {
	return &Service{db: db, cfg: cfg}
}

// Enabled reports whether a sidecar is configured. The page calls it to show
// or hide the microphone.
func (s *Service) Enabled() bool { return s.cfg.Program != "" }

// Transcribe returns the English transcript of one utterance: 16 kHz mono
// signed 16-bit little-endian PCM (pcm16le, base64 on the wire). It
// returns ErrNotConfigured when no sidecar is set.
func (s *Service) Transcribe(workspaceID string, pcm16le []byte) (string, error) {
	if _, err := workspace.Get(s.db, workspaceID); err != nil {
		return "", err
	}
	if !s.Enabled() {
		return "", ErrNotConfigured
	}
	if err := checkAudio(pcm16le); err != nil {
		return "", err
	}
	// The temp directory is private (0700) and holds only this utterance.
	dir, err := os.MkdirTemp("", "xhaos-dictation-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir) // the audio goes as soon as the transcript is read
	wav := filepath.Join(dir, "utterance.wav")
	if err := writeWAV(wav, pcm16le); err != nil {
		return "", err
	}
	timeout := s.timeout(len(pcm16le))
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := s.run(ctx, dir, wav)
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("dictation timed out after %s", timeout)
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// checkAudio rejects audio that is empty, odd-sized, or longer than MaxDuration.
func checkAudio(pcm []byte) error {
	switch {
	case len(pcm) == 0:
		return fmt.Errorf("%w: no audio", apperr.Invalid)
	case len(pcm)%bytesPerSample != 0:
		return fmt.Errorf("%w: audio is not 16-bit samples", apperr.Invalid)
	case len(pcm) > MaxBytes:
		return fmt.Errorf("%w: utterance is longer than %s", apperr.Invalid, MaxDuration)
	}
	return nil
}

// timeout is a base plus twice the utterance length, capped at maxTimeout.
func (s *Service) timeout(n int) time.Duration {
	if s.wait > 0 {
		return s.wait
	}
	seconds := float64(n) / float64(SampleRate*bytesPerSample)
	d := baseTimeout + time.Duration(2*seconds*float64(time.Second))
	return min(d, maxTimeout)
}

// argv fills the template: each field is split on spaces first, so a model
// directory with spaces stays one argument.
func (s *Service) argv(wav string) []string {
	fields := strings.Fields(s.cfg.Args)
	if len(fields) == 0 {
		fields = []string{"{wav}"}
	}
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.ReplaceAll(f, "{wav}", wav)
		f = strings.ReplaceAll(f, "{model}", s.cfg.Model)
		out = append(out, f)
	}
	return out
}

// run starts the sidecar in dir and returns its stdout. The process group is
// killed when ctx ends.
func (s *Service) run(ctx context.Context, dir, wav string) (string, error) {
	cmd := exec.CommandContext(ctx, s.cfg.Program, s.argv(wav)...)
	cmd.Dir = dir
	// Own process group, so a timeout kills the sidecar's children too.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = waitDelay
	var stdout capped
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > maxErrText {
			msg = msg[:maxErrText] + "…"
		}
		if msg != "" {
			return "", fmt.Errorf("dictation failed: %v: %s", err, msg)
		}
		return "", fmt.Errorf("dictation failed: %v", err)
	}
	return stdout.String(), nil
}

// capped keeps the first maxOutput bytes written to it and drops the rest.
type capped struct{ bytes.Buffer }

func (c *capped) Write(p []byte) (int, error) {
	if room := maxOutput - c.Len(); room > 0 {
		c.Buffer.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

// writeWAV writes pcm as a canonical 44-byte-header WAV file (PCM, mono,
// 16 kHz, 16-bit). The file is created exclusively with mode 0600.
func writeWAV(path string, pcm []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	hdr := make([]byte, 44)
	copy(hdr[0:], "RIFF")
	binary.LittleEndian.PutUint32(hdr[4:], uint32(36+len(pcm)))
	copy(hdr[8:], "WAVE")
	copy(hdr[12:], "fmt ")
	binary.LittleEndian.PutUint32(hdr[16:], 16)                        // fmt chunk size
	binary.LittleEndian.PutUint16(hdr[20:], 1)                         // PCM
	binary.LittleEndian.PutUint16(hdr[22:], 1)                         // mono
	binary.LittleEndian.PutUint32(hdr[24:], SampleRate)                // sample rate
	binary.LittleEndian.PutUint32(hdr[28:], SampleRate*bytesPerSample) // byte rate
	binary.LittleEndian.PutUint16(hdr[32:], bytesPerSample)            // block align
	binary.LittleEndian.PutUint16(hdr[34:], 8*bytesPerSample)          // bits per sample
	copy(hdr[36:], "data")
	binary.LittleEndian.PutUint32(hdr[40:], uint32(len(pcm)))
	_, werr := f.Write(hdr)
	if werr == nil {
		_, werr = f.Write(pcm)
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}
