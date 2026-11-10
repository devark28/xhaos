package classifier

import (
	"os"
	"path/filepath"
	"strings"
)

// npx, pnpx and bunx run a package that is not installed: they download it.
// Xhaos allows one case. The tool is one of the JavaScript tools of the list
// (js) and it is already in the project's node_modules/.bin. The command is
// then judged as that tool's own direct form, with the same flags and the
// same list. Every other use of a runner asks.
var runnerNames = []string{"npx", "pnpx", "bunx"}

// isRunner reports whether w is exactly a runner's name (as the list reads it).
func isRunner(w string) bool {
	for _, n := range runnerNames {
		if w == n {
			return true
		}
	}
	return false
}

// anyRunner reports whether a listed name (such as "npx eslint") starts with a
// runner's name.
func anyRunner(names []string) bool {
	for _, n := range names {
		if i := strings.IndexByte(n, ' '); i > 0 && isRunner(n[:i]) {
			return true
		}
	}
	return false
}

// hasRunner reports whether any word of command is a runner, by program name
// (as the floor reads it: a path ending in npx counts).
func hasRunner(command string) bool {
	return anyWord(parse(command), is(runnerNames...))
}

// installedRunners reports whether every runner word of c is the first word
// of its simple command, followed by a JavaScript tool the project has
// installed (see installedJS).
func installedRunners(c parsed) bool {
	for _, seg := range c.segments {
		for i, w := range seg {
			if !is(runnerNames...)(w) {
				continue
			}
			// a path such as ./node_modules/.bin/npx is not the runner: refused
			if !isRunner(w) || i != 0 || len(seg) < 2 || !installedJS(c.root, seg[1]) {
				return false
			}
		}
	}
	return true
}

// installedJS reports whether w is one of the listed JavaScript tools and the
// project folder root has it in node_modules/.bin.
func installedJS(root, w string) bool {
	if _, ok := js[w]; !ok {
		return false
	}
	return localTool(root, w)
}

// localTool reports whether root/node_modules/.bin/w is an executable file. w
// must be a plain name: no path, no version (@), no option. Nothing is
// downloaded or run; a symbolic link (what npm makes) is followed.
func localTool(root, w string) bool {
	if root == "" || w == "" || w == "." || w == ".." || strings.HasPrefix(w, "-") || strings.ContainsAny(w, `/\@:`) {
		return false
	}
	fi, err := os.Stat(filepath.Join(root, "node_modules", ".bin", w))
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}
