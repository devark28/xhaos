package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

// Programs the server runs itself (an MCP server's command, the dictation
// sidecar, git) are refused from a command-writable folder, by path, by a
// link into it, or by a bare name found there on PATH.
func TestCheckProgramRefusesCommandWritable(t *testing.T) {
	dir := t.TempDir()
	prog := filepath.Join(dir, "bin", "xhaos-mcp-test")
	os.MkdirAll(filepath.Dir(prog), 0o755)
	os.WriteFile(prog, []byte("#!/bin/sh\n"), 0o755)
	link := filepath.Join(t.TempDir(), "xhaos-mcp-link")
	os.Symlink(prog, link)
	if err := CheckProgram(prog); err != nil {
		t.Fatalf("refused before the folder was known as writable: %v", err)
	}
	t.Setenv("PATH", "/usr/bin:/bin")
	GuardPath(dir)
	for _, p := range []string{prog, link} {
		if err := CheckProgram(p); err == nil {
			t.Errorf("%s was allowed", p)
		}
	}
	os.Setenv("PATH", "/usr/bin:/bin:"+filepath.Dir(prog)) // as if added after the start's check
	if err := CheckProgram("xhaos-mcp-test"); err == nil {
		t.Error("a bare name found in a command-writable folder was allowed")
	}
	if err := CheckProgram("sh"); err != nil {
		t.Errorf("sh refused: %v", err)
	}
}
