// Package wire is the protocol between an Xhaos server and xhaos-node: the
// messages both sides send over the node's WebSocket, and the helpers both
// need (signing the challenge, confining paths to the node root).
//
// Control messages are JSON text frames, one Msg per frame. File data goes
// in binary frames: 8 bytes of request ID (big-endian), then the data.
package wire

import (
	"crypto/ed25519"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Path is where nodes connect, on the server's public address.
const Path = "/xhaos/node"

// Protocol is bumped when a change breaks older nodes; the server refuses
// a node whose protocol it does not speak.
const Protocol = 1

// Limits.
const (
	MaxFrame    = 1 << 20   // largest frame either side reads
	Chunk       = 256 << 10 // file data per binary frame
	MaxCopy     = 1 << 30   // bytes in one copy (a file, or a folder's tar)
	MaxJobLog   = 10 << 20  // a job's output kept on the node (the newest bytes)
	MaxWait     = 120_000   // ms a request may wait for a job
	DefaultWait = 120_000
)

// Message types.
const (
	// Joining (one connection, closed afterwards).
	TJoin   = "join"   // node → server: Code, PublicKey, Facts
	TJoined = "joined" // server → node: Node, Workspace, Name

	// Every later connection.
	TChallenge = "challenge" // server → node: Nonce
	THello     = "hello"     // node → server: Node, Sig, Facts, Jobs
	TWelcome   = "welcome"   // server → node: Name

	// Requests from the server (ID set); the node answers with the same ID.
	TExec   = "exec"   // Command, Dir, Wait → TJobInfo
	TJob    = "job"    // Job, Action (logs, wait, kill), Offset, Wait → TJobInfo
	TPut    = "put"    // Path, Mode, Tar; then binary frames; then TEOF → TDone
	TGet    = "get"    // Path → TMeta, binary frames, TEOF → (server checks)
	TCancel = "cancel" // stop request ID

	// Answers.
	TJobInfo = "jobinfo" // Job (state), Data (output from Offset), Offset (next)
	TMeta    = "meta"    // Mode, Tar, Size (of a file; -1 for a tar stream)
	TEOF     = "eof"     // SHA256 of the bytes sent
	TDone    = "done"    // Size, SHA256 of what was written
	TError   = "error"   // Error

	// Node → server, unasked.
	TJobs = "jobs" // Jobs: the node's jobs changed

	TPing = "ping"
	TPong = "pong"
)

// Job actions.
const (
	ActLogs = "logs"
	ActWait = "wait"
	ActKill = "kill"
)

// Msg is every message; each type uses some fields.
type Msg struct {
	Type string `json:"type"`
	ID   uint64 `json:"id,omitempty"`

	Code      string `json:"code,omitempty"`
	PublicKey []byte `json:"publicKey,omitempty"`
	Nonce     []byte `json:"nonce,omitempty"`
	Sig       []byte `json:"sig,omitempty"`
	Node      int64  `json:"node,omitempty"`
	Workspace string `json:"workspace,omitempty"`
	Name      string `json:"name,omitempty"`
	Facts     *Facts `json:"facts,omitempty"`
	Jobs      []Job  `json:"jobs,omitempty"`

	Command string `json:"command,omitempty"`
	Dir     string `json:"dir,omitempty"`
	Wait    int    `json:"wait,omitempty"` // ms
	Job     *Job   `json:"job,omitempty"`
	JobID   string `json:"jobId,omitempty"`
	Action  string `json:"action,omitempty"`
	Offset  int64  `json:"offset,omitempty"`
	Data    string `json:"data,omitempty"`

	Path   string `json:"path,omitempty"`
	Mode   uint32 `json:"mode,omitempty"`
	Tar    bool   `json:"tar,omitempty"`
	Size   int64  `json:"size,omitempty"`
	SHA256 string `json:"sha256,omitempty"`

	Error string `json:"error,omitempty"`
}

// Facts are what a node reports about itself.
type Facts struct {
	Version   string   `json:"version"`
	Protocol  int      `json:"protocol"`
	Hostname  string   `json:"hostname"`
	OS        string   `json:"os"`
	Arch      string   `json:"arch"`
	Kernel    string   `json:"kernel,omitempty"`
	Distro    string   `json:"distro,omitempty"`
	Model     string   `json:"model,omitempty"` // e.g. "Raspberry Pi 5 Model B Rev 1.0"
	CPUs      int      `json:"cpus"`
	MemoryMB  int64    `json:"memoryMB,omitempty"`
	Root      string   `json:"root"`
	Landlock  int      `json:"landlock"`              // ABI, 0 when missing
	Sandboxed bool     `json:"sandboxed"`             // commands run under Landlock
	Devices   []string `json:"devices,omitempty"`     // opened to commands (--device)
	Hardware  []string `json:"hardware,omitempty"`    // seen in /dev: video*, ttyUSB*, ttyACM*, dri
	Unsandbox bool     `json:"unsandboxed,omitempty"` // the owner ran it with --unsandboxed
}

// Job is a command running (or finished) on a node.
type Job struct {
	ID      string `json:"id"`
	Command string `json:"command"`
	Dir     string `json:"dir,omitempty"`
	Started int64  `json:"started"` // unix ms
	Ended   int64  `json:"ended,omitempty"`
	Running bool   `json:"running"`
	Exit    int    `json:"exit"`
	Signal  string `json:"signal,omitempty"`
	Size    int64  `json:"size"`    // output bytes so far (offsets run up to this)
	Dropped int64  `json:"dropped"` // the oldest bytes no longer kept
}

// Binary frames carry an 8-byte request ID, then data.

// Frame prefixes data with id.
func Frame(id uint64, data []byte) []byte {
	b := make([]byte, 8+len(data))
	binary.BigEndian.PutUint64(b, id)
	copy(b[8:], data)
	return b
}

// Unframe splits a binary frame.
func Unframe(b []byte) (uint64, []byte, error) {
	if len(b) < 8 {
		return 0, nil, errors.New("short binary frame")
	}
	return binary.BigEndian.Uint64(b), b[8:], nil
}

// Challenge text a node signs: the nonce, bound to this protocol, so a
// signature is good for one connection only.
func challengeText(nonce []byte) []byte {
	return append([]byte("xhaos-node-hello-v1\x00"), nonce...)
}

// Sign answers a challenge.
func Sign(key ed25519.PrivateKey, nonce []byte) []byte {
	return ed25519.Sign(key, challengeText(nonce))
}

// Verify checks an answer.
func Verify(pub ed25519.PublicKey, nonce, sig []byte) bool {
	return len(pub) == ed25519.PublicKeySize && len(nonce) >= 16 && ed25519.Verify(pub, challengeText(nonce), sig)
}

// Confine maps a path given by the server to one inside root. Relative
// paths are relative to root; "~" and "~/x" mean root too (the node root is
// the agent's home there); absolute paths must already be inside root.
// Symlinks are resolved in the part that exists, so a link cannot lead out.
func Confine(root, p string) (string, error) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("node root: %w", err)
	}
	switch {
	case p == "" || p == "~":
		p = "."
	case strings.HasPrefix(p, "~/"):
		p = p[2:]
	}
	path := p
	if !filepath.IsAbs(path) {
		path = filepath.Join(realRoot, p)
	}
	path = filepath.Clean(path)
	existing, rest := path, ""
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			break
		}
		rest = filepath.Join(filepath.Base(existing), rest)
		existing = parent
	}
	if real, err := filepath.EvalSymlinks(existing); err == nil {
		path = filepath.Join(real, rest)
	}
	if path != realRoot && !strings.HasPrefix(path, realRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is outside the node's folder (%s)", p, root)
	}
	return path, nil
}
