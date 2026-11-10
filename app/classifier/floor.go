package classifier

import (
	"path"
	"regexp"
	"strings"

	"github.com/devark28/xhaos/app/tool"
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

// gitDriverWhy is the floor's reason for a git command when the project's git
// config could run a program in it, or git's environment or exec path names
// one (see tool.GitDriverRisk).
const gitDriverWhy = "git runs a program that the project's git config or git's environment names (a diff driver, filter, fsmonitor hook, alias, or GIT_* setting)"

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
	{"it has a non-ASCII character in a word, which can be a look-alike of a command name (a Cyrillic о in go)", func(c parsed) bool {
		return anyWord(c, nonASCII)
	}},
	{"sh, bash or zsh with -c runs its text as a new command", func(c parsed) bool { return eachArgs(c, is(shellNames...), shellRunsText) }},
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
			after(c, is("bun"), word("add", "install", "i")) ||
			after(c, is("poetry"), word("add", "install", "update")) ||
			after(c, is("bundle"), word("install", "update")) ||
			after(c, is("composer"), word("install", "require", "update")) ||
			after(c, is("twine"), word("upload")) ||
			bareYarn(c) ||
			after(c, is("pip", "pip3"), word("install")) ||
			after(c, is("cargo"), word("install", "publish")) ||
			after(c, is("go"), word("install")) ||
			after(c, is("apt", "apt-get"), word("install")) ||
			after(c, is("brew"), word("install"))
	}},
	{"npx can download and run a package from the network", func(c parsed) bool { return anyWord(c, is(runnerNames...)) && !installedRunners(c) }},
	{"it pushes to or fetches from a remote, which can change or read another machine", func(c parsed) bool {
		return after(c, is("git"), word("push", "fetch", "pull", "clone"))
	}},
	{"git ls-remote, git remote update and git submodule update --remote contact a remote", func(c parsed) bool {
		return after(c, is("git"), word("ls-remote")) ||
			after2(c, is("git"), word("remote"), word("update")) ||
			(after2(c, is("git"), word("submodule"), word("update")) && anyWord(c, flagIs("--remote")))
	}},
	{"it looks up or contacts a host (dig, nslookup, host, ping, traceroute, whois, telnet, ftp, socat), even behind time, nice or a variable", func(c parsed) bool {
		return commandIs(c, hostTools...)
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
	{"it runs a program named by a file, or code given on the command line (node, python, perl, ruby, deno, cargo run, go run, go generate)", func(c parsed) bool {
		return eachArgs(c, nodeName, nodeRuns) || eachArgs(c, pythonName, pythonRuns) ||
			commandEach(c, perlName, perlRuns) || commandEach(c, rubyName, rubyRuns) ||
			sub(c, is("deno"), "run", "eval", "x", "install") || sub(c, is("cargo"), "run") ||
			sub(c, is("go"), "run", "generate")
	}},
	{"cargo --config sets a build setting that can name a program (a linker or rustc wrapper), as git -c does", func(c parsed) bool {
		return commandEach(c, is("cargo"), func(args []string) bool { return hasWord(args, flagPrefix("--config")) })
	}},
	{"bun runs a TypeScript or JavaScript file (bun x.ts, bun run x.ts); bun run with a script name is the model's", func(c parsed) bool {
		return eachArgs(c, is("bun"), bunRunsFile)
	}},
	{"sed's e command or flag runs a shell command (sed '1e id', s/x/y/e)", func(c parsed) bool {
		return eachArgs(c, is("sed"), sedExecutes)
	}},
	{"it changes project files in bulk (go fmt, go fix, --write, --fix, -w, -o, --output, --outdir)", func(c parsed) bool {
		return sub(c, is("go"), "fmt", "fix") || after(c, is("prettier", "gofmt", "goimports"), word("-w")) ||
			anyWord(c, flagIs("--write", "--fix", "--outdir", "--output")) || outputFlag(c)
	}},
	{"sed -i and --in-place change files in place", func(c parsed) bool { return after(c, is("sed"), inPlace) }},
	{"rg --pre and --pre-glob run a program on each file", func(c parsed) bool { return after(c, is("rg", "ripgrep"), flagIs("--pre", "--pre-glob")) }},
	{"sort --compress-program runs a program", func(c parsed) bool { return after(c, is("sort"), flagIs("--compress-program")) }},
	{"git --ext-diff and --textconv run a diff program named by the project's git config", func(c parsed) bool {
		return after(c, is("git"), flagIs("--ext-diff", "--textconv"))
	}},
	{gitDriverWhy, func(c parsed) bool {
		return c.root != "" && tool.GitDriverRisk(c.root, c.line)
	}},
	{"git -c and --config-env set config for one run, which can name a diff driver, a pager or an alias", func(c parsed) bool {
		return eachArgs(c, is("git"), gitConfigOption)
	}},
	{"npm, pnpm and yarn run dev, start, serve or watch (npm start too), which run a server that does not end", func(c parsed) bool {
		return serverRun(c) || sub(c, is("npm", "pnpm", "yarn"), "start")
	}},
	{"it downloads packages or modules from the network (npm view, pip download, cargo fetch, go mod download, tidy or vendor, go list -m -u)", func(c parsed) bool {
		return sub(c, is("npm", "pnpm", "yarn", "bun"), "view", "info", "show", "search", "outdated", "audit", "update", "up", "exec", "x", "dlx", "ping", "dist-tag") ||
			sub(c, is("pip", "pip3"), "download", "index") ||
			sub(c, is("cargo"), "search", "update", "fetch") ||
			after2(c, is("go"), word("mod"), word("download", "tidy", "vendor")) ||
			goListUpdates(c)
	}},
	{"git gc --prune and git reflog expire delete history that git could restore", func(c parsed) bool {
		return after(c, is("git"), word("gc")) && anyWord(c, flagPrefix("--prune")) || after2(c, is("git"), word("reflog"), word("expire"))
	}},
	{"git stash drop and git stash clear delete stashed work", gitStashDelete},
	{"env with an argument runs a program in a changed environment, and go env -w or -u writes the Go environment file", envRows},
	{"source or . runs a file's commands, except source .venv/bin/activate", func(c parsed) bool {
		return commandEach(c, is("source", "."), func(args []string) bool { return !(len(args) == 1 && args[0] == ".venv/bin/activate") })
	}},
	{"file -C or --compile writes a compiled magic file", func(c parsed) bool {
		return commandEach(c, is("file"), func(args []string) bool { return hasWord(args, compileFlag) })
	}},
	{"a shell, python, node, perl or ruby reads its program from stdin (a heredoc, a here-string, a process substitution, a file on stdin, a pipe, -s, or no script file)", stdinCode},
	{"a redirect or tee writes into .git, node_modules, or a tool config file (.npmrc, .gitattributes, .gitconfig, Makefile)", func(c parsed) bool {
		return redirectsToProtected(c) || commandEach(c, is("tee"), teeProtected)
	}},
	{"git config writes a value or changes the config (a key with a value can name a program: an alias, core.fsmonitor)", func(c parsed) bool {
		return eachArgs(c, is("git"), gitConfigWrite)
	}},
	{"a copy, move, install, link or touch writes into .git, node_modules, or a tool config file", func(c parsed) bool {
		return commandEach(c, is("cp", "mv", "install", "ln"), copyToProtected) || commandEach(c, is("touch"), anyProtected)
	}},
	{"an archive or patch writes files whose names it does not show (tar extracts, unzip, patch)", func(c parsed) bool {
		return commandEach(c, is("tar"), tarExtracts) || anyWord(c, is("unzip", "patch"))
	}},
	{"gh talks to GitHub (any command but --version, help)", func(c parsed) bool {
		return commandEach(c, is("gh"), ghReaches)
	}},
	{"openssl s_client opens a connection to another machine", func(c parsed) bool {
		return commandEach(c, is("openssl"), func(args []string) bool { return len(args) > 0 && args[0] == "s_client" })
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
// A flag such as cargo build --release names a build profile, not a step.
func releaseName(w string) bool {
	return !strings.HasPrefix(w, "-") && releaseWord.MatchString(path.Base(w))
}

// compileFlag matches file's -C (a short-flag cluster containing C) and --compile.
func compileFlag(w string) bool {
	if w == "--compile" {
		return true
	}
	return strings.HasPrefix(w, "-") && !strings.HasPrefix(w, "--") && strings.Contains(w, "C")
}

// envRows is the env row: env in command position with an argument that sets
// or runs something, and go env in command position with -w or -u (go env
// with neither only reads). Words that are only arguments, as in echo env go,
// do not count.
func envRows(c parsed) bool {
	for _, seg := range c.segments {
		for i, w := range seg {
			if !onlyWrappersBefore(seg[:i]) {
				continue
			}
			if is("env")(w) && envRuns(seg[i+1:]) {
				return true
			}
			if w == "go" && i+1 < len(seg) && seg[i+1] == "env" && hasWord(seg[i+2:], word("-w", "-u")) {
				return true
			}
		}
	}
	return false
}

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
		for len(words) > 0 && shellKeyword[words[0]] { // if true; then dig: dig is the command
			words = words[1:]
		}
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

// sub reports whether one of subs is among the first two words after a
// program (flags skipped), so a global option such as go -C dir still shows
// the subcommand.
func sub(c parsed, prog func(string) bool, subs ...string) bool {
	for _, seg := range c.segments {
		for i, w := range seg {
			if !prog(w) {
				continue
			}
			n := 0
			for _, v := range seg[i+1:] {
				if strings.HasPrefix(v, "-") {
					continue
				}
				if word(subs...)(v) {
					return true
				}
				if n++; n == 2 {
					break
				}
			}
		}
	}
	return false
}

// eachArgs reports whether f is true for the words after any use of prog.
func eachArgs(c parsed, prog func(string) bool, f func(args []string) bool) bool {
	for _, seg := range c.segments {
		for i, w := range seg {
			if prog(w) && f(seg[i+1:]) {
				return true
			}
		}
	}
	return false
}

var (
	nodeBase   = regexp.MustCompile(`^node$`)
	pythonBase = regexp.MustCompile(`^python[0-9.]*$`)
)

func nodeName(w string) bool { return nodeBase.MatchString(path.Base(w)) }

// bunScript matches a file name bun would run (a .js, .ts or similar name).
var bunScript = regexp.MustCompile(`\.(m|c)?[jt]sx?$`)

// bunRunsFile is true when one of bun's arguments names a script file.
func bunRunsFile(args []string) bool {
	for _, a := range args {
		if !strings.HasPrefix(a, "-") && bunScript.MatchString(a) {
			return true
		}
	}
	return false
}

// sedExec matches an e command or an e flag in a sed script: e at the start
// of the script or after a digit, $, /, ;, {, }, !, comma or space, and then
// a space, ;, } or the end. It is conservative: s/e/x/ matches too.
var sedExec = regexp.MustCompile(`(^|[0-9$/;{}!,\s])e($|[\s;}])`)

// sedExecutes is true when a sed argument may hold an e command or flag.
func sedExecutes(args []string) bool {
	for _, a := range args {
		if sedExec.MatchString(a) {
			return true
		}
	}
	return false
}
func pythonName(w string) bool { return pythonBase.MatchString(path.Base(w)) }

// nodeRuns is true when node is given a file, "-", or code (-e, -p).
func nodeRuns(args []string) bool {
	for _, a := range args {
		switch {
		case a == "-":
			return true
		case strings.HasPrefix(a, "--"):
			if a == "--eval" || a == "--print" || strings.HasPrefix(a, "--eval=") || strings.HasPrefix(a, "--print=") {
				return true
			}
		case strings.HasPrefix(a, "-"):
			if strings.ContainsAny(a[1:], "ep") {
				return true
			}
		default:
			return true
		}
	}
	return false
}

// pythonRuns is true when python is given a file, code (-c) or a module other
// than pytest (-m pytest is the list's job, see list.go).
func pythonRuns(args []string) bool {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-m":
			return !(i+1 < len(args) && args[i+1] == "pytest")
		case a == "-mpytest":
			return false
		case strings.HasPrefix(a, "--"):
		case strings.HasPrefix(a, "-"):
			if strings.ContainsAny(a[1:], "cm") {
				return true
			}
		default:
			return true
		}
	}
	return false
}

var (
	perlBase = regexp.MustCompile(`^perl[0-9.]*$`)
	rubyBase = regexp.MustCompile(`^ruby[0-9.]*$`)
)

func perlName(w string) bool { return perlBase.MatchString(path.Base(w)) }
func rubyName(w string) bool { return rubyBase.MatchString(path.Base(w)) }

// perlRuns is true when perl is given a file, "-", or code (-e, -E); -p and
// -n only loop over input.
func perlRuns(args []string) bool { return codeFlagRuns("eE")(args) }

// rubyRuns is true when ruby is given a file, "-", or code (-e).
func rubyRuns(args []string) bool { return codeFlagRuns("e")(args) }

// codeFlagRuns returns the test for an interpreter given a file, "-", or code
// with one of the flag letters.
func codeFlagRuns(letters string) func([]string) bool {
	return func(args []string) bool {
		for _, a := range args {
			switch {
			case a == "-" || !strings.HasPrefix(a, "-"):
				return true
			case strings.HasPrefix(a, "--"):
			case strings.ContainsAny(a[1:], letters):
				return true
			}
		}
		return false
	}
}

// teeProtected is true when tee's file arguments name a protected file (see
// protectedFile).
func teeProtected(args []string) bool {
	for _, a := range args {
		if !strings.HasPrefix(a, "-") && protectedFile.MatchString(a) {
			return true
		}
	}
	return false
}

// copyToProtected is true when a copy, move, install or link writes into a
// protected file or folder: its last operand, or the folder -t names.
func copyToProtected(args []string) bool {
	last := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-t" || a == "--target-directory":
			i++
			if i < len(args) && protectedFile.MatchString(args[i]) {
				return true
			}
			continue
		case strings.HasPrefix(a, "--target-directory="):
			if protectedFile.MatchString(strings.TrimPrefix(a, "--target-directory=")) {
				return true
			}
			continue
		case strings.HasPrefix(a, "-t") && !strings.HasPrefix(a, "--"):
			if protectedFile.MatchString(a[2:]) {
				return true
			}
			continue
		case strings.HasPrefix(a, "-"):
			continue
		}
		last = a
	}
	return protectedFile.MatchString(last)
}

// anyProtected is true when a file operand (a word that is not an option)
// names a protected file.
func anyProtected(args []string) bool {
	for _, a := range args {
		if !strings.HasPrefix(a, "-") && protectedFile.MatchString(a) {
			return true
		}
	}
	return false
}

// tarExtracts is true when tar is given an extract option, with or without a
// dash (tar -xf, tar xzf, tar --extract): the archive names the files it writes.
func tarExtracts(args []string) bool {
	for i, a := range args {
		switch {
		case a == "--extract" || a == "--get":
			return true
		case strings.HasPrefix(a, "--"):
		case i == 0 || strings.HasPrefix(a, "-"):
			if strings.ContainsRune(strings.TrimPrefix(a, "-"), 'x') && strings.Trim(strings.TrimPrefix(a, "-"), "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ") == "" {
				return true
			}
		}
	}
	return false
}

// envRuns is true when env is given anything but NAME=value assignments, so it
// runs a program (bare env only prints).
func envRuns(args []string) bool {
	for _, a := range args {
		if !strings.Contains(a, "=") {
			return true
		}
	}
	return false
}

// nonASCII matches a word with a character outside ASCII.
func nonASCII(w string) bool { return strings.IndexFunc(w, func(r rune) bool { return r > 127 }) >= 0 }

// interpStdin matches a shell or interpreter, with options only before it, that
// takes its program from a heredoc, a here-string, a process substitution or a
// file on stdin (sh <<'EOF', bash <<< x, bash <(x), python3 - <<EOF, node < x.js).
var interpStdin = regexp.MustCompile(`(?:^|[\s;&|(])(?:\S*/)?(?:sh|bash|zsh|dash|ksh|fish|python[0-9.]*|node|perl|ruby)(?:\s+-[-\w.+]*)*\s*(?:<<<?|<\(|<)`)

// interpPipe matches a pipe into python, node, perl or ruby with no script
// file (| python3, | node -); a pipe into a shell is pipeToShell's row.
var interpPipe = regexp.MustCompile(`(?m)\|&?\s*(?:\S*/)?(?:python[0-9.]*|node|perl|ruby)(?:\s+-[-\w.+]*)*\s*(?:[;&|)\n]|$)`)

// stdinCode is true when a shell, python, node, perl or ruby is given its
// program on stdin (see interpStdin, interpPipe and shellReadsStdin).
func stdinCode(c parsed) bool {
	return interpStdin.MatchString(c.plain) || interpPipe.MatchString(c.plain) || commandEach(c, is(shellNames...), shellReadsStdin)
}

// shellReadsStdin is true when a shell's arguments name no script file (only
// options, -o takes a name) or give -s, so it reads the program from stdin.
func shellReadsStdin(args []string) bool {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-o" || a == "+o":
			i++
		case strings.HasPrefix(a, "--"):
		case strings.HasPrefix(a, "-"):
			if strings.ContainsRune(a[1:], 's') {
				return true
			}
		default:
			return false
		}
	}
	return true
}

// redirTarget is the file a redirect writes to: > x, >> x, >| x, &> x. A
// descriptor (2>&1, >&2) is not a file.
var redirTarget = regexp.MustCompile(`>>?\|?\s*([^\s;&|)<>]\S*)`)

// protectedFile matches a path inside .git or node_modules, or a tool config
// file (.npmrc, .gitattributes, .gitconfig, Makefile) anywhere.
var protectedFile = regexp.MustCompile(`(^|/)(\.git|node_modules)(/|$)|(^|/)(\.npmrc|\.gitattributes|\.gitconfig|Makefile)$`)

// redirectsToProtected is true when a redirect writes to a protected file.
func redirectsToProtected(c parsed) bool {
	for _, m := range redirTarget.FindAllStringSubmatch(c.plain, -1) {
		if protectedFile.MatchString(m[1]) {
			return true
		}
	}
	return false
}

// ghReaches is true when gh is given anything but --version or help.
func ghReaches(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "--version", "version", "help", "--help", "-h":
		return false
	}
	return true
}

// commandEach is eachArgs for a program in command position (see commandIs),
// so a word such as gh as an argument does not count.
func commandEach(c parsed, prog func(string) bool, f func(args []string) bool) bool {
	for _, seg := range c.segments {
		for i, w := range seg {
			if prog(w) && onlyWrappersBefore(seg[:i]) && f(seg[i+1:]) {
				return true
			}
		}
	}
	return false
}

// shellNames are the shells whose -c flag runs a command given as text.
var shellNames = []string{"sh", "bash", "zsh", "dash", "ksh", "fish"}

// shellRunsText is true when a shell is given -c (also in a bundle such as
// -lc) before any script file. -o and +o take a name, which is skipped.
func shellRunsText(args []string) bool {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-o" || a == "+o":
			i++
		case strings.HasPrefix(a, "--"):
		case strings.HasPrefix(a, "-"):
			if strings.ContainsRune(a[1:], 'c') {
				return true
			}
		default:
			return false
		}
	}
	return false
}

// inPlace matches sed's -i (also -ni, -i.bak) and --in-place (also with =).
func inPlace(w string) bool {
	if w == "--in-place" || strings.HasPrefix(w, "--in-place=") {
		return true
	}
	return strings.HasPrefix(w, "-") && !strings.HasPrefix(w, "--") && strings.ContainsRune(w[1:], 'i')
}

// gitStashDelete is git stash followed, in the same command, by drop or clear
// (git -C dir stash drop too).
func gitStashDelete(c parsed) bool {
	for _, seg := range c.segments {
		for i, w := range seg {
			if !is("git")(w) {
				continue
			}
			for j, v := range seg[i+1:] {
				if v == "stash" && hasWord(seg[i+1+j+1:], word("drop", "clear")) {
					return true
				}
			}
		}
	}
	return false
}

// gitConfigOption is true when git's arguments set config for one run: -c or
// --config-env before the subcommand (git -C dir -c x=y log), or --config-env
// anywhere. A -c after the subcommand is an option of that subcommand (switch
// -c, log -c) and is not matched.
func gitConfigOption(args []string) bool {
	leading := true
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "--config-env") {
			return true
		}
		if !leading {
			continue
		}
		switch {
		case strings.HasPrefix(a, "-c"):
			return true
		case gitValueOpts[a]: // -C, --git-dir, ...: its value is the next word
			i++
		case strings.HasPrefix(a, "-"):
		default:
			leading = false // the subcommand
		}
	}
	return false
}

// gitConfigWrite is true when git's subcommand (its arguments after the
// global options) is config and config is given a write (see configWrites).
func gitConfigWrite(args []string) bool {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case gitValueOpts[a]: // -C dir and the like: skip the value
			i++
		case strings.HasPrefix(a, "-"):
		default:
			return a == "config" && configWrites(args[i+1:])
		}
	}
	return false
}

// configWrites is true when git config's arguments write: a key with a value
// (git config key value), or an option that adds, unsets, renames, removes or
// edits. A key alone, and --get, --list and -l, read.
func configWrites(args []string) bool {
	reads, positional := false, 0
	for _, a := range args {
		switch {
		case a == "--add" || a == "--replace-all" || a == "--unset" || a == "--unset-all" ||
			a == "--remove-section" || a == "--rename-section" || a == "-e" || a == "--edit":
			return true
		case strings.HasPrefix(a, "--get") || a == "--list" || a == "-l":
			reads = true
		case strings.HasPrefix(a, "-"):
		default:
			positional++
		}
	}
	return !reads && positional >= 2
}

// gitValueOpts are git's global options that take their value as the next
// word. -c is listed too: it is refused before this is used.
var gitValueOpts = map[string]bool{"-C": true, "-c": true, "--git-dir": true, "--work-tree": true, "--namespace": true}

// serverRun is npm, pnpm or yarn run with a script whose name, before any :,
// is dev, start, serve or watch (npm run dev:web, pnpm --filter web run dev).
// The first word after run that is not an option names the script.
func serverRun(c parsed) bool {
	for _, seg := range c.segments {
		for i, w := range seg {
			if !is("npm", "pnpm", "yarn")(w) {
				continue
			}
			for j, v := range seg[i+1:] {
				if v != "run" && v != "run-script" {
					continue
				}
				for _, x := range seg[i+1+j+1:] {
					if strings.HasPrefix(x, "-") {
						continue
					}
					name, _, _ := strings.Cut(x, ":")
					if word("dev", "start", "serve", "watch")(name) {
						return true
					}
					break
				}
			}
		}
	}
	return false
}

// shellKeyword are words that come before a command in a compound command
// (for, if, while, do, then, else, {, !) and are not commands themselves.
var shellKeyword = map[string]bool{"for": true, "if": true, "then": true, "else": true, "elif": true, "while": true, "until": true, "do": true, "{": true, "!": true}

// hostTools are programs that look up or contact a host.
var hostTools = []string{"dig", "nslookup", "host", "ping", "ping6", "traceroute", "traceroute6", "tracepath", "whois", "telnet", "ftp", "socat"}

// wrappers run the command given after them (with options or a time limit
// before it), so the program behind them is the one to check.
var wrappers = map[string]bool{"time": true, "nice": true, "nohup": true, "timeout": true, "env": true, "command": true, "exec": true, "ionice": true, "setsid": true, "stdbuf": true, "unbuffer": true, "builtin": true, "xargs": true}

// assignment matches NAME=value.
var assignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// commandIs reports whether one of names is the program of a simple command.
// Only wrappers, options, NAME=value assignments, file descriptor numbers and
// /dev/null may come before it (time -p nice -n 5 dig, X=1 host, 2>/dev/null
// ping), so a word such as ping as an argument (go test -run ping) is not it.
func commandIs(c parsed, names ...string) bool {
	m := is(names...)
	for _, seg := range c.segments {
		for i, w := range seg {
			if m(w) && onlyWrappersBefore(seg[:i]) {
				return true
			}
		}
	}
	return false
}

func onlyWrappersBefore(words []string) bool {
	for _, p := range words {
		switch {
		case wrappers[path.Base(p)], strings.HasPrefix(p, "-"), assignment.MatchString(p), p == "/dev/null":
		case strings.Trim(p, "0123456789") == "": // a file descriptor or a count (timeout 5)
		default:
			return false
		}
	}
	return true
}

// goListUpdates is go list with -m and -u: it asks the module proxy for
// newer versions. -mu and -um are the two flags bundled.
func goListUpdates(c parsed) bool {
	for _, seg := range c.segments {
		for i, w := range seg {
			if !is("go")(w) {
				continue
			}
			for j, v := range seg[i+1:] {
				if v != "list" {
					continue
				}
				args := seg[i+1+j+1:]
				if hasWord(args, word("-m", "-mu", "-um")) && hasWord(args, word("-u", "-mu", "-um")) {
					return true
				}
			}
		}
	}
	return false
}

// flagIs matches a word that is one of names, or name=value.
func flagIs(names ...string) func(string) bool {
	return func(w string) bool {
		name, _, _ := strings.Cut(w, "=")
		return word(names...)(name)
	}
}

// flagPrefix matches a word starting with p.
func flagPrefix(p string) func(string) bool {
	return func(w string) bool { return strings.HasPrefix(w, p) }
}

// outputFlag reports -o (or -oFILE) in a command that is not mvn, where -o
// means offline (the list allows it).
func outputFlag(c parsed) bool {
	for _, seg := range c.segments {
		if hasWord(seg, is("mvn")) {
			continue
		}
		for _, w := range seg {
			if w == "-o" || (len(w) > 2 && strings.HasPrefix(w, "-o") && !strings.HasPrefix(w, "--")) {
				return true
			}
		}
	}
	return false
}

func hasWord(words []string, m func(string) bool) bool {
	for _, w := range words {
		if m(w) {
			return true
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
