package classifier

import (
	"reflect"
	"strings"

	"github.com/devark28/xhaos/app/tool"
)

// maxQuoted is the longest quoted string the model is shown. A longer one is
// cut from the text the model reads, which only matters when the cut changes
// what the command runs (see modelText).
const maxQuoted = 200

// modelText is the command as the model reads it: shell comments (an unquoted
// # to the end of its line) and quoted strings longer than maxQuoted are
// removed. ok is false when that removal would change the words the command
// runs; the answer is then "ask" without a model call. Text the model never
// sees cannot carry an instruction to it.
func modelText(command string) (text string, ok bool) {
	text = stripForModel(command)
	if text == command {
		return command, true
	}
	a, okA := tool.SimpleCommands(command)
	b, okB := tool.SimpleCommands(text)
	if okA && okB && reflect.DeepEqual(a, b) {
		return text, true
	}
	return command, false
}

// stripForModel drops comments and long quoted strings from command. It
// follows the shell's quoting: a # inside quotes or after a backslash is text.
func stripForModel(command string) string {
	var out strings.Builder
	var quote rune // ' or " while inside quotes, else 0
	var body strings.Builder
	escaped := false
	prev := rune(' ')
	rs := []rune(command)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case escaped:
			escaped = false
			if quote == 0 {
				out.WriteRune(r)
			} else {
				body.WriteRune(r)
			}
		case quote == 0 && r == '#' && strings.ContainsRune(" \t\n;&|(", prev):
			// a comment: skip to the end of the line, keep the newline
			for i < len(rs) && rs[i] != '\n' {
				i++
			}
			if i < len(rs) {
				out.WriteRune('\n')
			}
			r = '\n'
		case quote == 0 && r == '\\':
			escaped = true
			out.WriteRune(r)
		case quote == 0 && (r == '\'' || r == '"'):
			quote = r
			body.Reset()
		case quote != 0 && r == quote:
			if body.Len() > maxQuoted {
				out.WriteRune(quote)
				out.WriteRune(quote)
			} else {
				out.WriteRune(quote)
				out.WriteString(body.String())
				out.WriteRune(quote)
			}
			quote = 0
		case quote != 0:
			if quote == '"' && r == '\\' {
				escaped = true
			}
			body.WriteRune(r)
		default:
			out.WriteRune(r)
		}
		prev = r
	}
	if quote != 0 { // unclosed quote: keep what was read, unchanged
		out.WriteRune(quote)
		out.WriteString(body.String())
	}
	return out.String()
}
