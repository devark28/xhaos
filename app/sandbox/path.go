package sandbox

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// toolBins are folders under Config.Cache with tools for commands only
// (Flutter writes into its own folder, so commands get a writable copy).
// They go on the commands' PATH, never on the server's.
var toolBins = []string{"flutter/bin"}

// TempRoot is the folder that holds every project's temp folder (TempDir).
func TempRoot() string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("xhaos-%d", os.Getuid()), "sandboxes")
}

// GuardPath removes from this process's own PATH every entry inside one of
// the folders in writable (folders sandboxed commands can write: the cache,
// projects, their temp folders). A program a command planted there would
// otherwise be run by the server, outside the sandbox, the next time it
// looks a name up on PATH (claude, git, an MCP server's command). It logs
// and returns what it removed.
func GuardPath(writable ...string) []string {
	var keep, dropped []string
	for _, e := range filepath.SplitList(os.Getenv("PATH")) {
		if e != "" && inAny(e, writable) {
			dropped = append(dropped, e)
			continue
		}
		keep = append(keep, e)
	}
	if len(dropped) > 0 {
		os.Setenv("PATH", strings.Join(keep, string(os.PathListSeparator)))
		for _, e := range dropped {
			log.Printf("sandbox: removed %s from the server's PATH: sandboxed commands can write there", e)
		}
	}
	return dropped
}

// inAny reports whether path is one of dirs or inside one, comparing both
// as given and with links resolved.
func inAny(path string, dirs []string) bool {
	paths := resolved(path)
	for _, d := range dirs {
		if d == "" {
			continue
		}
		for _, dd := range resolved(d) {
			for _, p := range paths {
				if p == dd || strings.HasPrefix(p, strings.TrimSuffix(dd, "/")+"/") {
					return true
				}
			}
		}
	}
	return false
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func resolved(p string) []string {
	if !filepath.IsAbs(p) {
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
	}
	p = filepath.Clean(p)
	out := []string{p}
	// The entry may not exist yet (a command could make it later): resolve
	// its nearest existing parent.
	rest := ""
	for q := p; ; q = filepath.Dir(q) {
		if r, err := filepath.EvalSymlinks(q); err == nil {
			if r = filepath.Join(r, rest); r != p {
				out = append(out, r)
			}
			break
		}
		if q == filepath.Dir(q) {
			break
		}
		rest = filepath.Join(filepath.Base(q), rest)
	}
	return out
}
