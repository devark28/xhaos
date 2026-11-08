package classifier

import (
	"path"
	"regexp"
	"strings"
)

// The deny floor: commands the classifier never allows, whatever the model
// says. A command that matches still follows the policy (it asks the owner).
//
// The floor reads the command more broadly than a shell would, so that
// quoting cannot hide a word from it: quote marks and backslashes are
// dropped, quoted text is split into words like the rest (so the commands
// inside bash -c "…" are seen), and every word is checked, not only the
// first of each command. A harmless word in an odd place (echo curl) can
// therefore match; that only means the owner is asked. What the floor cannot
// read safely ($ expansions other than $TMPDIR, backticks, brace expansion,
// eval, a pipe into a shell) matches too.

// rule is one row of the floor: why it refuses, and when.
type rule struct {
	why   string
	match func(c parsed) bool
}

// parsed is a command line as the floor reads it.
type parsed struct {
	line     string     // as given
	plain    string     // quote marks and backslashes dropped
	segments [][]string // simple commands (split on ; & | newlines and parentheses), each split into words
	root     string     // the project folder, for the runners (see runner.go); "" when unknown
}

// floor is the whole deny floor, in one table (see floor_test.go).
var floor = []rule{
	{"it uses a $ expansion other than $TMPDIR, which can name any path or run any command", func(c parsed) bool { return strings.Contains(expandOK.ReplaceAllString(c.line, ""), "$") }},
	{"it uses backticks, which run a hidden command", func(c parsed) bool { return strings.Contains(c.line, "`") }},
	{"it uses brace expansion, which can build any word", func(c parsed) bool { return anyWord(c, braceExpansion) }},
	{"it uses eval", func(c parsed) bool { return anyWord(c, word("eval")) }},
	{"it pipes text into a shell", func(c parsed) bool { return pipeToShell.MatchString(c.plain) }},
	{"sudo runs commands as another user", func(c parsed) bool { return anyWord(c, is("sudo", "su", "doas")) }},
	{"rm -r deletes whole folders", func(c parsed) bool { return after(c, is("rm"), recursiveFlag("r", "R")) }},
	{"git clean deletes untracked files", func(c parsed) bool { return after(c, is("git"), word("clean")) }},
	{"git reset --hard discards changes", func(c parsed) bool { return after2(c, is("git"), word("reset"), word("--hard")) }},
	{"git checkout -- discards changes", func(c parsed) bool { return after2(c, is("git"), word("checkout"), word("--")) }},
	{"find -delete deletes files", func(c parsed) bool { return after(c, is("find"), word("-delete")) }},
	{"dd writes raw data", func(c parsed) bool { return anyWord(c, is("dd")) }},
	{"mkfs formats a file system", func(c parsed) bool { return anyWord(c, mkfs) }},
	{"truncate cuts files", func(c parsed) bool { return anyWord(c, is("truncate")) }},
	{"chmod or chown -R changes a whole tree", func(c parsed) bool { return after(c, is("chmod", "chown"), recursiveFlag("R")) }},
	{"curl and wget reach the network", func(c parsed) bool { return anyWord(c, is("curl", "wget")) }},
	{"ssh, scp, rsync and nc reach other machines", func(c parsed) bool {
		return anyWord(c, is("ssh", "scp", "sftp", "rsync", "nc", "ncat", "netcat"))
	}},
	{"docker and podman run containers", func(c parsed) bool { return anyWord(c, is("docker", "podman")) }},
	{"it installs or publishes packages", func(c parsed) bool {
		return after(c, is("npm", "pnpm", "yarn"), word("add", "install", "i", "ci", "publish")) ||
			bareYarn(c) ||
			after(c, is("pip", "pip3"), word("install")) ||
			after(c, is("cargo"), word("install", "publish")) ||
			after(c, is("go"), word("install")) ||
			after(c, is("apt", "apt-get"), word("install"))
	}},
	{"npx can download and run a package from the network", func(c parsed) bool { return anyWord(c, is(runnerNames...)) && !installedRunners(c) }},
	{"it pushes to or fetches from a remote, which can change or read another machine", func(c parsed) bool {
		return after(c, is("git"), word("push", "fetch", "pull", "clone"))
	}},
	{"it changes a remote system (kubectl, terraform, cloud and hosting tools)", func(c parsed) bool {
		return anyWord(c, is("kubectl", "helm", "terraform", "tofu", "pulumi", "ansible", "ansible-playbook", "aws", "gcloud", "az", "flyctl", "vercel", "netlify", "heroku", "wrangler", "firebase"))
	}},
	{"it stops or restarts processes or services", func(c parsed) bool {
		return anyWord(c, is("kill", "pkill", "killall", "shutdown", "reboot", "halt", "poweroff", "systemctl", "service", "launchctl", "crontab"))
	}},
	{"it fetches or changes Go modules, or runs a make install, deploy, publish or release target", func(c parsed) bool {
		return after(c, is("go"), word("get")) || after(c, is("make"), word("install", "deploy", "publish", "release"))
	}},
	{"it names a deploy, publish or release step", func(c parsed) bool { return anyWord(c, releaseName) }},
	{"it names a path outside the project and $TMPDIR", func(c parsed) bool { return anyWord(c, outside) || cdHome(c) }},
}

// Floor returns why the deny floor refuses command, or "" when it does not.
// It does not know the project folder, so a runner (npx and the like) is
// refused; see FloorIn.
func Floor(command string) string { return FloorIn("", command) }

// FloorIn is Floor for the project folder root: a runner is allowed when the
// tool it runs is installed there (see runner.go).
func FloorIn(root, command string) string {
	c := parse(command)
	c.root = root
	for _, r := range floor {
		if r.match(c) {
			return r.why
		}
	}
	return ""
}

var (
	// expandOK are the only $ expansions the floor reads: the command's own
	// temporary folder.
	expandOK = regexp.MustCompile(`\$TMPDIR\b|\$\{TMPDIR\}`)
	// pipeToShell is a pipe whose next command is a shell.
	pipeToShell = regexp.MustCompile(`\|&?\s*(?:\S*/)?(?:sh|bash|zsh|dash|ksh|fish)\b`)
	// releaseWord matches a word that names a deploy, publish or release step
	// (npm run deploy, node deploy.js, ./scripts/release.sh).
	releaseWord = regexp.MustCompile(`(?i)deploy|publish|release`)
)

// releaseName matches a word whose program or script name says it deploys,
// publishes or releases something.
func releaseName(w string) bool { return releaseWord.MatchString(path.Base(w)) }

// parse splits a line for the floor (see the comment at the top).
func parse(line string) parsed {
	plain := strings.NewReplacer(`'`, "", `"`, "", `\`, "").Replace(line)
	var segs [][]string
	for _, s := range strings.FieldsFunc(plain, func(r rune) bool {
		return strings.ContainsRune(";&|\n()`", r)
	}) {
		words := strings.FieldsFunc(s, func(r rune) bool {
			return r == ' ' || r == '\t' || r == '\r' || r == '<' || r == '>'
		})
		if len(words) > 0 {
			segs = append(segs, words)
		}
	}
	return parsed{line: line, plain: plain, segments: segs}
}

// is matches words whose program name (the part after the last slash) is
// one of names.
func is(names ...string) func(string) bool {
	return func(w string) bool {
		base := path.Base(w)
		for _, n := range names {
			if w == n || base == n {
				return true
			}
		}
		return false
	}
}

// word matches words that are exactly one of words (subcommands, flags).
func word(words ...string) func(string) bool {
	return func(w string) bool {
		for _, n := range words {
			if w == n {
				return true
			}
		}
		return false
	}
}

func mkfs(w string) bool {
	b := path.Base(w)
	return b == "mkfs" || strings.HasPrefix(b, "mkfs.")
}

// recursiveFlag matches --recursive and short flags holding one of letters
// (-r, -rf, -fR, …).
func recursiveFlag(letters ...string) func(string) bool {
	return func(w string) bool {
		if w == "--recursive" {
			return true
		}
		if !strings.HasPrefix(w, "-") || strings.HasPrefix(w, "--") {
			return false
		}
		for _, l := range letters {
			if strings.Contains(w[1:], l) {
				return true
			}
		}
		return false
	}
}

// braceExpansion matches a word bash would expand into several ({a,b} or
// {1..3}); {} alone (find -exec) does not.
func braceExpansion(w string) bool {
	i := strings.IndexByte(w, '{')
	return i >= 0 && (strings.Contains(w[i:], ",") || strings.Contains(w[i:], ".."))
}

// anyWord reports whether any word of the line matches.
func anyWord(c parsed, m func(string) bool) bool {
	for _, seg := range c.segments {
		for _, w := range seg {
			if m(w) {
				return true
			}
		}
	}
	return false
}

// after reports a word matching first followed, in the same command, by a
// word matching then.
func after(c parsed, first, then func(string) bool) bool {
	for _, seg := range c.segments {
		for i, w := range seg {
			if !first(w) {
				continue
			}
			for _, v := range seg[i+1:] {
				if then(v) {
					return true
				}
			}
		}
	}
	return false
}

// after2 is after with a third word following the second.
func after2(c parsed, first, second, third func(string) bool) bool {
	for _, seg := range c.segments {
		for i, w := range seg {
			if !first(w) {
				continue
			}
			for j, v := range seg[i+1:] {
				if !second(v) {
					continue
				}
				for _, x := range seg[i+1+j+1:] {
					if third(x) {
						return true
					}
				}
			}
		}
	}
	return false
}

// bareYarn is yarn with no subcommand, which installs.
func bareYarn(c parsed) bool {
	for _, seg := range c.segments {
		for i, w := range seg {
			if !is("yarn")(w) {
				continue
			}
			bare := true
			for _, v := range seg[i+1:] {
				if !strings.HasPrefix(v, "-") {
					bare = false
					break
				}
			}
			if bare {
				return true
			}
		}
	}
	return false
}

// cdHome is cd with no folder, or cd -, which go outside the project.
func cdHome(c parsed) bool {
	for _, seg := range c.segments {
		for i, w := range seg {
			if w == "cd" && (i == len(seg)-1 || seg[i+1] == "-") {
				return true
			}
		}
	}
	return false
}

// outside reports a word that names a path outside the project and
// $TMPDIR. It covers what package tool's outside does (a leading / or ~, the
// same after an =, a .. segment) and more: the same after a : or , (PATH
// lists, key:value), a value glued to a short flag (-o/etc/x, -C..), and
// globs such as .* that can match "..". /dev/null alone is allowed (it is
// how output is thrown away).
func outside(w string) bool {
	if w == "/dev/null" {
		return false
	}
	candidates := []string{w}
	candidates = append(candidates, strings.FieldsFunc(w, func(r rune) bool { return r == '=' || r == ':' || r == ',' })...)
	if len(w) > 2 && w[0] == '-' && w[1] != '-' {
		candidates = append(candidates, w[2:])
	}
	for _, c := range candidates {
		if c == "/dev/null" {
			continue
		}
		if strings.HasPrefix(c, "/") || strings.HasPrefix(c, "~") {
			return true
		}
		for _, part := range strings.Split(c, "/") {
			if part == ".." || strings.HasPrefix(part, ".") && strings.ContainsAny(part, "*?[") {
				return true
			}
		}
	}
	return false
}
