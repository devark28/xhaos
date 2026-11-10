package tool

import (
	"bufio"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// git runs programs that the project's own git config names: a diff driver
// (diff.external, textconv, command), a filter, the fsmonitor hook, an alias,
// an include. Any git command can run one (status runs the fsmonitor and the
// clean filters, blame runs a textconv, an alias runs a shell command), so
// when a repo's config names one, a git command is not a read (see
// GitDriverRisk). Only config in the project is read: the owner's global and
// system config is trusted as the owner's own.

// gitWord matches git as a word of a command line (git, /usr/bin/git, the git
// in $(git ...)); the .git folder of a path does not count.
var gitWord = regexp.MustCompile(`(?:^|[^A-Za-z0-9_.-])git(?:$|[^A-Za-z0-9_])`)

// maxScan bounds the walk for repos: a bigger tree counts as risky.
const maxScan = 200000

// GitDriverRisk reports whether line runs git and git could run a program the
// project names: the config of a repo under root names one (see
// ConfigRunsDrivers), or the line sets git's environment or its exec path,
// which can name one too.
func GitDriverRisk(root, line string) bool {
	// The shell drops quote marks and backslashes before it runs a word, so
	// g''it and --exec-pa''th are git and --exec-path: match on that text.
	plain := unquoted(line)
	if !gitWord.MatchString(plain) {
		return false
	}
	if strings.Contains(plain, "GIT_") || strings.Contains(plain, "--exec-path") {
		return true
	}
	return ConfigRunsDrivers(root)
}

// unquoted drops the quote marks and backslashes of a command line, as the
// shell does before it runs a word (the same text the floor reads as plain).
func unquoted(line string) string {
	return strings.NewReplacer(`'`, "", `"`, "", `\`, "").Replace(line)
}

// ConfigRunsDrivers reports whether a git repo under root (the .git folder or
// file of root and of any folder below it: a command can cd or use -C into
// any of them) could run a program in any git command. Its config and its
// per-worktree config count, and so does the common config a worktree shares.
// An unreadable file, a tree too large to check, or an empty root counts as
// yes; a folder with no repo counts as no.
func ConfigRunsDrivers(root string) bool {
	if root == "" {
		return true
	}
	risky, scanned := false, 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			risky = true // a folder that cannot be read may hold a repo
			return nil
		}
		if scanned++; scanned > maxScan {
			risky = true
			return fs.SkipAll
		}
		if d.Name() != ".git" {
			return nil
		}
		if d.IsDir() {
			risky = risky || gitDirConfigRuns(p)
			return fs.SkipDir // its objects are not config
		}
		risky = risky || dotGitFileRuns(filepath.Dir(p), p)
		return nil
	})
	return risky || err != nil
}

// dotGitFileRuns reports whether the repo of a .git file (a worktree or a
// submodule, "gitdir: <path>"; dir is the folder that holds it) could run a
// program. A gitdir that does not exist has no config to run.
func dotGitFileRuns(dir, dotGit string) bool {
	raw, err := os.ReadFile(dotGit)
	if err != nil {
		return true
	}
	gitdir := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(raw)), "gitdir:"))
	if gitdir == "" {
		return true
	}
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(dir, gitdir)
	}
	if _, err := os.Stat(gitdir); errors.Is(err, fs.ErrNotExist) {
		return false
	}
	return gitDirConfigRuns(gitdir)
}

// gitDirConfigRuns reports whether the config files of the git dir gitdir
// could run a program: its own config and per-worktree config, and the common
// config its commondir names (a worktree shares it).
func gitDirConfigRuns(gitdir string) bool {
	common := gitdir
	raw, err := os.ReadFile(filepath.Join(gitdir, "commondir"))
	switch {
	case err == nil:
		common = strings.TrimSpace(string(raw))
		if !filepath.IsAbs(common) {
			common = filepath.Join(gitdir, common)
		}
	case !errors.Is(err, fs.ErrNotExist):
		return true
	}
	for _, path := range []string{
		filepath.Join(gitdir, "config.worktree"),
		filepath.Join(gitdir, "config"),
		filepath.Join(common, "config"),
	} {
		if configFileRuns(path) {
			return true
		}
	}
	return false
}

// configFileRuns reports whether one config file could run a program. A file
// that does not exist runs nothing; one that cannot be read may.
func configFileRuns(path string) bool {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false
	}
	if err != nil {
		return true
	}
	defer f.Close()
	return configRuns(f)
}

// runSections are sections whose every key can run a program or bring in more
// config: an alias runs a shell command, an include adds config files.
var runSections = map[string]bool{"alias": true, "include": true, "includeif": true, "pager": true}

// runKeys are section.key pairs that name a program git runs (the section is
// the name before any subsection, so filter "lfs" clean is filter.clean).
var runKeys = map[string]bool{
	"diff.external": true, "diff.textconv": true, "diff.command": true,
	"filter.clean": true, "filter.smudge": true, "filter.process": true,
	"merge.driver":   true,
	"core.fsmonitor": true, "core.hookspath": true, "core.pager": true, "core.editor": true,
	"core.sshcommand": true, "core.askpass": true, "core.gitproxy": true,
	"credential.helper": true, "gpg.program": true, "sequence.editor": true,
	"remote.uploadpack": true, "remote.receivepack": true, "remote.vcs": true,
}

// configRuns reports whether a git config file could make git run a program:
// a key in runKeys, any key of a runSections section, or a line git cannot
// read. Section and key names are case-insensitive. A key on the same line as
// its section header ([diff] external = x) counts.
func configRuns(r io.Reader) bool {
	sc := bufio.NewScanner(r)
	section := ""
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if line[0] == '[' {
			end := strings.IndexByte(line, ']')
			if end < 0 {
				return true // a header git may read another way
			}
			name := strings.ToLower(line[1:end])
			if f := strings.FieldsFunc(name, func(r rune) bool { return r == ' ' || r == '\t' || r == '"' || r == '.' }); len(f) > 0 {
				section = f[0]
			} else {
				section = ""
			}
			if runSections[section] {
				return true
			}
			line = strings.TrimSpace(line[end+1:])
			if line == "" {
				continue
			}
		}
		key, _, _ := strings.Cut(line, "=")
		key = strings.ToLower(strings.TrimSpace(key))
		if runKeys[section+"."+key] {
			return true
		}
	}
	return sc.Err() != nil // an unreadable part: risky
}
