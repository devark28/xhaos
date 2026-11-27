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

// Project A's command plants usercustomize.py in its HOME (Python runs it
// at every start). Python in project B must not run it, and A cannot write
// into B's cache.
func TestHomeIsPerProject(t *testing.T) {
	if ABI() == 0 {
		t.Skip("no Landlock in this kernel")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("no python3")
	}
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	os.MkdirAll(a, 0o755)
	os.MkdirAll(b, 0o755)
	c := Config{Mode: On, Cache: filepath.Join(dir, "cache")}
	plant := `d=$(python3 -c 'import site; print(site.getusersitepackages())') && mkdir -p "$d" && echo 'print("PLANTED-RAN")' > "$d/usercustomize.py" && python3 -c 'print("in A")'`
	out, err := run(t, c, a, plant)
	if err != nil || !strings.Contains(out, "PLANTED-RAN") {
		t.Fatalf("the plant did not take in A itself (test setup): %v %s", err, out)
	}
	out, err = run(t, c, b, `python3 -c 'print("in B")'`)
	if err != nil || !strings.Contains(out, "in B") {
		t.Fatalf("python3 in B: %v %s", err, out)
	}
	if strings.Contains(out, "PLANTED-RAN") {
		t.Error("project A's usercustomize.py ran in project B's python3")
	}
	// Nor can A reach B's HOME by its path.
	out, err = run(t, c, b, `echo $HOME`)
	if err != nil {
		t.Fatal(err)
	}
	bHome := strings.TrimSpace(out)
	out, err = run(t, c, a, "echo x > "+filepath.Join(bHome, ".npmrc"))
	if err == nil && bHome != "" {
		if _, err := os.Stat(filepath.Join(bHome, ".npmrc")); err == nil {
			t.Errorf("A wrote into B's HOME (%s): %s", bHome, out)
		}
	}
	// HOME, the Go, npm and XDG caches are B's own, not A's.
	envA, _, err := c.prepare(a)
	if err != nil {
		t.Fatal(err)
	}
	envB, _, err := c.prepare(b)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"HOME=", "GOCACHE=", "GOMODCACHE=", "npm_config_cache=", "XDG_CACHE_HOME="} {
		if va, vb := envWith(envA, k), envWith(envB, k); len(va) != 1 || len(vb) != 1 || va[0] == vb[0] {
			t.Errorf("%s shared between projects: A %v, B %v", k, va, vb)
		}
	}
}
