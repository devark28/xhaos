package client

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os/exec"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/devark28/xhaos/app/node/wire"
)

// maxReply is the most output one answer carries (the newest bytes); the
// server shows the model less, and older output can be asked for by offset.
const maxReply = 64 << 10

// keepDone is how many finished jobs are kept.
const keepDone = 50

type job struct {
	mu      sync.Mutex
	info    wire.Job
	data    []byte // the newest output, at most wire.MaxJobLog bytes
	changed chan struct{}
	done    chan struct{}
	cmd     *exec.Cmd
}

// Write appends output, dropping the oldest bytes past the cap.
func (j *job) Write(p []byte) (int, error) {
	j.mu.Lock()
	j.data = append(j.data, p...)
	if over := len(j.data) - wire.MaxJobLog; over > 0 {
		j.data = append(j.data[:0:0], j.data[over:]...)
		j.info.Dropped += int64(over)
	}
	j.info.Size += int64(len(p))
	close(j.changed)
	j.changed = make(chan struct{})
	j.mu.Unlock()
	return len(p), nil
}

// read returns the output from offset (at most maxReply bytes, the newest
// when there is more), the offset it starts at, and the job's state.
func (j *job) read(offset int64) (string, int64, wire.Job) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if offset < j.info.Dropped {
		offset = j.info.Dropped
	}
	if offset > j.info.Size {
		offset = j.info.Size
	}
	if j.info.Size-offset > maxReply {
		offset = j.info.Size - maxReply
	}
	return string(j.data[offset-j.info.Dropped:]), offset, j.info
}

type jobs struct {
	mu     sync.Mutex
	all    map[string]*job
	notify func() // the job list changed (started, ended)
}

func newJobs(notify func()) *jobs {
	if notify == nil {
		notify = func() {}
	}
	return &jobs{all: map[string]*job{}, notify: notify}
}

func newID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// start runs cmd as a job. The process gets its own group, so kill reaches
// what it starts.
func (s *jobs) start(cmd *exec.Cmd, command, dir string) (*job, error) {
	j := &job{changed: make(chan struct{}), done: make(chan struct{}), cmd: cmd,
		info: wire.Job{ID: newID(), Command: command, Dir: dir, Started: time.Now().UnixMilli(), Running: true}}
	cmd.Stdout, cmd.Stderr = j, j
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.all[j.info.ID] = j
	s.trim()
	s.mu.Unlock()
	s.notify()
	go func() {
		err := cmd.Wait()
		j.mu.Lock()
		j.info.Running, j.info.Ended = false, time.Now().UnixMilli()
		j.info.Exit = cmd.ProcessState.ExitCode()
		if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			j.info.Signal = ws.Signal().String()
		}
		if err != nil && cmd.ProcessState == nil {
			j.info.Exit = -1
		}
		close(j.changed)
		j.changed = make(chan struct{})
		j.mu.Unlock()
		close(j.done)
		s.notify()
	}()
	return j, nil
}

// trim forgets the oldest finished jobs beyond keepDone (mu held).
func (s *jobs) trim() {
	var done []*job
	for _, j := range s.all {
		j.mu.Lock()
		if !j.info.Running {
			done = append(done, j)
		}
		j.mu.Unlock()
	}
	if len(done) <= keepDone {
		return
	}
	sort.Slice(done, func(a, b int) bool { return done[a].info.Started < done[b].info.Started })
	for _, j := range done[:len(done)-keepDone] {
		delete(s.all, j.info.ID)
	}
}

func (s *jobs) get(id string) (*job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.all[id]
	if !ok {
		return nil, errors.New("no job " + id + " on this node (finished jobs are kept for a while, not across a node restart)")
	}
	return j, nil
}

// list is every job, newest first.
func (s *jobs) list() []wire.Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]wire.Job, 0, len(s.all))
	for _, j := range s.all {
		j.mu.Lock()
		out = append(out, j.info)
		j.mu.Unlock()
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Started > out[b].Started })
	return out
}

// wait blocks until the job ends or d passes (or ctx ends).
func (j *job) wait(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-j.done:
	case <-t.C:
	case <-ctx.Done():
	}
}

// kill stops the job's process group: SIGTERM, then SIGKILL after 5 s.
func (j *job) kill() {
	j.mu.Lock()
	running, p := j.info.Running, j.cmd.Process
	j.mu.Unlock()
	if !running || p == nil {
		return
	}
	_ = syscall.Kill(-p.Pid, syscall.SIGTERM)
	select {
	case <-j.done:
	case <-time.After(5 * time.Second):
		_ = syscall.Kill(-p.Pid, syscall.SIGKILL)
		<-j.done
	}
}

// killAll stops every running job (the node is shutting down).
func (s *jobs) killAll() {
	s.mu.Lock()
	list := make([]*job, 0, len(s.all))
	for _, j := range s.all {
		list = append(list, j)
	}
	s.mu.Unlock()
	var wg sync.WaitGroup
	for _, j := range list {
		wg.Add(1)
		go func() { defer wg.Done(); j.kill() }()
	}
	wg.Wait()
}
