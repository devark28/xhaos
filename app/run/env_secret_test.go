package run_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/devark28/xhaos/app/agent"
	"github.com/devark28/xhaos/app/backend"
	"github.com/devark28/xhaos/app/llm"
	"github.com/devark28/xhaos/app/policy"
	"github.com/devark28/xhaos/app/provider"
	"github.com/devark28/xhaos/app/sandbox"
	"github.com/devark28/xhaos/app/secret"
)

const appEnv = `# qc-server
DB_PASSWORD="hunter2 with spaces"
export API_KEY=key_abcdef123456
PEM="-----BEGIN KEY-----\nMIIBVQIBADANBgkqhkiG9w0BAQEFAASCAT8wggE7AgEAAkEA\n-----END KEY-----"
TINY=ab
`

// An env secret: a granted agent's command gets each variable and
// $XHAOS_ENV_FILE (a 0600 file in its own scratch folder, gone after the
// command); every value is scrubbed from the output (not the 2-character
// one); an agent without the grant gets neither, and cannot ask for it.
func TestEnvSecretInSealedCommands(t *testing.T) {
	if sandbox.ABI() < sandbox.NetABI {
		t.Skip("this kernel's Landlock cannot restrict the network")
	}
	const grantedCmd = `echo "db=$DB_PASSWORD key=$API_KEY tiny=$TINY"
echo "pem=$PEM"
printf %s "$API_KEY" | base64
echo "file=$XHAOS_ENV_FILE"
stat -c 'mode=%a' "$XHAOS_ENV_FILE"
cat "$XHAOS_ENV_FILE"
echo "env=$(env | grep -c '^DB_PASSWORD=')"`
	h := setupIn(t, t.TempDir(), func(turn llm.Turn) (llm.Result, error) {
		last := turn.Messages[len(turn.Messages)-1]
		if len(last.Results) > 0 {
			return llm.Result{Text: "done", StopReason: "end_turn"}, nil
		}
		if strings.Contains(turn.Messages[0].Text, "ungranted") {
			return llm.Result{Calls: []llm.ToolCall{
				tc("u1", "bash", map[string]any{"command": "echo $DB_PASSWORD", "secrets": []string{"APP_ENV"}}),
				tc("u2", "bash", map[string]any{"command": `echo "[${DB_PASSWORD}][${API_KEY}][${XHAOS_ENV_FILE}]"`}),
			}, StopReason: "tool_use"}, nil
		}
		return llm.Result{Calls: []llm.ToolCall{tc("g", "bash", map[string]any{"command": grantedCmd, "secrets": []string{"APP_ENV"}})}, StopReason: "tool_use"}, nil
	}, func(o *backend.Options) { o.Sandbox = sandbox.Auto })
	h.s.Providers.Save(provider.SaveRequest{WorkspaceID: ws, ID: "f", Kind: "fake", APIKey: "k", Models: []string{"m1"}})
	if _, err := h.s.Secrets.Save(secret.SaveRequest{WorkspaceID: ws, Name: "APP_ENV", Kind: secret.KindEnv, Value: appEnv}); err != nil {
		t.Fatal(err)
	}
	auto := policy.Policy{Access: "rw", Review: "auto", Commit: "review"}
	mk := func(key string) agent.Agent {
		a, err := h.s.Agents.Create(agent.CreateRequest{IdempotencyKey: key, WorkspaceID: ws, Title: key, Provider: "f", Model: "m1", Policy: &auto})
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	granted, other := mk("granted"), mk("ungranted")
	if _, err := h.s.Secrets.Grant(secret.GrantRequest{WorkspaceID: ws, Name: "APP_ENV", AgentID: granted.ID, Mode: secret.Ask}); err != nil {
		t.Fatal(err)
	}
	h.s.Agents.Message(agent.MessageRequest{IdempotencyKey: "m2", WorkspaceID: ws, AgentIDs: []int64{other.ID}, Body: "go ungranted"})
	h.waitStatus(t, other.ID, agent.StatusAttention) // the plain command asks: it is not read-only
	h.s.Agents.Decide(agent.DecideRequest{IdempotencyKey: "d2", WorkspaceID: ws, AgentID: other.ID, CallID: "u2", Allow: true})
	h.waitStatus(t, other.ID, agent.StatusComplete)
	if st := h.tools(t, other.ID)["u1"]; st.State != agent.ToolFailed || !strings.Contains(st.Output, "not granted") {
		t.Fatalf("an ungranted agent asking for the secret: %+v", st)
	}
	if st := h.tools(t, other.ID)["u2"]; !strings.Contains(st.Output, "[][][]") {
		t.Fatalf("an ungranted agent's command saw the variables: %q", st.Output)
	}

	h.s.Agents.Message(agent.MessageRequest{IdempotencyKey: "m1", WorkspaceID: ws, AgentIDs: []int64{granted.ID}, Body: "go granted"})
	h.waitStatus(t, granted.ID, agent.StatusAttention)
	st := h.tools(t, granted.ID)["g"]
	if st.State != agent.ToolPending || !strings.Contains(st.Preview, "env file APP_ENV (4 variables: DB_PASSWORD, API_KEY, PEM, TINY; no network)") {
		t.Fatalf("pending = %+v", st)
	}
	h.s.Agents.Decide(agent.DecideRequest{IdempotencyKey: "d", WorkspaceID: ws, AgentID: granted.ID, CallID: "g", Allow: true})
	h.waitStatus(t, granted.ID, agent.StatusComplete)
	out := h.tools(t, granted.ID)["g"].Output

	for _, want := range []string{
		"db=[secret:APP_ENV.DB_PASSWORD]", "key=[secret:APP_ENV.API_KEY] tiny=ab", "pem=[secret:APP_ENV.PEM]",
		"mode=600", "# qc-server", "DB_PASSWORD=\"[secret:APP_ENV.DB_PASSWORD]\"", "export API_KEY=[secret:APP_ENV.API_KEY]", "TINY=ab", "env=1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, leak := range []string{"hunter2", "key_abcdef", "MIIBVQ", "a2V5X2FiY2RlZjEyMzQ1Ng", "BEGIN KEY"} {
		if strings.Contains(out, leak) {
			t.Errorf("%q leaked:\n%s", leak, out)
		}
	}
	m := regexp.MustCompile(`file=(\S+)`).FindStringSubmatch(out)
	if m == nil || !strings.HasSuffix(m[1], "/env/.env") {
		t.Fatalf("no env file path in:\n%s", out)
	}
	if _, err := os.Stat(m[1]); !os.IsNotExist(err) {
		t.Errorf("the env file outlived its command: %v", err)
	}
	for _, raw := range transcript(t, h) {
		if strings.Contains(raw, "hunter2") || strings.Contains(raw, "key_abcdef") {
			t.Fatalf("a value reached the log: %s", raw)
		}
	}
}

// An env secret's hosts are what its command can reach, as for a token.
func TestEnvSecretHosts(t *testing.T) {
	if sandbox.ABI() < sandbox.NetABI {
		t.Skip("this kernel's Landlock cannot restrict the network")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("no curl")
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "hello, auth=%v", r.Header.Get("Authorization") == "Bearer key_abcdef123456")
	}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "https://"))
	cmd := `curl -sk -H "Authorization: Bearer $API_KEY" https://api.allowed.test/hello; echo
curl -sk -m 5 https://evil.test/ || echo EVIL-BLOCKED
curl -sk -m 5 --noproxy '*' https://127.0.0.1:` + port + `/ || echo DIRECT-BLOCKED`
	h := setupIn(t, t.TempDir(), func(turn llm.Turn) (llm.Result, error) {
		if len(turn.Messages[len(turn.Messages)-1].Results) > 0 {
			return llm.Result{Text: "done", StopReason: "end_turn"}, nil
		}
		return llm.Result{Calls: []llm.ToolCall{tc("c", "bash", map[string]any{"command": cmd, "secrets": []string{"APP_ENV"}})}, StopReason: "tool_use"}, nil
	}, func(o *backend.Options) { o.Sandbox = sandbox.Auto })
	p := h.s.Egress()
	p.AllowPrivate = true
	p.Resolve = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	var d net.Dialer
	p.Dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, _ := net.SplitHostPort(addr)
		return d.DialContext(ctx, network, net.JoinHostPort(host, port))
	}
	h.s.Providers.Save(provider.SaveRequest{WorkspaceID: ws, ID: "f", Kind: "fake", APIKey: "k", Models: []string{"m1"}})
	if _, err := h.s.Secrets.Save(secret.SaveRequest{WorkspaceID: ws, Name: "APP_ENV", Kind: secret.KindEnv, Value: appEnv, Hosts: []string{"api.allowed.test"}}); err != nil {
		t.Fatal(err)
	}
	a, _ := h.s.Agents.Create(agent.CreateRequest{IdempotencyKey: "a", WorkspaceID: ws, Title: "a", Provider: "f", Model: "m1"})
	h.s.Secrets.Grant(secret.GrantRequest{WorkspaceID: ws, Name: "APP_ENV", AgentID: a.ID, Mode: secret.Ask})
	h.s.Agents.Message(agent.MessageRequest{IdempotencyKey: "m", WorkspaceID: ws, AgentIDs: []int64{a.ID}, Body: "go"})
	h.waitStatus(t, a.ID, agent.StatusAttention)
	if st := h.tools(t, a.ID)["c"]; !strings.Contains(st.Preview, "can reach api.allowed.test") {
		t.Fatalf("preview = %q", st.Preview)
	}
	h.s.Agents.Decide(agent.DecideRequest{IdempotencyKey: "d", WorkspaceID: ws, AgentID: a.ID, CallID: "c", Allow: true})
	h.waitStatus(t, a.ID, agent.StatusComplete)
	out := h.tools(t, a.ID)["c"].Output
	for _, want := range []string{"hello, auth=true", "EVIL-BLOCKED", "DIRECT-BLOCKED"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}
