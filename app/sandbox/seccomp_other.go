//go:build !linux

package sandbox

import (
	"fmt"
	"runtime"
)

// denySockets has no equivalent outside Linux (and Landlock is Linux-only,
// so the sandbox is never active there).
func denySockets(bool) error { return fmt.Errorf("no seccomp on %s", runtime.GOOS) }
