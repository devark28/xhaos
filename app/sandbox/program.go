package sandbox

import (
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"sync"
)

// The folders sandboxed commands can write, as the server has learnt them
// (GuardPath records them: at start the data folder, cache, temp roots and
// projects; before each command, that command's write set).
var writable struct {
	sync.Mutex
	dirs []string
}

func noteWritable(dirs []string) {
	writable.Lock()
	defer writable.Unlock()
	for _, d := range dirs {
		if d == "" || d == "/dev" || strings.HasPrefix(d, "/dev/") {
			continue // devices: no programs there
		}
		if !slices.Contains(writable.dirs, d) {
			writable.dirs = append(writable.dirs, d)
		}
	}
}

// CheckProgram refuses a program the server is about to run itself, outside
// the sandbox, when it or any folder above it (as given, or with links
// resolved, see inAny) is inside a folder sandboxed commands can write: a
// command could have put it there, or swapped it, and it would run as the
// server. A bare name is looked up on PATH first. The error is for the
// owner: an explicit path they set is refused with the reason, not ignored.
func CheckProgram(program string) error {
	path := program
	if !strings.ContainsRune(program, '/') {
		p, err := exec.LookPath(program)
		if err != nil {
			return nil // nothing to run; the caller reports that
		}
		path = p
	}
	writable.Lock()
	dirs := slices.Clone(writable.dirs)
	writable.Unlock()
	for _, d := range dirs {
		if inAny(path, []string{d}) {
			return fmt.Errorf("refusing to run %s: it is inside %s (links resolved), which sandboxed commands can write, so a command could have planted it; install it in a folder they cannot write", path, d)
		}
	}
	return nil
}
