// Package dictation turns one spoken utterance into text. The browser records
// the whole utterance (the user clicks the microphone, speaks, clicks again)
// and sends it as 16 kHz mono signed 16-bit little-endian PCM; the reply is
// the transcript. There is no live or streaming text.
//
// Transcription runs in an external sidecar program, so the server stays
// pure Go (no cgo). The server writes the utterance to a WAV file in a private
// temp directory, runs the sidecar on it, reads the transcript from its
// stdout, and deletes the audio before returning. The audio is kept only when
// the server's "keep recordings" setting is on (see Keeping).
//
// A warm service (warm.go) keeps the model loaded: scripts/dictation/stt.py
// --serve, on the loopback. With -dictation-url set, each take goes to it; when
// it is down or fails, the take goes to the command, if -dictation-cmd is also
// set. Without the command, the error is reported.
//
// Configuration (cmd/xhaos flags, passed as Options.Dictation):
//
//	-dictation-url     the warm service's base URL, e.g. http://127.0.0.1:8190. Empty: none.
//	-dictation-cmd     the sidecar program. Empty turns dictation off.
//	-dictation-args    the argument template, split on spaces. {wav} is the WAV
//	                   path, {model} is -dictation-model, {language} is the
//	                   dictation language (Settings → Dictation; "en" by default,
//	                   "auto" to detect), and {prompt} is the workspace's dictation
//	                   hints. {prompt} is substituted inside its field, so
//	                   "--prompt={prompt}" passes the hints as one argument even
//	                   with spaces; a field that is only "{prompt}" is dropped when
//	                   there are no hints. Empty means the WAV path alone.
//	-dictation-model   the model directory.
//	-dictation-timeout a fixed sidecar time limit; 0 scales it with the audio.
//
// Installing a sidecar: scripts/dictation/stt.py is the reference sidecar
// (faster-whisper). It reads a 16 kHz WAV and prints the transcript on stdout.
// Nothing in this package downloads a model.
//
// The limit, for the warm service's answer or the sidecar's run, is 60 s plus
// one and a half times the utterance length (8.5 minutes for a five-minute
// take, inside the page's 10-minute call limit), unless -dictation-timeout is
// set. It is generous on purpose: a small server's CPU can take longer than
// the speech itself, and a command loads the model for every take. A timeout
// is reported, not retried with the command. On timeout a command's whole
// process group is killed.
package dictation

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/devark28/xhaos/app/apperr"
	"github.com/devark28/xhaos/app/localhttp"
	"github.com/devark28/xhaos/app/sandbox"
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

const (
	timeoutBase  = 60 * time.Second // the sidecar's limit for any utterance (loading the model)
	timeoutShare = 1.5              // plus this multiple of the utterance's length
	waitDelay    = 5 * time.Second  // after a kill, wait this long for pipes to close
	maxOutput    = 1 << 20          // stdout kept, in bytes
	maxErrText   = 500              // stderr quoted in errors, in bytes
	maxHints     = 1000             // dictation hints, in characters
	startChars   = 40               // the transcript's start written to the recordings log
	logFile      = "log.txt"        // the recordings log, in Config.Recordings
	languageKey  = "language"       // the server-wide dictation language, in dictation_settings
	defaultLang  = "en"             // the dictation language until the setting is changed
	autoLang     = "auto"           // the setting that asks the sidecar to detect the language
)

// ErrNotConfigured means no sidecar is set (-dictation-cmd is empty).
var ErrNotConfigured = errors.New("dictation is not configured: start the server with -dictation-cmd")

// Config says how to run the sidecar. See the package comment.
type Config struct {
	URL        string        // warm service base URL (-dictation-url); "" for none
	Program    string        // sidecar executable; "" turns dictation off unless URL is set
	Args       string        // argument template: fields split on spaces; {wav}, {model}, {language}, {prompt}
	Model      string        // model directory, for {model}
	Timeout    time.Duration // fixed sidecar time limit; 0 scales with the audio
	Recordings string        // folder for kept recordings (beside the database); "" disables keeping
}

// Service is the client-facing API.
type Service struct {
	db   *sql.DB
	cfg  Config
	http *http.Client // to the warm service; each request is bounded by its context
}

// NewService returns the dictation service. It needs the database for the
// workspace checks and the settings.
func NewService(db *sql.DB, cfg Config) *Service {
	// The warm service is reached on a Unix socket (unix:/path) or a URL.
	base, client := localhttp.New(cfg.URL)
	cfg.URL = base
	return &Service{db: db, cfg: cfg, http: client}
}

// Migrate creates the settings table. Settings are scoped: scope "" holds the
// server's (keep recordings), a workspace ID holds that workspace's (hints).
func Migrate(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS dictation_settings (
		scope TEXT NOT NULL,
		key   TEXT NOT NULL,
		value TEXT NOT NULL,
		PRIMARY KEY (scope, key)
	)`)
	return err
}

// Enabled reports whether a sidecar or a warm service is configured. The page
// calls it to show or hide the microphone.
func (s *Service) Enabled() bool { return s.cfg.Program != "" || s.cfg.URL != "" }

// Transcribe returns the transcript of one utterance: 16 kHz mono signed
// 16-bit little-endian PCM (pcm16le, base64 on the wire). It returns
// ErrNotConfigured when neither a warm service nor a sidecar is set.
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
	hints, err := s.Hints(workspaceID)
	if err != nil {
		return "", err
	}
	language, err := s.Language()
	if err != nil {
		return "", err
	}
	keep, err := s.Keeping()
	if err != nil {
		return "", err
	}
	timeout := s.timeout(len(pcm16le))
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	started := time.Now()
	out, warm, err := s.transcribe(ctx, pcm16le, language, hints)
	took := time.Since(started)
	if ctx.Err() == context.DeadlineExceeded {
		err = fmt.Errorf("dictation timed out after %s", timeout)
	}
	text := strings.TrimSpace(out)
	// Each finished or timed-out take tells the server its speed (other
	// failures are quick and say nothing about it).
	if cut := ctx.Err() == context.DeadlineExceeded; err == nil || cut {
		length := float64(len(pcm16le)) / float64(SampleRate*bytesPerSample)
		if serr := s.recordSpeed(length, took.Seconds(), cut, warm); serr != nil {
			log.Printf("WARN dictation: could not record the speed: %v", serr)
		}
	}
	if keep {
		// A failed save only goes to the server log: the transcript still returns.
		if kerr := s.keepRecording(pcm16le, text, err, started, took, warm); kerr != nil {
			log.Printf("WARN dictation: could not keep the recording: %v", kerr)
		}
	}
	if err != nil {
		return "", err
	}
	return text, nil
}

// transcribe takes one utterance through the warm service when one is set,
// else through the command. The command also takes a take the service failed
// on (down, or an error status), when -dictation-cmd is set; a timeout is not
// retried. warm reports whether the service handled the take.
func (s *Service) transcribe(ctx context.Context, pcm []byte, language, prompt string) (text string, warm bool, err error) {
	if s.cfg.URL != "" {
		text, err = s.serve(ctx, pcm, language, prompt)
		if err == nil || ctx.Err() != nil || s.cfg.Program == "" {
			return text, true, err
		}
		log.Printf("WARN dictation: the warm service failed, running the command instead: %v", err)
	}
	text, err = s.command(ctx, pcm, language, prompt)
	return text, false, err
}

// command runs the sidecar on the utterance as a WAV file in a private temp
// directory, which is removed before it returns.
func (s *Service) command(ctx context.Context, pcm []byte, language, prompt string) (string, error) {
	// The temp directory is private (0700) and holds only this utterance.
	dir, err := os.MkdirTemp("", "xhaos-dictation-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir) // the audio goes as soon as the transcript is read
	wav := filepath.Join(dir, "utterance.wav")
	if err := writeWAV(wav, pcm); err != nil {
		return "", err
	}
	return s.run(ctx, dir, wav, language, prompt)
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

// timeout is the sidecar's limit: the configured one, else timeoutBase plus
// timeoutShare times the utterance's length.
func (s *Service) timeout(n int) time.Duration {
	if s.cfg.Timeout > 0 {
		return s.cfg.Timeout
	}
	seconds := float64(n) / float64(SampleRate*bytesPerSample)
	return timeoutBase + time.Duration(timeoutShare*seconds*float64(time.Second))
}

// argv fills the template. Each field is split on spaces first, so a model
// directory with spaces stays one argument. {language} is filled before
// {prompt}, which is filled last inside its field, so the hints keep their
// spaces and any "{...}" text the user typed in them stays as typed.
func (s *Service) argv(wav, language, prompt string) []string {
	fields := strings.Fields(s.cfg.Args)
	if len(fields) == 0 {
		fields = []string{"{wav}"}
	}
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f == "{prompt}" && prompt == "" {
			continue
		}
		f = strings.ReplaceAll(f, "{wav}", wav)
		f = strings.ReplaceAll(f, "{model}", s.cfg.Model)
		f = strings.ReplaceAll(f, "{language}", language)
		f = strings.ReplaceAll(f, "{prompt}", prompt)
		out = append(out, f)
	}
	return out
}

// run starts the sidecar in dir and returns its stdout. The process group is
// killed when ctx ends.
func (s *Service) run(ctx context.Context, dir, wav, language, prompt string) (string, error) {
	cmd := exec.CommandContext(ctx, s.cfg.Program, s.argv(wav, language, prompt)...)
	// The server runs it unsandboxed: never from a folder commands can write.
	if err := sandbox.CheckProgram(cmd.Path); err != nil {
		return "", fmt.Errorf("dictation: %w", err)
	}
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

// Keeping reports whether the server keeps each recording (the Developer
// setting). It is off by default.
func (s *Service) Keeping() (bool, error) {
	v, err := s.setting("", "keep")
	return v == "on", err
}

// SetKeeping turns keeping recordings on or off.
func (s *Service) SetKeeping(on bool) error {
	v := "off"
	if on {
		v = "on"
	}
	return s.putSetting("", "keep", v)
}

// DeleteRecordings removes every kept recording and the log.
func (s *Service) DeleteRecordings() error {
	if s.cfg.Recordings == "" {
		return nil
	}
	return os.RemoveAll(s.cfg.Recordings)
}

// keepRecording writes the utterance's WAV into the recordings folder and
// appends one line to its log: the time, the file, the length, the loudness,
// the time taken, the outcome, the transcript's start, and whether the warm
// service or the command transcribed it.
func (s *Service) keepRecording(pcm []byte, text string, outcome error, at time.Time, took time.Duration, warm bool) error {
	if s.cfg.Recordings == "" {
		return nil
	}
	if err := os.MkdirAll(s.cfg.Recordings, 0o700); err != nil {
		return err
	}
	name := at.Format("20060102-150405.000000") + ".wav"
	if err := writeWAV(filepath.Join(s.cfg.Recordings, name), pcm); err != nil {
		return err
	}
	rms, peak := loudness(pcm)
	result := "ok"
	if outcome != nil {
		result = outcome.Error()
	}
	start := text
	if utf8.RuneCountInString(start) > startChars {
		start = string([]rune(start)[:startChars]) + "…"
	}
	seconds := float64(len(pcm)) / float64(SampleRate*bytesPerSample)
	via := "command"
	if warm {
		via = "service"
	}
	line := fmt.Sprintf("%s  %s  length %.1f s  rms %.4f  peak %.4f  transcribed in %.1f s  %s  %s  %q\n",
		at.Format(time.RFC3339), name, seconds, rms, peak, took.Seconds(), via, result, start)
	f, err := os.OpenFile(filepath.Join(s.cfg.Recordings, logFile), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.WriteString(line)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// loudness returns the RMS and the peak of 16-bit little-endian PCM, each on
// a 0 to 1 scale.
func loudness(pcm []byte) (rms, peak float64) {
	n := len(pcm) / bytesPerSample
	if n == 0 {
		return 0, 0
	}
	var sum float64
	for i := 0; i < n; i++ {
		v := float64(int16(binary.LittleEndian.Uint16(pcm[i*bytesPerSample:]))) / 32768
		sum += v * v
		peak = math.Max(peak, math.Abs(v))
	}
	return math.Sqrt(sum / float64(n)), peak
}

// Hints returns the workspace's dictation hints: a punctuated sentence with
// the words and names to expect, passed to the sidecar as {prompt}.
func (s *Service) Hints(workspaceID string) (string, error) {
	if _, err := workspace.Get(s.db, workspaceID); err != nil {
		return "", err
	}
	return s.setting(workspaceID, "hints")
}

// SetHints stores the workspace's dictation hints, on one line. The limit is
// in characters.
func (s *Service) SetHints(workspaceID, text string) error {
	if _, err := workspace.Get(s.db, workspaceID); err != nil {
		return err
	}
	text = strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(text) > maxHints {
		return fmt.Errorf("%w: hints are longer than %d characters", apperr.Invalid, maxHints)
	}
	return s.putSetting(workspaceID, "hints", text)
}

// Language returns the dictation language for the sidecar's {language}: a
// language code such as "en", or "auto" to detect it. The setting is server-wide
// and defaults to English.
func (s *Service) Language() (string, error) {
	v, err := s.setting("", languageKey)
	if err != nil {
		return "", err
	}
	if v == "" {
		return defaultLang, nil
	}
	return v, nil
}

// SetLanguage stores the dictation language: "auto", or a two- or three-letter
// lowercase language code.
func (s *Service) SetLanguage(code string) error {
	if !validLanguage(code) {
		return fmt.Errorf("%w: language must be a code such as en, or auto", apperr.Invalid)
	}
	return s.putSetting("", languageKey, code)
}

// validLanguage accepts "auto" and two- or three-letter lowercase codes.
func validLanguage(code string) bool {
	if code == autoLang {
		return true
	}
	if len(code) < 2 || len(code) > 3 {
		return false
	}
	for _, r := range code {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	return true
}

func (s *Service) setting(scope, key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM dictation_settings WHERE scope = ? AND key = ?`, scope, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (s *Service) putSetting(scope, key, value string) error {
	_, err := s.db.Exec(`INSERT INTO dictation_settings (scope, key, value) VALUES (?, ?, ?)
		ON CONFLICT (scope, key) DO UPDATE SET value = excluded.value`, scope, key, value)
	return err
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
	_, werr := f.Write(wavHeader(len(pcm)))
	if werr == nil {
		_, werr = f.Write(pcm)
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// wavBytes is the whole WAV file for pcm, in memory (the warm service's body).
func wavBytes(pcm []byte) []byte {
	return append(wavHeader(len(pcm)), pcm...)
}

// wavHeader is the 44-byte header of a WAV file holding n bytes of PCM.
func wavHeader(n int) []byte {
	hdr := make([]byte, 44)
	copy(hdr[0:], "RIFF")
	binary.LittleEndian.PutUint32(hdr[4:], uint32(36+n))
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
	binary.LittleEndian.PutUint32(hdr[40:], uint32(n))
	return hdr
}
