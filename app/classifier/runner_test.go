package classifier

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/devark28/xhaos/app/policy"
)

// installTools makes root/node_modules/.bin with the named executables, the
// way npm does it (a symbolic link to the package's file).
func installTools(t *testing.T, root string, names ...string) {
	t.Helper()
	bin := filepath.Join(root, "node_modules", ".bin")
	pkg := filepath.Join(root, "node_modules", "pkg", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		target := filepath.Join(pkg, n)
		if err := os.WriteFile(target, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(bin, n)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRunnerFloorNeedsAnInstalledListedTool(t *testing.T) {
	root := t.TempDir()
	installTools(t, root, "tsc", "eslint", "prettier", "vitest", "some-tool")
	cases := []struct {
		command string
		refused bool
	}{
		{"npx tsc --noEmit", false},
		{"npx eslint frontend/src --max-warnings 0", false},
		{"npx prettier --check .", false},
		{"npx vitest run", false},
		{"pnpx tsc --noEmit", false},
		{"bunx vitest run", false},
		{"npx some-tool", true}, // installed, but not a listed JavaScript tool
		{"npx unknown", true},   // not installed
		{"npx --yes tsc --noEmit", true},
		{"npx ../tsc", true},
		{"npx ./node_modules/.bin/tsc", true},
		{"npx tsc@5 --noEmit", true},
		{"npx tsc --noEmit; npx unknown", true},
		{"npx tsc --noEmit && rm -rf x", true}, // the other floor rows still apply
		{"echo npx tsc", true},                 // not the first word of its command
		{"./node_modules/.bin/npx tsc", true},  // not a bare runner name: refused
	}
	for _, c := range cases {
		got := FloorIn(root, c.command) != ""
		if got != c.refused {
			t.Errorf("FloorIn(%q) refused = %v, want %v (%s)", c.command, got, c.refused, FloorIn(root, c.command))
		}
	}
	if FloorIn("", "npx tsc --noEmit") == "" {
		t.Error("with no project folder, npx must be refused")
	}
	if Floor("npx tsc --noEmit") == "" {
		t.Error("Floor (no folder) must refuse npx")
	}
}

func TestRunnerNeedsTheToolInTheProject(t *testing.T) {
	root := t.TempDir() // no node_modules at all
	if FloorIn(root, "npx tsc --noEmit") == "" {
		t.Error("npx with no node_modules must be refused")
	}
	installTools(t, root, "tsc")
	if err := os.Chmod(filepath.Join(root, "node_modules", "pkg", "bin", "tsc"), 0o644); err != nil {
		t.Fatal(err)
	}
	if FloorIn(root, "npx tsc --noEmit") == "" {
		t.Error("a tool that is not executable must be refused")
	}
}

func TestRunnerListedWithTheDirectFlagRules(t *testing.T) {
	root := t.TempDir()
	installTools(t, root, "tsc", "eslint", "prettier", "vitest")
	allowed := map[string]string{
		"npx tsc --noEmit":                         "npx tsc --noEmit",
		"npx eslint frontend/src --max-warnings 0": "npx eslint",
		"npx prettier --check .":                   "npx prettier --check",
		"npx vitest run":                           "npx vitest run",
		"npx tsc --noEmit 2>&1":                    "npx tsc --noEmit",
		"npx tsc -p other.json --noEmit":           "npx tsc --noEmit", // the direct form takes -p too
	}
	for cmd, name := range allowed {
		got, ok := ListedIn(root, cmd)
		if !ok || got != name {
			t.Errorf("ListedIn(%q) = %q, %v; want %q", cmd, got, ok, name)
		}
	}
	refused := []string{
		"npx tsc",                  // the direct form needs --noEmit too
		"npx tsc --noEmit -o x.js", // -o writes
		"npx eslint --fix .",       // the write flag of the direct form
		"npx prettier --write .",
		"npx vitest", // the direct form needs run
		"npx vitest run --watch",
		"npx --yes tsc --noEmit", // flags before the tool
		"npx unknown",
		"npx tsc --noEmit | tee out", // not a filter
		"npx ./node_modules/.bin/tsc --noEmit",
		"./node_modules/.bin/npx tsc --noEmit",
		"npx tsc --noEmit $(id)",
	}
	for _, cmd := range refused {
		if name, ok := ListedIn(root, cmd); ok {
			t.Errorf("ListedIn(%q) listed as %q, want refused", cmd, name)
		}
	}
	if _, ok := ListedIn("", "npx tsc --noEmit"); ok {
		t.Error("with no project folder, npx must not be listed")
	}
}

func TestRunnerIsNeverAskedOfTheModel(t *testing.T) {
	root := t.TempDir()
	installTools(t, root, "tsc")
	p := policy.Policy{Access: "rw", Review: "auto", Commit: "review"}
	call := func(cmd string) Call {
		return Call{Name: "bash", Effect: policy.EffectExec, Command: cmd, Policy: p, Root: root}
	}
	if name, ok := Allowed(call("npx tsc --noEmit")); !ok || name != "npx tsc --noEmit" {
		t.Errorf("Allowed(npx tsc --noEmit) = %q, %v; want listed", name, ok)
	}
	if Eligible(call("npx tsc --noEmit")) {
		t.Error("a runner command must not be eligible for the model")
	}
	if Eligible(call("npx unknown")) {
		t.Error("an unlisted runner command must not be eligible for the model either")
	}
	if _, ok := Allowed(call("npx unknown")); ok {
		t.Error("an uninstalled tool must not be allowed")
	}
}
