package tool

import (
	"bufio"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// git diff, log and show run a program named by the project's own git config:
// a diff.external program, a textconv or command driver, or an alias. Such a
// read is not a read, so the gate asks for it when the project's config could
// run one (see GitDriverRisk). Only the project's config is read; the owner's
// global and system config is trusted as the owner's own.

// gitDriverReads are the git subcommands that run diff drivers.
var gitDriverReads = map[string]bool{"diff": true, "log": true, "show": true}

// gitValueOpts are git's global options that take a separate value word.
var gitValueOpts = map[string]bool{"-C": true, "-c": true, "--git-dir": true, "--work-tree": true, "--namespace": true}

// GitDriverRisk reports whether line runs git diff, log or show and the
// project folder root has git config that could run a program when they do.
func GitDriverRisk(root, line string) bool {
	return runsGitDriverRead(line) && ConfigRunsDrivers(root)
}

// runsGitDriverRead reports whether a simple command in line is git with diff,
// log or show as its subcommand (the first word that is not an option).
func runsGitDriverRead(line string) bool {
	for _, part := range splitCommands(line) {
		words, ok := shellWords(part)
		if !ok {
			continue
		}
		if len(words) == 0 || filepath.Base(words[0]) != "git" {
			continue
		}
		for i := 1; i < len(words); i++ {
			w := words[i]
			if gitValueOpts[w] { // -C <path>, -c <name=value>, ...: skip the value
				i++
				continue
			}
			if strings.HasPrefix(w, "-") {
				continue
			}
			if gitDriverReads[w] {
				return true
			}
			break
		}
	}
	return false
}

// ConfigRunsDrivers reports whether the git config of the project at root could
// run a program in git diff, log or show: diff.external, a diff section with
// textconv or command, any alias, or an include (which can bring any of these
// in). A missing .git folder has no project config and counts as no; an
// unreadable config, or an empty root, counts as yes.
func ConfigRunsDrivers(root string) bool {
	if root == "" {
		return true
	}
	path, ok, err := gitConfigPath(root)
	if err != nil {
		return true
	}
	if !ok {
		return false
	}
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false // a .git folder with no config file: nothing to run
	}
	if err != nil {
		return true
	}
	defer f.Close()
	return configRuns(f)
}

// gitConfigPath returns the config file of the repository at root: root/.git/config,
// or for a worktree the common config its gitdir names. ok is false when root
// has no .git at all; err is set when .git cannot be read.
func gitConfigPath(root string) (path string, ok bool, err error) {
	dot := filepath.Join(root, ".git")
	fi, err := os.Stat(dot)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if fi.IsDir() {
		return filepath.Join(dot, "config"), true, nil
	}
	// A file: "gitdir: <path>" (a worktree or a submodule).
	raw, err := os.ReadFile(dot)
	if err != nil {
		return "", false, err
	}
	gitdir := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(raw)), "gitdir:"))
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(root, gitdir)
	}
	if common, err := os.ReadFile(filepath.Join(gitdir, "commondir")); err == nil {
		c := strings.TrimSpace(string(common))
		if !filepath.IsAbs(c) {
			c = filepath.Join(gitdir, c)
		}
		return filepath.Join(c, "config"), true, nil
	}
	return filepath.Join(gitdir, "config"), true, nil
}

// configRuns reports whether a git config file has a key or section that
// makes a diff run a program. Keys are matched by name, case-insensitively.
func configRuns(r io.Reader) bool {
	sc := bufio.NewScanner(r)
	section := ""
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if strings.HasPrefix(line, "[") {
			hdr := strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")
			if i := strings.IndexAny(hdr, " \t\"."); i >= 0 {
				hdr = hdr[:i]
			}
			section = strings.ToLower(strings.TrimSpace(hdr))
			if section == "alias" || section == "include" || section == "includeif" {
				return true
			}
			continue
		}
		key, _, _ := strings.Cut(line, "=")
		key = strings.ToLower(strings.TrimSpace(key))
		if section == "diff" && (key == "external" || key == "textconv" || key == "command") {
			return true
		}
	}
	return sc.Err() != nil // unreadable part: risky
}
