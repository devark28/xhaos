package tool

import (
	"path/filepath"
	"regexp"
	"strings"
)

// Command classes decide how a bash call is gated (see policy.Gate):
//   - Read: inspects the project only; runs without asking.
//   - Git: local git changes (add, commit, switch); runs without asking only
//     under auto review, and commits still follow the agent's commit setting.
//   - Exec: anything else; always waits for the owner.
//
// Classification is deliberately narrow: a command line counts as read or
// git only when every command in it (split on ;, &&, ||, |, and newlines) is
// on the lists below and nothing in it can write, run other programs, or
// reach outside the project.
const Git = "git"

var readCommands = map[string]bool{
	"ls": true, "cat": true, "head": true, "tail": true, "wc": true, "grep": true, "rg": true, "find": true,
	"pwd": true, "echo": true, "tree": true, "stat": true, "file": true, "du": true, "sort": true, "uniq": true,
	"cut": true, "diff": true, "basename": true, "dirname": true, "realpath": true, "true": true,
}

// find options that run commands or write files.
var findWriters = map[string]bool{"-exec": true, "-execdir": true, "-ok": true, "-okdir": true, "-delete": true, "-fprint": true, "-fprint0": true, "-fprintf": true, "-fls": true}

var gitReads = map[string]bool{
	"status": true, "log": true, "diff": true, "show": true, "rev-parse": true, "ls-files": true,
	"blame": true, "shortlog": true, "describe": true,
}

var gitLocalWrites = map[string]bool{"add": true, "commit": true, "switch": true}

// harmless redirections reach no file: output thrown away (>/dev/null,
// 2>/dev/null, &>/dev/null, >>/dev/null), no input (</dev/null), or output
// moved between the command's own streams (2>&1, >&2). /dev/null must end
// the word, so /dev/null/../x or /dev/nullx stay redirections.
// The fd in ">&2" must end the word too: bash reads ">&2x" (or ">&1x") as a
// redirection to the FILE "2x", and stripping only ">&2" would leave "x"
// looking like an ordinary argument.
var harmless = regexp.MustCompile(`(?:[0-9]|&)?>>?\s*/dev/null(?:$|[\s;&|)])|<\s*/dev/null(?:$|[\s;&|)])|[0-9]?>&[0-9](?:$|[\s;&|)])`)

// ClassifyCommand returns Read, Git, or Exec for a bash command line.
func ClassifyCommand(line string) string {
	line = harmless.ReplaceAllStringFunc(line, func(m string) string {
		// Keep a separator the match ended on (">/dev/null;" → ";").
		if last := m[len(m)-1]; strings.ContainsRune(" \t\n;&|)", rune(last)) {
			return " " + string(last)
		}
		return " "
	})
	if strings.ContainsAny(line, "`><") || strings.Contains(line, "$(") || strings.Contains(line, "${") {
		return Exec // command substitution, expansion tricks, or redirection
	}
	class := Read
	for _, part := range splitCommands(line) {
		words, ok := shellWords(part)
		if !ok {
			return Exec
		}
		if len(words) == 0 {
			continue
		}
		switch c := classifyWords(words); {
		case c == Exec:
			return Exec
		case c == Git:
			class = Git
		}
	}
	return class
}

func classifyWords(words []string) string {
	name := words[0]
	if strings.Contains(name, "=") || strings.Contains(name, "/") {
		return Exec // VAR=value prefixes, or a program by path
	}
	for _, w := range words[1:] {
		if outside(w) {
			return Exec
		}
	}
	if name == "git" {
		return classifyGit(words[1:])
	}
	if !readCommands[name] {
		return Exec
	}
	if name == "find" {
		for _, w := range words[1:] {
			if findWriters[w] {
				return Exec
			}
		}
	}
	return Read
}

func classifyGit(args []string) string {
	if len(args) == 0 {
		return Read
	}
	sub, rest := args[0], args[1:]
	if strings.HasPrefix(sub, "-") {
		return Exec // git -C, -c, --git-dir, ...: options that change where or how git runs
	}
	for _, a := range rest {
		if strings.HasPrefix(a, "--output") || strings.HasPrefix(a, "--exec") || a == "--ext-diff" {
			return Exec
		}
	}
	switch {
	case gitReads[sub]:
		return Read
	case sub == "branch" || sub == "tag":
		for _, a := range rest {
			switch a {
			case "-a", "--all", "-r", "--remotes", "-v", "-vv", "--verbose", "-l", "--list", "--show-current":
			default:
				return Exec // creates, renames, or deletes
			}
		}
		return Read
	case sub == "remote":
		if len(rest) == 0 || (len(rest) == 1 && rest[0] == "-v") {
			return Read
		}
		return Exec
	case gitLocalWrites[sub]:
		return Git
	}
	return Exec // push, pull, fetch, reset, checkout, rebase, config, ...
}

// outside reports a word that names a path outside the project.
func outside(w string) bool {
	if strings.HasPrefix(w, "/") || strings.HasPrefix(w, "~") {
		return true
	}
	if i := strings.IndexByte(w, '='); i >= 0 { // --flag=path
		w = w[i+1:]
		if strings.HasPrefix(w, "/") || strings.HasPrefix(w, "~") {
			return true
		}
	}
	for _, part := range strings.Split(filepath.ToSlash(w), "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

// splitCommands splits a line on ;, &&, ||, |, and newlines outside quotes.
// A lone & (run in background) makes the whole line one unsplittable part,
// which shellWords then rejects.
func splitCommands(line string) []string {
	var parts []string
	var cur strings.Builder
	quote := rune(0)
	runes := []rune(line)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if quote != 0 {
			cur.WriteRune(r)
			if r == quote {
				quote = 0
			} else if r == '\\' && quote == '"' && i+1 < len(runes) {
				i++
				cur.WriteRune(runes[i])
			}
			continue
		}
		switch r {
		case '\'', '"':
			quote = r
			cur.WriteRune(r)
		case ';', '\n', '|':
			parts = append(parts, cur.String())
			cur.Reset()
			if r == '|' && i+1 < len(runes) && runes[i+1] == '|' {
				i++
			}
		case '&':
			if i+1 < len(runes) && runes[i+1] == '&' {
				parts = append(parts, cur.String())
				cur.Reset()
				i++
			} else {
				cur.WriteRune(r) // background job: kept, so shellWords fails
			}
		default:
			cur.WriteRune(r)
		}
	}
	return append(parts, cur.String())
}

// shellWords splits one simple command into words, honoring quotes. It fails
// on unbalanced quotes and on characters with shell meaning it does not model.
func shellWords(s string) ([]string, bool) {
	var words []string
	var cur strings.Builder
	in := false
	quote := rune(0)
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case quote == '\'':
			if r == '\'' {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case quote == '"':
			switch {
			case r == '"':
				quote = 0
			case r == '\\' && i+1 < len(runes):
				i++
				cur.WriteRune(runes[i])
			case r == '$':
				return nil, false
			default:
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, in = r, true
		case r == ' ' || r == '\t':
			if in {
				words = append(words, cur.String())
				cur.Reset()
				in = false
			}
		case r == '&' || r == '(' || r == ')' || r == '{' || r == '}' || r == '$' || r == '\\':
			return nil, false
		default:
			cur.WriteRune(r)
			in = true
		}
	}
	if quote != 0 {
		return nil, false
	}
	if in {
		words = append(words, cur.String())
	}
	return words, true
}
