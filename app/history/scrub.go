// Package history makes past Claude Code sessions searchable: transcripts are
// read, scrubbed of secrets, split into chunks, embedded by a local model
// (llama-server with an embedding GGUF) and stored in the database, where
// agents find them with history_search.
package history

import "regexp"

// redacted replaces each secret.
const redacted = "[redacted]"

// Scrub returns text with every secret replaced, and how many were replaced.
// It runs before anything is stored, so a secret never reaches the database,
// the index or a search result.
func Scrub(text string) (string, int) {
	n := 0
	for _, r := range scrubbers {
		text = r.re.ReplaceAllStringFunc(text, func(m string) string {
			out := r.repl(m)
			if out != m { // a line already redacted by an earlier rule is not a new secret
				n++
			}
			return out
		})
	}
	return text, n
}

type scrubber struct {
	re   *regexp.Regexp
	repl func(match string) string
}

// keep returns a replacement that keeps the first capture group (for
// example "Bearer " or "https://user:") and redacts the rest.
func keep(prefixRe *regexp.Regexp) func(string) string {
	return func(m string) string {
		if sub := prefixRe.FindStringSubmatch(m); sub != nil {
			return sub[1] + redacted
		}
		return redacted
	}
}

func whole(string) string { return redacted }

// userinfo keeps the scheme and the host after the '@': https://[redacted]@host.
var schemeRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.\-]*://`)

func userinfo(m string) string { return schemeRe.FindString(m) + redacted + "@" }

var scrubbers = []scrubber{
	// Channel passphrases (xhc_...), as minted by the channel link.
	{regexp.MustCompile(`xhc_[A-Za-z0-9_\-]{8,}`), whole},
	// PEM private key blocks, whatever their type.
	{regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`), whole},
	// Well-known API keys and tokens by their prefix.
	{regexp.MustCompile(`sk-ant-[A-Za-z0-9_\-]{10,}|sk-[A-Za-z0-9_\-]{20,}|gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|xox[abprs]-[A-Za-z0-9\-]{10,}|AKIA[0-9A-Z]{16}|AIza[0-9A-Za-z_\-]{30,}`), whole},
	// Authorization headers and bearer tokens.
	{regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=\-]{8,}`), keep(regexp.MustCompile(`(?i)^(bearer\s+)`))},
	// URLs with a user and password: scheme://user:password@host.
	{regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.\-]*://[^\s/:@]+:[^\s/@]+@`), userinfo},
	// NAME=value anywhere in the text when the name says it is a secret
	// (DEPLOY_TOKEN=..., password=..., api_key=...): the name stays, the value goes.
	{regexp.MustCompile(`(?i)\b[a-z0-9_\-]*(?:token|secret|passwd|password|api[_\-]?key|credentials?)\s*=\s*[^\s'"]+`), keep(regexp.MustCompile(`^([^=]*=\s*)`))},
	// .env-style lines: an upper-case NAME=value at the start of a line (the
	// value to the end of the line; the name stays, so the chunk still makes sense).
	{regexp.MustCompile(`(?m)^(\s*(?:export\s+)?[A-Z][A-Z0-9_]*=)[^\n]*`), keep(regexp.MustCompile(`^(\s*(?:export\s+)?[A-Z][A-Z0-9_]*=)`))},
}
