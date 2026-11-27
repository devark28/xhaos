package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// What a sealed command holds (its secrets, the SSH key Setup writes in its
// scratch folder) must not be reachable by the project's ordinary commands,
// which the model reads: not the scratch folder, and not /dev/shm.
func TestSealedLeavesNothingOtherCommandsRead(t *testing.T) {
	if ABI() < NetABI {
		t.Skip("sealed commands need Landlock ABI 4+")
	}
	dir := t.TempDir()
	root := filepath.Join(dir, "project")
	os.MkdirAll(root, 0o755)
	c := Config{Mode: On, Cache: filepath.Join(dir, "cache")}
	var key string
	shm := fmt.Sprintf("/dev/shm/xhaos-sealed-test-%d", time.Now().UnixNano())
	defer os.Remove(shm)
	cmd, cleanup, err := c.CommandSealed(context.Background(), root, "echo SECRET > "+shm+"; echo done", Sealed{
		ProxyPort: 1,
		Setup: func(scratch string) ([]string, error) {
			key = filepath.Join(scratch, "home", "id_key")
			return nil, os.WriteFile(key, []byte("PRIVATE KEY"), 0o600)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	// While the sealed command is set up (its key in place), an ordinary
	// command of the same project tries to read it.
	out, err := run(t, c, root, "cat "+key)
	if err == nil || strings.Contains(out, "PRIVATE KEY") {
		t.Errorf("an ordinary command read the sealed command's key at %s: %q", key, out)
	}
	tmp, _ := TempDir(root)
	if strings.HasPrefix(key, tmp+"/") {
		t.Errorf("the scratch folder %s is inside the project's TMPDIR %s", key, tmp)
	}
	sealedOut, _ := cmd.CombinedOutput()
	if b, err := os.ReadFile(shm); err == nil {
		t.Errorf("the sealed command wrote to /dev/shm (%q), which every command can read; its output: %s", b, sealedOut)
	}
	if !strings.Contains(string(sealedOut), "done") {
		t.Errorf("the sealed command did not run: %s", sealedOut)
	}
}
