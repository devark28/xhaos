package history

import (
	"strings"
	"testing"
)

// Samples look real but are made up; none is a live credential.
func TestScrub(t *testing.T) {
	cases := []struct {
		name, in, want string
		n              int
	}{
		{"channel passphrase", "use xhc_9fA3kQ2mZ7vB1pLr8sTw for the link", "use [redacted] for the link", 1},
		{"anthropic key", "key sk-ant-api03-EXAMPLEKEY0123456789abcdef here", "key [redacted] here", 1},
		{"openai-style key", "OPENAI sk-proj0123456789abcdefghij0123", "OPENAI [redacted]", 1},
		{"github token", "push with ghp_1234567890abcdefghijKLMNOP", "push with [redacted]", 1},
		{"github fine-grained", "github_pat_11ABCDEFG0123456789_abcdefghij", "[redacted]", 1},
		{"slack token", "xoxb-1234567890-abcdefghijk", "[redacted]", 1},
		{"aws key id", "AKIAABCDEFGHIJKLMNOP is the id", "[redacted] is the id", 1},
		{"google key", "AIzaSyEXAMPLE0123456789abcdefghijklmno", "[redacted]", 1},
		{"bearer header", "curl -H 'Authorization: Bearer abc.def-ghi_123456'", "curl -H 'Authorization: Bearer [redacted]'", 1},
		{"url with password", "clone https://deploy:s3cretPass@git.example.test/repo.git", "clone https://[redacted]@git.example.test/repo.git", 1},
		{"url without password", "see https://example.test/docs", "see https://example.test/docs", 0},
		{"env line", "DATABASE_URL=postgres://u:p@h/db\nNAME=ok", "DATABASE_URL=[redacted]\nNAME=[redacted]", 3},
		{"export env line", "export API_TOKEN=abc123", "export API_TOKEN=[redacted]", 1},
		{"secret name inside a sentence", "Remember DEPLOY_TOKEN=abc123secret for the call", "Remember DEPLOY_TOKEN=[redacted] for the call", 1},
		{"password in a sentence", "the db password=hunter2 is in the vault", "the db password=[redacted] is in the vault", 1},
		{"token-like names that are not secrets", "max_tokens=512 and tokenizer=bpe", "max_tokens=512 and tokenizer=bpe", 0},
		{"private key block", "key:\n-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAA\n-----END OPENSSH PRIVATE KEY-----\nthanks", "key:\n[redacted]\nthanks", 1},
		{"lower-case assignment kept", "set level=debug in config", "set level=debug in config", 0},
		{"plain prose kept", "Refactor the parser and run go test ./...", "Refactor the parser and run go test ./...", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, n := Scrub(c.in)
			if got != c.want || n != c.n {
				t.Fatalf("Scrub(%q)\n got  %q (%d)\n want %q (%d)", c.in, got, n, c.want, c.n)
			}
		})
	}
}

func TestScrubLeavesNoSecretBehind(t *testing.T) {
	in := "xhc_AAAAAAAAAAAAAAAAAAAA and sk-ant-api03-EXAMPLEKEY0123456789abcdef"
	out, _ := Scrub(in)
	for _, secret := range []string{"xhc_AAAA", "EXAMPLEKEY"} {
		if strings.Contains(out, secret) {
			t.Fatalf("%q survived: %q", secret, out)
		}
	}
}
