package secret_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/devark28/xhaos/app/agent"
	"github.com/devark28/xhaos/app/apperr"
	"github.com/devark28/xhaos/app/secret"
)

func TestParseEnv(t *testing.T) {
	type kv = [2]string
	good := []struct {
		name, text string
		want       []kv
	}{
		{"plain", "A=1\nB=two", []kv{{"A", "1"}, {"B", "two"}}},
		{"comments and blanks", "# top\n\nA=1\n   # indented\n\nB=2 # trailing\n", []kv{{"A", "1"}, {"B", "2"}}},
		{"export", "export A=1\nexport   B=2\n\texport C=3", []kv{{"A", "1"}, {"B", "2"}, {"C", "3"}}},
		{"a name that merely starts with export", "exporter=1\nexport_x=2", []kv{{"exporter", "1"}, {"export_x", "2"}}},
		{"spaces around =", "A = 1\nB =  two words", []kv{{"A", "1"}, {"B", "two words"}}},
		{"empty value", "A=\nB=''\nC=\"\"", []kv{{"A", ""}, {"B", ""}, {"C", ""}}},
		{"double quotes with spaces", `A="hello  world"`, []kv{{"A", "hello  world"}}},
		{"double quote escapes", `A="a\nb\tc\"d\\e\$f\q"`, []kv{{"A", "a\nb\tc\"d\\e$f\\q"}}},
		{"single quotes are literal", `A='a\nb $C "d"'`, []kv{{"A", `a\nb $C "d"`}}},
		{"comment after a quoted value", `A="x y" # why` + "\nB='z' #c", []kv{{"A", "x y"}, {"B", "z"}}},
		{"# inside quotes and without a space before", "A=\"a # b\"\nB=#fff\nC=a#b", []kv{{"A", "a # b"}, {"B", "#fff"}, {"C", "a#b"}}},
		{"a comment right after =", "A= # nothing", []kv{{"A", ""}}},
		{"a quote inside an unquoted value", `A=it's "fine"`, []kv{{"A", `it's "fine"`}}},
		{"= inside a value", "URL=https://h/p?a=b&c=d", []kv{{"URL", "https://h/p?a=b&c=d"}}},
		{"no expansion", "A=$B\nB=${A}x", []kv{{"A", "$B"}, {"B", "${A}x"}}},
		{"multi-line quoted", "KEY=\"-----BEGIN\nline two\n-----END\"\nNEXT=1", []kv{{"KEY", "-----BEGIN\nline two\n-----END"}, {"NEXT", "1"}}},
		{"multi-line single quoted keeps backslashes", "K='a\\\nb'\nL=1", []kv{{"K", "a\\\nb"}, {"L", "1"}}},
		{"CRLF and BOM", "\ufeffA=1\r\nB=\"x\r\ny\"\r\n", []kv{{"A", "1"}, {"B", "x\ny"}}},
		{"lower case and underscore names", "_a=1\naB_9=2", []kv{{"_a", "1"}, {"aB_9", "2"}}},
	}
	for _, c := range good {
		t.Run(c.name, func(t *testing.T) {
			vars, err := secret.ParseEnv(c.text)
			if err != nil {
				t.Fatal(err)
			}
			var got []kv
			for _, v := range vars {
				got = append(got, kv{v.Key, v.Value})
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %q\nwant %q", got, c.want)
			}
		})
	}

	bad := []struct{ name, text, want string }{
		{"no equals", "A=1\nnot a pair", "line 2: expected KEY=value"},
		{"digit first", "A=1\n\n1B=2", "line 3: the name before ="},
		{"space in name", "MY KEY=1", "line 1: the name before ="},
		{"dash in name", "A=1\nB-C=2", "line 2: the name before ="},
		{"empty name", "=1", "line 1: the name before ="},
		{"export alone", "export\nA=1", "line 1: expected KEY=value"},
		{"duplicate", "A=1\nB=2\nA=3", "line 3: A is already set on line 1"},
		{"duplicate after export", "A=1\nexport A=2", "line 2: A is already set on line 1"},
		{"unterminated double quote", "A=1\nB=\"never\nends\n", "line 2: the quote that opens B"},
		{"unterminated single quote", "B='x", "line 1: the quote that opens B"},
		{"text after the quote", "A=1\nB=\"x\" y", "line 2: text after the closing quote of B"},
		{"reserved", "A=1\nPATH=/x", "line 2: PATH is reserved"},
		{"reserved lower case", "https_proxy=http://x", "line 1: https_proxy is reserved"},
		{"reserved prefix", "LD_PRELOAD=/x", "line 1: LD_PRELOAD is reserved"},
		{"xhaos prefix", "XHAOS_ENV_FILE=/x", "line 1: XHAOS_ENV_FILE is reserved"},
		{"nul", "A=a\x00b", "line 1: the value of A holds a NUL"},
		{"nothing", "# only a comment\n\n", "no KEY=value lines"},
		{"empty", "", "no KEY=value lines"},
		{"too big", "A=" + strings.Repeat("x", 65<<10), "at most 64 KB"},
		{"too many", strings.Repeat("K=1\n", 1) + func() string {
			var b strings.Builder
			for i := 0; i < 300; i++ {
				b.WriteString("V" + strings.Repeat("a", i%7) + string(rune('a'+i%26)) + string(rune('a'+i/26)) + "=1\n")
			}
			return b.String()
		}(), "more than 256 variables"},
	}
	for _, c := range bad {
		t.Run("bad "+c.name, func(t *testing.T) {
			_, err := secret.ParseEnv(c.text)
			if err == nil || !errors.Is(err, apperr.Invalid) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to contain %q", err, c.want)
			}
		})
	}
}

// An error names the line but never shows what is on it.
func TestParseEnvErrorsHideValues(t *testing.T) {
	for _, text := range []string{"hunter2-not-a-pair", "MY KEY=hunter2", "A=\"hunter2\" tail", "B=\"hunter2\nstill open"} {
		_, err := secret.ParseEnv(text)
		if err == nil || strings.Contains(err.Error(), "hunter2") {
			t.Errorf("%q: %v", text, err)
		}
	}
}

func TestEnvSecretSaveLoad(t *testing.T) {
	s := open(t)
	text := "# qc server\nDB_PASSWORD=\"p@ss word-9\"\nexport API_KEY=abcd1234efgh\nEMPTY=\n"
	v, err := s.Secrets.Save(secret.SaveRequest{WorkspaceID: ws, Name: "QC_SERVER", Kind: secret.KindEnv, Description: "qc-server .env", Value: text, Hosts: []string{"API.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if v.Kind != secret.KindEnv || strings.Join(v.Keys, ",") != "DB_PASSWORD,API_KEY,EMPTY" || strings.Join(v.Hosts, ",") != "api.example.com" {
		t.Fatalf("view = %+v", v)
	}
	// Encrypted at rest; names are the only thing the list or the events show.
	var stored string
	s.DB().QueryRow(`SELECT value FROM secrets WHERE name = 'QC_SERVER'`).Scan(&stored)
	if stored == "" || strings.Contains(stored, "p@ss") || strings.Contains(stored, "abcd1234") || strings.Contains(stored, "DB_PASSWORD") {
		t.Fatalf("stored in the clear: %q", stored)
	}
	list, _ := s.Secrets.Secrets(ws)
	raw, _ := jsonOf(list)
	if strings.Contains(raw, "p@ss") || strings.Contains(raw, "abcd1234") || strings.Contains(eventsText(t, s), "abcd1234") || !strings.Contains(raw, `"keys":["DB_PASSWORD","API_KEY","EMPTY"]`) {
		t.Fatalf("list = %s", raw)
	}
	if got, _ := s.Secrets.Reveal(secret.RevealRequest{WorkspaceID: ws, Name: "QC_SERVER"}); got != text {
		t.Fatalf("reveal = %q", got)
	}
	// An empty value keeps the text and the names; a new text replaces both.
	if v, err := s.Secrets.Save(secret.SaveRequest{WorkspaceID: ws, Name: "QC_SERVER", Kind: secret.KindEnv, Description: "renamed"}); err != nil || len(v.Keys) != 3 {
		t.Fatalf("keep = %+v %v", v, err)
	}
	if v, err := s.Secrets.Save(secret.SaveRequest{WorkspaceID: ws, Name: "QC_SERVER", Kind: secret.KindEnv, Value: "ONLY=one\r\n"}); err != nil || strings.Join(v.Keys, ",") != "ONLY" {
		t.Fatalf("replace = %+v %v", v, err)
	}
	if got, _ := s.Secrets.Reveal(secret.RevealRequest{WorkspaceID: ws, Name: "QC_SERVER"}); got != "ONLY=one\n" {
		t.Fatalf("reveal after replace = %q", got)
	}
	// A bad text is refused with its line and changes nothing.
	_, err = s.Secrets.Save(secret.SaveRequest{WorkspaceID: ws, Name: "QC_SERVER", Kind: secret.KindEnv, Value: "A=1\nB C=2"})
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("err = %v", err)
	}
	if got, _ := s.Secrets.Reveal(secret.RevealRequest{WorkspaceID: ws, Name: "QC_SERVER"}); got != "ONLY=one\n" {
		t.Fatalf("a refused save changed the value: %q", got)
	}
	// The kind is fixed once saved.
	if _, err := s.Secrets.Save(secret.SaveRequest{WorkspaceID: ws, Name: "QC_SERVER", Value: "x"}); err == nil {
		t.Fatal("changed an env secret into a token")
	}
	// Hosts follow the token rules.
	if _, err := s.Secrets.Save(secret.SaveRequest{WorkspaceID: ws, Name: "BAD_HOST", Kind: secret.KindEnv, Value: "A=1", Hosts: []string{"localhost"}}); err == nil {
		t.Fatal("accepted localhost")
	}
	// CheckEnv gives names without saving.
	keys, err := s.Secrets.CheckEnv(secret.EnvCheckRequest{WorkspaceID: ws, Text: "X=1\nY=2"})
	if err != nil || strings.Join(keys, ",") != "X,Y" {
		t.Fatalf("check = %v %v", keys, err)
	}
	if _, err := s.Secrets.CheckEnv(secret.EnvCheckRequest{WorkspaceID: ws, Text: "X=1\nX=2"}); err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("check duplicate = %v", err)
	}
}

// Check: a command's secrets may not set the same variable twice; the preview
// names the variables and the hosts.
func TestEnvSecretCheck(t *testing.T) {
	s := open(t)
	a, _ := s.Agents.Create(agent.CreateRequest{IdempotencyKey: "a", WorkspaceID: ws, Title: "a", Provider: "none", Model: "m"})
	save := func(name, kind, value string, hosts ...string) {
		t.Helper()
		if _, err := s.Secrets.Save(secret.SaveRequest{WorkspaceID: ws, Name: name, Kind: kind, Value: value, Hosts: hosts}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Secrets.Grant(secret.GrantRequest{WorkspaceID: ws, Name: name, AgentID: a.ID, Mode: secret.Ask}); err != nil {
			t.Fatal(err)
		}
	}
	save("ENV_ONE", secret.KindEnv, "SHARED=1\nONE=1", "one.example.com")
	save("ENV_TWO", secret.KindEnv, "SHARED=2\nTWO=2")
	save("ENV_THREE", secret.KindEnv, "THREE=3", "three.example.com")
	save("ONE", secret.KindToken, "tok_value_1")
	sealer := secret.NewSealer(s.Secrets)
	use, err := sealer.Check(ws, a, []string{"ENV_ONE", "ENV_THREE"})
	if err != nil {
		t.Fatal(err)
	}
	if len(use.Rules) != 2 || !strings.Contains(use.Preview, "env file ENV_ONE (2 variables: SHARED, ONE; can reach one.example.com)") {
		t.Fatalf("use = %+v", use)
	}
	if _, err := sealer.Check(ws, a, []string{"ENV_ONE", "ENV_TWO"}); err == nil || !strings.Contains(err.Error(), "$SHARED is set by both") {
		t.Fatalf("two env secrets with one variable: %v", err)
	}
	if _, err := sealer.Check(ws, a, []string{"ENV_ONE", "ONE"}); err == nil || !strings.Contains(err.Error(), "$ONE is set by both") {
		t.Fatalf("an env key and a token name: %v", err)
	}
	// ENV_THREE's own name is only a label: no variable of that name is set,
	// so a token may use it.
	save("ENV_LABEL", secret.KindEnv, "A=1")
	save("TOKEN_X", secret.KindToken, "tok_value_2")
	if _, err := sealer.Check(ws, a, []string{"ENV_LABEL", "TOKEN_X"}); err != nil {
		t.Fatal(err)
	}
}

func TestScrubEnvForms(t *testing.T) {
	v := map[string]string{"QC.PEM": "-----BEGIN\nabc\n-----END", "QC.PEM|literal": `-----BEGIN\nabc\n-----END`, "QC.TINY": "abc"}
	out := secret.Scrub(`a -----BEGIN\nabc\n-----END b abc`, v)
	if out != "a [secret:QC.PEM] b abc" {
		t.Fatalf("scrubbed = %q", out)
	}
}
