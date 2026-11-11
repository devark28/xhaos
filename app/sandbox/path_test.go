package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A folder in the commands' cache (the installer used to put the cache's
// Flutter there) is on the server's PATH. A command plants a program in it;
// the server must not find it on its own PATH afterwards, while commands
// still get the folder on theirs.
func TestServerPathNeverReachesCommandFolders(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "cache")
	bin := filepath.Join(cache, "flutter", "bin")
	os.MkdirAll(bin, 0o755)
	os.WriteFile(filepath.Join(bin, "xhaos-planted-tool"), []byte("#!/bin/sh\necho PWNED\n"), 0o755)
	root := filepath.Join(dir, "project")
	os.MkdirAll(filepath.Join(root, "bin"), 0o755)
	os.WriteFile(filepath.Join(root, "bin", "xhaos-planted-two"), []byte("#!/bin/sh\necho PWNED\n"), 0o755)
	t.Setenv("PATH", os.Getenv("PATH")+string(os.PathListSeparator)+bin+string(os.PathListSeparator)+filepath.Join(root, "bin"))

	c := Config{Mode: On, Cache: cache}
	env, _, err := c.prepare(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"xhaos-planted-tool", "xhaos-planted-two"} {
		if p, err := exec.LookPath(name); err == nil {
			t.Errorf("the server finds %s on its own PATH: %s", name, p)
		}
	}
	var path string
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			path = v
		}
	}
	if !strings.Contains(path, bin) {
		t.Errorf("commands lost the cache's tool folder from their PATH: %s", path)
	}
	if ABI() > 0 {
		cmd, err := c.Command(context.Background(), root, "xhaos-planted-tool")
		if err != nil {
			t.Fatal(err)
		}
		if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), "PWNED") {
			t.Errorf("a command cannot run a tool from the cache's folder: %v %s", err, out)
		}
	}
}

func TestGuardPathKeepsOtherEntries(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	os.Symlink(dir, link)
	t.Setenv("PATH", "/usr/bin:"+filepath.Join(link, "bin")+":/bin:"+dir+"x/bin")
	got := GuardPath(dir)
	if len(got) != 1 || got[0] != filepath.Join(link, "bin") {
		t.Errorf("dropped %v", got)
	}
	if p := os.Getenv("PATH"); p != "/usr/bin:/bin:"+dir+"x/bin" {
		t.Errorf("PATH = %s", p)
	}
}
