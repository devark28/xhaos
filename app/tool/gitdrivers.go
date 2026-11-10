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
// system config is trusted as the owner's own. A command that points git at
// config outside the project (HOME, a git dir or work tree named on the line,
// or a cd or -C out of it) is risky whatever the project's config says.

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
	if strings.Contains(plain, "GIT_") || strings.Contains(plain, "--exec-path") || reachesOutside(plain) {
		return true
	}
	return ConfigRunsDrivers(root)
}

// movesGit matches a command that changes folder: cd, pushd or git -C.
// outsideTarget matches a target that may be outside the project: a path that
// starts with /, ~, $ or a backquote, a .. part, or cd - (the last folder).
var (
	movesGit      = regexp.MustCompile(`(^|[\s;&|(])(cd|pushd)(\s|$)|(^|\s)-C(\s|=|$)`)
	outsideTarget = regexp.MustCompile("(^|[\\s=:(])[/~$`]|\\.\\.|(cd|pushd)\\s+-(\\s|$)")
)

// reachesOutside reports whether a git command line points git at config
// outside the project: HOME or XDG_CONFIG_HOME (git reads ~/.gitconfig and
// $XDG_CONFIG_HOME/git/config), a git dir or a work tree named on the line,
// or a cd or -C to a folder that may lie outside the project.
func reachesOutside(plain string) bool {
	if strings.Contains(plain, "HOME=") || strings.Contains(plain, "--git-dir") || strings.Contains(plain, "--work-tree") {
		return true
	}
	return movesGit.MatchString(plain) && outsideTarget.MatchString(plain)
}

// unquoted drops the quote marks and backslashes of a command line, as the
// shell does before it runs a word (the same text the floor reads as plain).
func unquoted(line string) string {
	return strings.NewReplacer(`'`, "", `"`, "", `\`, "").Replace(line)
}

// ConfigRunsDrivers reports whether a git repo under root (the .git folder or
// file of root and of any folder below it, or a bare repo's folder: a command
// can cd or use -C into any of them) could run a program in any git command.
// Its config and its per-worktree config count, and so does the common config
// a worktree shares. A link to a folder outside the project counts too: git
// can enter it, and the walk does not follow links. An unreadable file, a
// tree too large to check, or an empty root counts as yes; a folder with no
// repo counts as no.
func ConfigRunsDrivers(root string) bool {
	if root == "" {
		return true
	}
	// The walk starts at the real folder: a root that is a link would not be
	// walked at all.
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return true
	}
	risky, scanned := false, 0
	err = filepath.WalkDir(realRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			risky = true // a folder that cannot be read may hold a repo
			return nil
		}
		if scanned++; scanned > maxScan {
			risky = true
			return fs.SkipAll
		}
		if d.Type()&fs.ModeSymlink != 0 {
			risky = risky || linksOutside(realRoot, p)
			return nil
		}
		if d.IsDir() && d.Name() == "objects" && isGitDir(filepath.Dir(p)) {
			risky = risky || gitDirConfigRuns(filepath.Dir(p))
			return fs.SkipDir // a bare repo's objects are not config
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

// linksOutside reports whether the link at p leads to a folder outside
// realRoot. A dangling link, or one to a file or to a folder inside the
// project, does not count.
func linksOutside(realRoot, p string) bool {
	target, err := filepath.EvalSymlinks(p)
	if err != nil {
		return false
	}
	info, err := os.Stat(target)
	if err != nil || !info.IsDir() {
		return false
	}
	rel, err := filepath.Rel(realRoot, target)
	return err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// isGitDir reports whether dir holds a bare repo's HEAD file, objects folder
// and refs folder: what git looks for before it uses a folder as its git dir.
func isGitDir(dir string) bool {
	head, err := os.Lstat(filepath.Join(dir, "HEAD"))
	if err != nil || !head.Mode().IsRegular() {
		return false
	}
	for _, d := range []string{"objects", "refs"} {
		info, err := os.Lstat(filepath.Join(dir, d))
		if err != nil || !info.IsDir() {
			return false
		}
	}
	return true
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
	first := true
	for sc.Scan() {
		text := sc.Text()
		if first {
			text = strings.TrimPrefix(text, "\uFEFF") // git skips a UTF-8 BOM
			first = false
		}
		line := strings.TrimSpace(text)
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if line[0] == '[' {
			var rest string
			var ok bool
			section, rest, ok = parseHeader(line)
			if !ok {
				return true // a header git may read another way
			}
			if runSections[section] {
				return true
			}
			line = strings.TrimSpace(rest)
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

// parseHeader reads a section header that starts at line[0] == '[': the
// section name (lower case, up to its first dot, as the legacy
// [section.subsection] form reads) and the text after the closing ']'. A
// quoted subsection may hold any character, a ] included, with \ escapes. ok
// is false for a header that does not close.
func parseHeader(line string) (section, rest string, ok bool) {
	i := 1
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	start := i
	for i < len(line) && !strings.ContainsRune(" \t\"]", rune(line[i])) {
		i++
	}
	name := strings.ToLower(line[start:i])
	section, _, _ = strings.Cut(name, ".")
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	if i < len(line) && line[i] == '"' {
		for i++; i < len(line) && line[i] != '"'; i++ {
			if line[i] == '\\' {
				i++ // the next character is part of the name
			}
		}
		if i >= len(line) {
			return "", "", false // the subsection's quote does not close
		}
		i++
	}
	if i >= len(line) || line[i] != ']' {
		return "", "", false
	}
	return section, line[i+1:], true
}
