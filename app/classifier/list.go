package classifier

import (
	"regexp"
	"strings"

	"github.com/devark28/xhaos/app/tool"
)

// The built-in list: exact build, test and lint commands that are allowed
// with no model (Listed). It is strict: a command is listed only when every
// word is understood; anything else asks (or goes to the model, when it is on).
//
// A listed command line is one or more simple commands joined by &&, || or ;
// and pipelines (|). Every simple command is a listed program with listed
// subcommands, flags and arguments; after a pipe it may also be a pure filter
// (head, tail, grep, wc, sort, uniq, cut, tr, cat) with read-only flags. The
// only redirects are 2>&1 and 2>/dev/null. Arguments are package patterns
// (./..., ./app/...) or relative paths inside the project (no .., no leading /
// or ~, no glob that could match a dot folder). The line is read with shell
// quoting rules (single and double quotes, backslashes); a $ expansion,
// backticks, braces, subshells, other redirects, a comment, a newline or a
// background & make it not listed.
//
// The list never overrides the deny floor or the policy's hard limits:
// Allowed checks everything Eligible checks first.

// ListReason is the reason text of a listed command (after ReasonPrefix).
func ListReason(name string) string {
	return "matches the built-in list of build, test and lint commands: " + name
}

// Listed reports whether command is on the built-in list, with the names of
// the listed commands it is made of (for example "go vet, go test"). A runner
// (npx) is never listed without a project folder: see ListedIn.
func Listed(command string) (name string, ok bool) { return ListedIn("", command) }

// ListedIn is Listed for the project folder root: npx TOOL is listed when TOOL
// is a listed JavaScript tool installed in root (see runner.go).
func ListedIn(root, command string) (name string, ok bool) {
	toks, ok := lex(command)
	if !ok || len(toks) == 0 {
		return "", false
	}
	var names []string
	seen := map[string]bool{}
	// read is set when a simple command is a read beside the listed commands
	// (see simple); a runner's package is then not allowed in the same line.
	read := false
	// Split into simple commands; remember which follow a pipe.
	var cmd []token
	afterPipe := false
	flush := func(nextAfterPipe bool) bool {
		n, ok := simple(cmd, afterPipe, root)
		if !ok {
			return false
		}
		if n == "" && !afterPipe && len(cmd) > 0 {
			read = true
		}
		if n != "" && !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
		cmd, afterPipe = nil, nextAfterPipe
		return true
	}
	for _, t := range toks {
		switch t.op {
		case "":
			cmd = append(cmd, t)
		case "2>":
			cmd = append(cmd, t)
		case "|":
			if !flush(true) {
				return "", false
			}
		case "&&", "||", ";":
			if !flush(false) {
				return "", false
			}
		default:
			return "", false
		}
	}
	if !flush(false) || len(names) == 0 {
		return "", false
	}
	if read && anyRunner(names) {
		// npx TOOL can download a package; a read beside it is not taken
		// (true; npx eslint . would otherwise be listed).
		return "", false
	}
	return strings.Join(names, ", "), true
}

// readPart reports a simple command that only reads, as the gate classifies
// it (tool.ReadWords), with no glob: a glob could expand to a file name that
// starts with a dash and turns the read into a write.
//
// git is not taken: git diff and log can run a program named by the project's
// config (diff.external, textconv drivers), which the list does not judge.
func readPart(toks []token) bool {
	words := make([]string, 0, len(toks))
	for _, t := range toks {
		if t.op == "2>" {
			continue
		}
		if t.glob {
			return false
		}
		words = append(words, t.text)
	}
	return words[0] != "git" && tool.ReadWords(words)
}

// simple checks one simple command. A filter is allowed only after a pipe and
// returns an empty name.
func simple(toks []token, afterPipe bool, root string) (string, bool) {
	if len(toks) == 0 || toks[0].op != "" || toks[0].glob {
		return "", false
	}
	prog := toks[0].text
	if !afterPipe && isRunner(prog) {
		// npx TOOL ...: TOOL must be a listed JavaScript tool installed in the
		// project; the rest is judged as TOOL's own direct form.
		if len(toks) < 2 || toks[1].op != "" || toks[1].glob || !installedJS(root, toks[1].text) {
			return "", false
		}
		name, ok := simple(toks[1:], false, root)
		if !ok {
			return "", false
		}
		return prog + " " + name, true
	}
	var args []arg
	for _, t := range toks[1:] {
		if t.op == "2>" {
			continue // 2>&1 or 2>/dev/null, after the program name
		}
		args = append(args, t.arg)
	}
	if afterPipe {
		if f, ok := filters[prog]; ok {
			return "", f(args)
		}
	} else if readPart(toks) {
		// A read beside listed commands (echo ok && go build ./...): it adds no name.
		return "", true
	}
	p, ok := programs[prog]
	if !ok {
		return "", false
	}
	return p(args)
}

// --- the shell reader ---

// arg is one shell word after quote removal. glob is set when it holds an
// unquoted * or ?, which the shell would expand.
type arg struct {
	text string
	glob bool
}

// token is a word or an operator (op: | && || ; or "2>" for an allowed
// redirect).
type token struct {
	arg
	op string
}

// lex splits a command line like a shell would, and fails on anything the
// list does not read.
func lex(s string) ([]token, bool) {
	var toks []token
	var b strings.Builder
	inWord, glob, quoted := false, false, false
	end := func() {
		if inWord {
			toks = append(toks, token{arg: arg{text: b.String(), glob: glob}})
		}
		b.Reset()
		inWord, glob, quoted = false, false, false
	}
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch >= 0x7f || ch < 0x20 && ch != '\t' {
			return nil, false // newlines, control characters, non-ASCII
		}
		switch ch {
		case ' ', '\t':
			end()
		case '\'':
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				return nil, false
			}
			b.WriteString(s[i+1 : i+1+j])
			inWord, quoted = true, true
			i += j + 1
		case '"':
			i++
			for ; i < len(s) && s[i] != '"'; i++ {
				c := s[i]
				switch {
				case c == '$' || c == '`' || c == '!' || c >= 0x7f || c < 0x20 && c != '\t':
					return nil, false
				case c == '\\' && i+1 < len(s) && strings.IndexByte("\"\\$`", s[i+1]) >= 0:
					i++
					b.WriteByte(s[i])
				default:
					b.WriteByte(c)
				}
			}
			if i >= len(s) {
				return nil, false
			}
			inWord, quoted = true, true
		case '\\':
			if i+1 >= len(s) || s[i+1] < 0x20 || s[i+1] >= 0x7f {
				return nil, false
			}
			i++
			b.WriteByte(s[i])
			inWord, quoted = true, true
		case '$', '`', '(', ')', '{', '}', '<', '~', '!', '#', '[':
			// expansions, subshells, braces, input redirects, tilde, history,
			// comments, bracket globs
			return nil, false
		case '*', '?':
			b.WriteByte(ch)
			inWord, glob = true, true
		case ';':
			end()
			toks = append(toks, token{op: ";"})
		case '&':
			if i+1 < len(s) && s[i+1] == '&' {
				end()
				toks = append(toks, token{op: "&&"})
				i++
				continue
			}
			return nil, false // background, &>
		case '|':
			end()
			if i+1 < len(s) && s[i+1] == '|' {
				toks = append(toks, token{op: "||"})
				i++
			} else if i+1 < len(s) && s[i+1] == '&' {
				return nil, false // |&
			} else {
				toks = append(toks, token{op: "|"})
			}
		case '>':
			// Only 2>&1 and 2>/dev/null, written as one word.
			if !inWord || quoted || glob || b.String() != "2" {
				return nil, false
			}
			rest := s[i+1:]
			var target string
			switch {
			case strings.HasPrefix(rest, "&1"):
				target = "&1"
			case strings.HasPrefix(rest, "/dev/null"):
				target = "/dev/null"
			default:
				return nil, false
			}
			after := i + 1 + len(target)
			if after < len(s) && strings.IndexByte(" \t;|&", s[after]) < 0 {
				return nil, false
			}
			b.Reset()
			inWord = false
			toks = append(toks, token{op: "2>"})
			i = after - 1
		default:
			b.WriteByte(ch)
			inWord = true
		}
	}
	end()
	// Operators must sit between commands.
	for i, t := range toks {
		if t.op != "" && t.op != "2>" && (i == 0 || i == len(toks)-1 || toks[i-1].op != "" && toks[i-1].op != "2>") {
			return nil, false
		}
	}
	return toks, true
}

// --- argument checks ---

// spec is what a program accepts after its subcommand.
type spec struct {
	bools  []string                     // flags with no value
	short  string                       // letters that may be combined in one -abc word
	values map[string]func(string) bool // flags with a value: -f v, -f=v, -fv (one letter), --flag=v
	pos    func(arg) bool               // positional arguments; nil: none
}

// check reports whether args fit the spec.
func (s spec) check(args []arg) bool {
	_, ok := s.parse(args)
	return ok
}

// parse is check that also returns the flags seen.
func (s spec) parse(args []arg) (map[string]bool, bool) {
	seen := map[string]bool{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		t := a.text
		if !strings.HasPrefix(t, "-") || t == "-" {
			if s.pos == nil || !s.pos(a) {
				return nil, false
			}
			continue
		}
		if a.glob {
			return nil, false
		}
		if contains(s.bools, t) {
			seen[t] = true
			continue
		}
		if name, v, ok := strings.Cut(t, "="); ok {
			if f := s.values[name]; f != nil && value(v, f) {
				seen[name] = true
				continue
			}
			return nil, false
		}
		if f := s.values[t]; f != nil {
			if i+1 >= len(args) || args[i+1].glob || !value(args[i+1].text, f) {
				return nil, false
			}
			seen[t] = true
			i++
			continue
		}
		if len(t) > 2 && t[1] != '-' {
			if f := s.values[t[:2]]; f != nil {
				if !value(t[2:], f) {
					return nil, false
				}
				seen[t[:2]] = true
				continue
			}
			if s.short != "" && strings.Trim(t[1:], s.short) == "" {
				for _, l := range t[1:] {
					seen["-"+string(l)] = true
				}
				continue
			}
		}
		if len(t) == 2 && s.short != "" && strings.IndexByte(s.short, t[1]) >= 0 {
			seen[t] = true
			continue
		}
		return nil, false
	}
	return seen, true
}

// value checks a flag's value: not empty, not a flag itself, and what f says.
func value(v string, f func(string) bool) bool {
	return v != "" && !strings.HasPrefix(v, "-") && f(v)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func oneOf(names ...string) func(string) bool {
	return func(s string) bool { return contains(names, s) }
}

func match(re string) func(string) bool { return regexp.MustCompile(re).MatchString }

var (
	number   = match(`^[0-9]{1,6}$`)
	duration = match(`^[0-9][0-9a-z.]{0,15}$`)
	ident    = match(`^[A-Za-z0-9_][A-Za-z0-9_.,:+-]*$`)
	anyText  = func(string) bool { return true } // a regex or expression: quoted text, never expanded
	pathText = match(`^[A-Za-z0-9._@+,*?/-]+$`)
)

// relPath reports a path inside the project: relative, no .., no ~, and a
// glob only when it cannot match a dot folder or start with -.
func relPath(w arg) bool {
	t := w.text
	if t == "" || !pathText(t) || strings.HasPrefix(t, "-") || strings.HasPrefix(t, "/") {
		return false
	}
	if w.glob && !isAlnum(t[0]) {
		return false // a leading * could expand to a -flag file name or a dot file
	}
	for _, part := range strings.Split(t, "/") {
		if part == ".." || strings.HasPrefix(part, ".") && strings.ContainsAny(part, "*?") {
			return false
		}
	}
	return true
}

// plainPath is relPath without shell globs.
func plainPath(w arg) bool { return !w.glob && relPath(w) }

func plainPathText(s string) bool { return plainPath(arg{text: s}) }

func isAlnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_'
}

// goPkg is a Go package pattern inside the module: ., ./..., ./app/...,
// ./app/classifier/.
func goPkg(w arg) bool {
	return !w.glob && (w.text == "." || strings.HasPrefix(w.text, "./") && relPath(w))
}

// splitDash splits args at the first "--".
func splitDash(args []arg) (before, after []arg, dash bool) {
	for i, a := range args {
		if a.text == "--" && !a.glob {
			return args[:i], args[i+1:], true
		}
	}
	return args, nil, false
}

// --- the programs ---

var (
	goTags  = match(`^[A-Za-z0-9_.,]+$`)
	goBuild = spec{
		bools:  []string{"-v", "-race", "-trimpath", "-a"},
		values: map[string]func(string) bool{"-tags": goTags, "-mod": oneOf("readonly", "vendor"), "-p": number},
		pos:    goPkg,
	}
	goTest = spec{
		bools: []string{"-v", "-race", "-short", "-failfast", "-benchmem", "-json", "-cover", "-trimpath"},
		values: map[string]func(string) bool{
			"-count": number, "-run": anyText, "-skip": anyText, "-bench": anyText, "-list": anyText,
			"-timeout": duration, "-benchtime": duration, "-tags": goTags, "-p": number, "-parallel": number,
			"-cpu": match(`^[0-9,]+$`), "-shuffle": match(`^(on|off|[0-9]+)$`), "-mod": oneOf("readonly", "vendor"),
		},
		pos: goPkg,
	}
	goVet = spec{
		bools:  []string{"-v"},
		values: map[string]func(string) bool{"-tags": goTags},
		pos:    goPkg,
	}
	gofmt = spec{bools: []string{"-l", "-d", "-s", "-e"}, pos: relPath}

	// The arguments that may follow "--" after npm/pnpm/yarn test.
	scriptExtra = spec{bools: []string{"--watch=false", "--watchAll=false", "--run", "--ci", "--silent", "--verbose", "--passWithNoTests", "--bail"}}
	scripts     = []string{"build", "lint", "typecheck", "test"}

	tsc    = spec{bools: []string{"--noEmit", "--pretty", "--skipLibCheck", "--strict"}, values: map[string]func(string) bool{"-p": plainPathText, "--project": plainPathText}}
	eslint = spec{
		bools:  []string{"--quiet"},
		values: map[string]func(string) bool{"--ext": match(`^[.A-Za-z0-9,]+$`), "--max-warnings": number},
		pos:    relPath,
	}
	prettier = spec{bools: []string{"--check", "-c", "--list-different", "-l"}, pos: relPath}
	vitest   = spec{pos: plainPath}

	pytest = spec{
		bools: []string{"-x", "-q", "-v", "-s", "-vv", "-qq", "--lf", "--ff", "--exitfirst", "--quiet", "--verbose", "--no-header"},
		short: "xqvs",
		values: map[string]func(string) bool{
			"-k": anyText, "-m": anyText, "--maxfail": number, "--tb": oneOf("auto", "long", "short", "line", "native", "no"),
		},
		pos: pytestPath,
	}
	ruffCheck = spec{
		bools:  []string{"--quiet", "-q", "--statistics", "--no-fix", "--show-fixes"},
		values: map[string]func(string) bool{"--select": match(`^[A-Z0-9,]+$`), "--ignore": match(`^[A-Z0-9,]+$`)},
		pos:    relPath,
	}
	ruffFormat = spec{bools: []string{"--check", "--diff", "--quiet", "-q"}, pos: relPath}
	black      = spec{bools: []string{"--check", "--diff", "--quiet", "-q", "--verbose", "-v"}, pos: relPath}
	mypy       = spec{
		bools:  []string{"--strict", "--ignore-missing-imports", "--pretty", "--no-error-summary", "--show-error-codes"},
		values: map[string]func(string) bool{"-p": match(`^[A-Za-z0-9_.]+$`), "--package": match(`^[A-Za-z0-9_.]+$`), "-m": match(`^[A-Za-z0-9_.]+$`)},
		pos:    relPath,
	}

	crate     = match(`^[A-Za-z0-9_-]+$`)
	cargoArgs = spec{
		bools: []string{"--release", "--all-features", "--no-default-features", "--workspace", "--all", "--all-targets",
			"--lib", "--bins", "--tests", "--examples", "-q", "--quiet", "-v", "--verbose", "--locked", "--offline", "--frozen"},
		values: map[string]func(string) bool{"-p": crate, "--package": crate, "--features": match(`^[A-Za-z0-9_,/-]+$`), "-j": number, "--jobs": number},
	}
	testName      = func(w arg) bool { return !w.glob && match(`^[A-Za-z0-9_:]+$`)(w.text) }
	cargoTestArgs = spec{bools: cargoArgs.bools, values: cargoArgs.values, pos: testName}
	cargoTestBin  = spec{
		bools:  []string{"--nocapture", "--ignored", "--include-ignored", "--exact", "-q", "--quiet", "--show-output"},
		values: map[string]func(string) bool{"--test-threads": number},
		pos:    testName,
	}
	lint        = match(`^[a-z0-9_:-]+$`)
	clippyLints = spec{values: map[string]func(string) bool{"-D": lint, "-W": lint, "-A": lint, "-F": lint}}
	cargoFmt    = spec{bools: []string{"--check", "--all", "-q", "--quiet"}, values: map[string]func(string) bool{"-p": crate, "--package": crate}}

	staticcheck = spec{values: map[string]func(string) bool{"-tags": goTags, "-checks": match(`^[A-Za-z0-9_,-]+$`)}, pos: goPkg}
	golangci    = spec{bools: []string{"-v", "--verbose"}, values: map[string]func(string) bool{"--timeout": duration}, pos: plainPath}
	shellcheck  = spec{
		bools: []string{"-x", "--external-sources", "-a", "--check-sourced"},
		values: map[string]func(string) bool{
			"-S": match(`^[a-z]+$`), "--severity": match(`^[a-z]+$`), "-s": match(`^[a-z]+$`), "--shell": match(`^[a-z]+$`),
			"-e": match(`^[A-Za-z0-9,]+$`), "--exclude": match(`^[A-Za-z0-9,]+$`), "-f": match(`^[a-z0-9]+$`), "--format": match(`^[a-z0-9]+$`),
		},
		pos: relPath,
	}
	mvn    = spec{bools: []string{"-q", "--quiet", "-B", "--batch-mode", "-o", "--offline", "-e", "--errors", "-ntp", "--no-transfer-progress"}, pos: target("test", "compile", "verify")}
	gradle = spec{bools: []string{"-q", "--quiet", "--offline", "--no-daemon", "--stacktrace", "--info", "--console=plain"}, pos: target("test", "build", "check")}
	dotnet = spec{
		bools: []string{"--no-restore", "--no-build", "--nologo"},
		values: map[string]func(string) bool{
			"-c": match(`^[A-Za-z]+$`), "--configuration": match(`^[A-Za-z]+$`), "-v": match(`^[a-z]+$`), "--verbosity": match(`^[a-z]+$`),
			"-f": match(`^[a-z0-9.]+$`), "--framework": match(`^[a-z0-9.]+$`),
		},
		pos: plainPath,
	}
	// date prints the time: no setting, no file (no -s, -f, -r, -d).
	dateArgs = spec{bools: []string{"-u", "--utc", "--universal"}, pos: dateFormat}
	// lsArgs are npm ls, pnpm ls and yarn list: they print the installed tree
	// from node_modules and the lockfile, and change nothing.
	lsArgs = spec{
		bools:  []string{"--all", "--long", "-l", "--json", "--parseable", "--prod", "--production", "--dev"},
		values: map[string]func(string) bool{"--depth": number},
		pos:    packageName,
	}
	makeArgs = spec{bools: []string{"-k", "-s", "--silent", "--keep-going", "-n", "--dry-run", "--just-print"}, values: map[string]func(string) bool{"-j": number}, pos: target("build", "test", "lint")}
)

// dateFormat is a date output format: +FORMAT, with no shell expansion.
var dateFormat = func(w arg) bool {
	return !w.glob && match(`^\+[-A-Za-z0-9%:._,/+ ]*$`)(w.text)
}

// packageName is an npm package name, optionally scoped.
var packageName = func(w arg) bool {
	return !w.glob && match(`^(@[A-Za-z0-9._-]+/)?[A-Za-z0-9._-]+$`)(w.text)
}

// withTree adds a read-only listing form (npm ls, yarn list) to a runner.
func withTree(prog, sub string, run program) program {
	return func(a []arg) (string, bool) {
		if first(a) == sub {
			return named(prog+" "+sub, lsArgs.check(a[1:]))
		}
		return run(a)
	}
}

// target accepts one of names as a positional word.
func target(names ...string) func(arg) bool {
	return func(w arg) bool { return !w.glob && contains(names, w.text) }
}

// pytestPath is a test path, optionally with ::test node ids.
func pytestPath(w arg) bool {
	file, rest, _ := strings.Cut(w.text, "::")
	if !relPath(arg{text: file, glob: w.glob}) {
		return false
	}
	if rest == "" {
		return !strings.Contains(w.text, "::")
	}
	return !w.glob && match(`^[A-Za-z0-9_]+(::[A-Za-z0-9_]+)*$`)(rest)
}

type program func(args []arg) (name string, ok bool)

// named returns name when ok.
func named(name string, ok bool) (string, bool) {
	if !ok {
		return "", false
	}
	return name, true
}

func first(args []arg) string {
	if len(args) == 0 || args[0].glob {
		return ""
	}
	return args[0].text
}

// runScript is npm/pnpm/yarn: test, run <script>, or (pnpm, yarn) <script>,
// optionally followed by -- and test runner flags.
func runScript(prog string, bare bool) program {
	return func(args []arg) (string, bool) {
		before, after, _ := splitDash(args)
		if !scriptExtra.check(after) {
			return "", false
		}
		switch {
		case len(before) == 1 && first(before) == "test":
			return prog + " test", true
		case len(before) == 2 && first(before) == "run" && !before[1].glob && contains(scripts, before[1].text):
			return prog + " run " + before[1].text, true
		case bare && len(before) == 1 && contains(scripts, first(before)):
			return prog + " " + first(before), true
		}
		return "", false
	}
}

// requires checks args against s and that one of flags is present (the
// read-only mode of a formatter, --noEmit of tsc).
func requires(s spec, args []arg, flags ...string) bool {
	seen, ok := s.parse(args)
	if !ok {
		return false
	}
	for _, f := range flags {
		if seen[f] {
			return true
		}
	}
	return false
}

// js are the JavaScript tools, run directly: they run only a local binary.
// npx is listed only as a runner of one of these tools, installed in the
// project (see simple and runner.go).
var js = map[string]program{
	"tsc":    func(a []arg) (string, bool) { return named("tsc --noEmit", requires(tsc, a, "--noEmit")) },
	"eslint": func(a []arg) (string, bool) { return named("eslint", eslint.check(a)) },
	"prettier": func(a []arg) (string, bool) {
		return named("prettier --check", requires(prettier, a, "--check", "-c", "--list-different", "-l"))
	},
	"vitest": func(a []arg) (string, bool) {
		return named("vitest run", first(a) == "run" && vitest.check(a[1:]))
	},
}

var programs = map[string]program{}

func init() {
	for k, v := range js {
		programs[k] = v
	}
	programs["go"] = func(a []arg) (string, bool) {
		switch first(a) {
		case "build":
			return named("go build", goBuild.check(a[1:]))
		case "test":
			return named("go test", goTest.check(a[1:]))
		case "vet":
			return named("go vet", goVet.check(a[1:]))
		}
		return "", false
	}
	programs["gofmt"] = func(a []arg) (string, bool) { return named("gofmt", gofmt.check(a)) }
	programs["staticcheck"] = func(a []arg) (string, bool) { return named("staticcheck", staticcheck.check(a)) }
	programs["golangci-lint"] = func(a []arg) (string, bool) {
		return named("golangci-lint run", first(a) == "run" && golangci.check(a[1:]))
	}
	programs["npm"] = withTree("npm", "ls", runScript("npm", false))
	programs["pnpm"] = withTree("pnpm", "ls", runScript("pnpm", true))
	programs["yarn"] = withTree("yarn", "list", runScript("yarn", true))
	programs["date"] = func(a []arg) (string, bool) { return named("date", dateArgs.check(a)) }
	programs["make"] = func(a []arg) (string, bool) { return named("make", makeArgs.check(a)) }
	programs["pytest"] = func(a []arg) (string, bool) { return named("pytest", pytest.check(a)) }
	for _, py := range []string{"python", "python3"} {
		programs[py] = func(a []arg) (string, bool) {
			return named("python -m pytest", len(a) >= 2 && first(a) == "-m" && !a[1].glob && a[1].text == "pytest" && pytest.check(a[2:]))
		}
	}
	programs["ruff"] = func(a []arg) (string, bool) {
		switch first(a) {
		case "check":
			return named("ruff check", ruffCheck.check(a[1:]))
		case "format":
			return named("ruff format --check", requires(ruffFormat, a[1:], "--check", "--diff"))
		}
		return "", false
	}
	programs["black"] = func(a []arg) (string, bool) { return named("black --check", requires(black, a, "--check", "--diff")) }
	programs["mypy"] = func(a []arg) (string, bool) { return named("mypy", mypy.check(a)) }
	programs["cargo"] = func(a []arg) (string, bool) {
		sub := first(a)
		if sub == "" {
			return "", false
		}
		before, after, dash := splitDash(a[1:])
		switch sub {
		case "build", "check":
			return named("cargo "+sub, !dash && cargoArgs.check(before))
		case "test":
			return named("cargo test", cargoTestArgs.check(before) && cargoTestBin.check(after))
		case "clippy":
			return named("cargo clippy", cargoArgs.check(before) && clippyLints.check(after))
		case "fmt":
			seen, ok := cargoFmt.parse(before)
			if !ok {
				return "", false
			}
			if dash {
				if len(after) != 1 || after[0].text != "--check" || after[0].glob {
					return "", false
				}
				seen["--check"] = true
			}
			return named("cargo fmt --check", seen["--check"])
		}
		return "", false
	}
	programs["shellcheck"] = func(a []arg) (string, bool) { return named("shellcheck", shellcheck.check(a)) }
	programs["mvn"] = func(a []arg) (string, bool) {
		return named("mvn", mvn.check(a) && hasPositional(a))
	}
	gradleRun := func(a []arg) (string, bool) { return named("gradle", gradle.check(a) && hasPositional(a)) }
	programs["./gradlew"] = gradleRun
	programs["gradle"] = gradleRun
	programs["dotnet"] = func(a []arg) (string, bool) {
		switch first(a) {
		case "test", "build":
			return named("dotnet "+first(a), dotnet.check(a[1:]))
		}
		return "", false
	}
}

// hasPositional reports a goal or task among args (mvn and gradle with none
// run a default that can be anything).
func hasPositional(a []arg) bool {
	for _, w := range a {
		if !strings.HasPrefix(w.text, "-") {
			return true
		}
	}
	return false
}

// filters are the pure filters allowed after a pipe: they read standard
// input and print; none of the allowed flags writes a file or runs a program
// (no sort -o, no uniq output file, no grep -r).
var filters = map[string]func(args []arg) bool{
	"head": lineCount(spec{bools: []string{"-q"}, values: map[string]func(string) bool{"-n": number, "-c": number, "--lines": number, "--bytes": number}}),
	"tail": lineCount(spec{bools: []string{"-q"}, values: map[string]func(string) bool{"-n": match(`^\+?[0-9]{1,6}$`), "-c": number, "--lines": number, "--bytes": number}}),
	"grep": func(args []arg) bool {
		s := spec{
			short:  "ivEFwxcnoqsHhz",
			bools:  []string{"--line-buffered"},
			values: map[string]func(string) bool{"-e": anyText, "-A": number, "-B": number, "-C": number, "-m": number, "--color": oneOf("never", "always", "auto")},
			pos:    func(w arg) bool { return !w.glob },
		}
		seen, ok := s.parse(args)
		if !ok {
			return false
		}
		n := positionals(args, s)
		return seen["-e"] && n == 0 || !seen["-e"] && n == 1 // a pattern, never a file
	},
	"wc":   spec{short: "lwcmL"}.check,
	"sort": spec{short: "rnuhfbVdgM", values: map[string]func(string) bool{"-k": match(`^[0-9.,a-zA-Z]+$`), "-t": match(`^.$`)}}.check,
	"uniq": spec{short: "cdui", values: map[string]func(string) bool{"-f": number, "-s": number, "-w": number}}.check,
	"cut":  spec{bools: []string{"--complement", "-s"}, values: map[string]func(string) bool{"-d": match(`^.$`), "-f": match(`^[0-9,-]+$`), "-c": match(`^[0-9,-]+$`), "-b": match(`^[0-9,-]+$`)}}.check,
	"tr": func(args []arg) bool {
		s := spec{short: "dscC", pos: func(w arg) bool { return !w.glob }}
		n := positionals(args, s)
		return s.check(args) && n >= 1 && n <= 2
	},
	"cat": spec{short: "nbsAvET", pos: plainPath}.check,
}

// lineCount adds the old -NUM form (head -40, tail -100) to s.
func lineCount(s spec) func(args []arg) bool {
	return func(args []arg) bool {
		var rest []arg
		for _, a := range args {
			if !a.glob && len(a.text) > 1 && a.text[0] == '-' && number(a.text[1:]) {
				continue
			}
			rest = append(rest, a)
		}
		return s.check(rest)
	}
}

// positionals counts the positional words in args (args already passed
// s.parse), skipping the values of flags.
func positionals(args []arg, s spec) int {
	n := 0
	for i := 0; i < len(args); i++ {
		t := args[i].text
		switch {
		case !strings.HasPrefix(t, "-") || t == "-":
			n++
		case s.values[t] != nil && !contains(s.bools, t):
			i++ // the flag's value
		}
	}
	return n
}
