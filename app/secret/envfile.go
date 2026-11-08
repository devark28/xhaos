package secret

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/devark28/xhaos/app/apperr"
)

// An "env" secret is the text of a .env file: KEY=value lines.
//
//	# comments and blank lines are ignored
//	export KEY=value            ("export " is accepted and dropped)
//	KEY = value                 (spaces around = are dropped)
//	KEY=plain text # a comment  (" #" after an unquoted value starts a comment)
//	KEY="two words\nand \"escapes\""   (\n \r \t \" \\ \$ in double quotes; may span lines)
//	KEY='taken literally'       (single quotes: no escapes; may span lines)
//
// Nothing is expanded: $OTHER stays $OTHER. Errors name the line, never show
// the line's text (it may be a value).

const (
	maxEnvKeys = 256
	maxEnvText = 64 << 10
)

var envKeyRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// EnvVar is one KEY=value of a .env text.
type EnvVar struct {
	Key   string
	Value string
	// Literal is the value as written between its quotes (escapes not yet
	// applied), or the same as Value for an unquoted one. A file given to a
	// command holds the literal form, so output scrubbing covers both.
	Literal string
	Line    int
}

// envReserved reports whether a command's environment must keep its own value
// of key: the names the sealed environment sets (the proxy, HOME, PATH…) and
// ones that change how programs load code. Compared without case, so
// https_proxy is as reserved as HTTPS_PROXY.
func envReserved(key string) bool {
	up := strings.ToUpper(key)
	for _, prefix := range []string{"XHAOS_", "LD_", "BASH_FUNC_", "GIT_CONFIG"} {
		if strings.HasPrefix(up, prefix) {
			return true
		}
	}
	return slices.Contains(reserved, up)
}

// ParseEnv reads a .env text. A bad text gives an apperr.Invalid error that
// starts with "line N:".
func ParseEnv(text string) ([]EnvVar, error) {
	if len(text) > maxEnvText {
		return nil, fmt.Errorf("%w: a .env text is at most %d KB", apperr.Invalid, maxEnvText>>10)
	}
	text = strings.TrimPrefix(text, "\ufeff")
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(text, "\n")
	bad := func(n int, format string, args ...any) error {
		return fmt.Errorf("%w: line %d: %s", apperr.Invalid, n, fmt.Sprintf(format, args...))
	}
	var out []EnvVar
	seen := map[string]int{}
	for i := 0; i < len(lines); i++ {
		n := i + 1
		t := strings.TrimSpace(lines[i])
		if t == "" || t[0] == '#' {
			continue
		}
		if len(t) > 6 && strings.HasPrefix(t, "export") && (t[6] == ' ' || t[6] == '\t') {
			t = strings.TrimLeft(t[6:], " \t")
		}
		eq := strings.IndexByte(t, '=')
		if eq < 0 {
			return nil, bad(n, "expected KEY=value (there is no = sign)")
		}
		key := strings.TrimSpace(t[:eq])
		if !envKeyRE.MatchString(key) {
			return nil, bad(n, "the name before = must be letters, digits and _, and not start with a digit")
		}
		if envReserved(key) {
			return nil, bad(n, "%s is reserved (the command's own environment sets it, or it changes how programs load code); rename it or leave it out", key)
		}
		if first, dup := seen[key]; dup {
			return nil, bad(n, "%s is already set on line %d", key, first)
		}
		raw := t[eq+1:]
		rest := strings.TrimLeft(raw, " \t")
		v := EnvVar{Key: key, Line: n}
		switch {
		case strings.HasPrefix(rest, `"`) || strings.HasPrefix(rest, `'`):
			quote := rest[0]
			var b strings.Builder
			var lit strings.Builder
			s, closed := rest[1:], false
			for !closed {
				j := 0
				for ; j < len(s); j++ {
					c := s[j]
					if c == quote {
						closed = true
						break
					}
					if quote == '"' && c == '\\' && j+1 < len(s) {
						lit.WriteByte(c)
						j++
						lit.WriteByte(s[j])
						switch s[j] {
						case 'n':
							b.WriteByte('\n')
						case 'r':
							b.WriteByte('\r')
						case 't':
							b.WriteByte('\t')
						case '"', '\\', '$':
							b.WriteByte(s[j])
						default:
							b.WriteByte('\\')
							b.WriteByte(s[j])
						}
						continue
					}
					b.WriteByte(c)
					lit.WriteByte(c)
				}
				if closed {
					after := strings.TrimSpace(s[j+1:])
					if after != "" && after[0] != '#' {
						return nil, bad(n, "text after the closing quote of %s (put a # before a comment)", key)
					}
					break
				}
				i++
				if i >= len(lines) {
					return nil, bad(n, "the quote that opens %s's value is never closed", key)
				}
				b.WriteByte('\n')
				lit.WriteByte('\n')
				s = lines[i]
			}
			v.Value, v.Literal = b.String(), lit.String()
		default:
			val := raw
			for j := 1; j < len(raw); j++ {
				if raw[j] == '#' && (raw[j-1] == ' ' || raw[j-1] == '\t') {
					val = raw[:j]
					break
				}
			}
			v.Value = strings.TrimSpace(val)
			v.Literal = v.Value
		}
		if strings.ContainsRune(v.Value, 0) {
			return nil, bad(n, "the value of %s holds a NUL byte, which an environment cannot carry", key)
		}
		seen[key] = n
		out = append(out, v)
		if len(out) > maxEnvKeys {
			return nil, bad(n, "more than %d variables in one secret; split it in two", maxEnvKeys)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: no KEY=value lines in it", apperr.Invalid)
	}
	return out, nil
}

// EnvKeys are the names in vars, in file order.
func EnvKeys(vars []EnvVar) []string {
	keys := make([]string, 0, len(vars))
	for _, v := range vars {
		keys = append(keys, v.Key)
	}
	return keys
}

// EnvCheckRequest is a .env text to check without saving it.
type EnvCheckRequest struct {
	WorkspaceID string `json:"workspaceId"`
	Text        string `json:"text"`
}

// CheckEnv validates a .env text the way Save will, and returns its key
// names (never values), so the form can show them before saving.
func (s *Service) CheckEnv(req EnvCheckRequest) ([]string, error) {
	vars, err := ParseEnv(req.Text)
	if err != nil {
		return nil, err
	}
	return EnvKeys(vars), nil
}
