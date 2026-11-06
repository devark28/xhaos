package run_test

import (
	"os"
	"testing"

	"xhaos/app/egress"
	"xhaos/app/sandbox"
)

// The test binary doubles as the sandbox helper, like the server binary.
func TestMain(m *testing.M) {
	sandbox.MaybeRun()
	egress.MaybeConnect()
	os.Exit(m.Run())
}
