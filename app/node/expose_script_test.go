package node_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// scripts/expose-nodes.sh is run with every system command stubbed (a PATH
// of stubs plus the plain tools it needs, nothing else) and a temporary
// folder standing for /, so nothing on this machine or the network is used.

const exposeUnit = `# Managed by the Xhaos installer (install.sh). Change settings in /etc/xhaos/install.conf and re-run it.
[Service]
ExecStart=/usr/local/bin/xhaos -db /var/lib/xhaos/data/xhaos.db -sandbox on
`

type exposeEnv struct {
	t        *testing.T
	root     string
	log      string
	path     string
	env      map[string]string
	noCaddy  bool
	extraBin map[string]string
}

func newExposeEnv(t *testing.T) *exposeEnv {
	t.Helper()
	root := t.TempDir()
	e := &exposeEnv{t: t, root: root, log: filepath.Join(t.TempDir(), "calls"), env: map[string]string{
		"FAKE_A":    "203.0.113.7",
		"FAKE_AAAA": "",
		"FAKE_MINE": "203.0.113.7",
		"FAKE_SS":   "",
		"FAKE_UFW":  "Status: active",
		"FAKE_UID":  "1000",
		"FAKE_PID":  "0",
	}}
	e.write("etc/xhaos/install.conf", "PROJECTS='/srv/projects'\nPUBLIC_HOST=''\nTRUSTED_PROXY=''\nLISTEN='127.0.0.1'\nPORT='5180'\nNODE_URL=''\n")
	e.write("etc/systemd/system/xhaos.service", exposeUnit)
	e.write("usr/local/bin/xhaos", "#!/bin/sh\necho v0.1.9\n")
	if err := os.Chmod(filepath.Join(root, "usr/local/bin/xhaos"), 0o755); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *exposeEnv) write(rel, content string) {
	e.t.Helper()
	p := filepath.Join(e.root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func (e *exposeEnv) read(rel string) string {
	b, _ := os.ReadFile(filepath.Join(e.root, rel))
	return string(b)
}

// bin builds the PATH: stubs that log every call, and links to the plain tools.
func (e *exposeEnv) bin() string {
	t := e.t
	dir := t.TempDir()
	logLine := `echo "$(basename "$0") $*" >>"$FAKE_LOG"` + "\n"
	stubs := map[string]string{
		"dig":       `case $2 in A) printf '%s\n' $FAKE_A ;; AAAA) printf '%s\n' $FAKE_AAAA ;; esac`,
		"getent":    `exit 2`,
		"ip":        `for a in $FAKE_MINE; do echo "2: eth0    inet $a/24 scope global eth0"; done`,
		"curl":      `for a; do u=$a; done; case $u in */xhaos/node) printf 426 ;; https://nodes.example.com/) printf 404 ;; *) exit 7 ;; esac`,
		"ss":        `[ -z "$FAKE_SS" ] || printf '%s\n' "$FAKE_SS"`,
		"ufw":       `if [ "$1" = status ]; then printf '%s\n' "$FAKE_UFW"; fi`,
		"apt-get":   `exit 0`,
		"systemctl": `case $1 in show) echo "$FAKE_PID" ;; is-active) exit 0 ;; esac`,
		"setsid":    `exit 0`,
		"id":        `[ "$1" = -u ] && echo "$FAKE_UID" || echo tester`,
		"caddy":     `[ "$1" != validate ] || [ -z "$FAKE_CADDY_FAIL" ] || { echo "Error: ambiguous site definition"; exit 1; }`,
	}
	for name, body := range stubs {
		if name == "caddy" && e.noCaddy {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+logLine+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, tool := range []string{"sh", "tr", "grep", "sed", "awk", "sort", "cut", "cat", "cmp", "install", "mktemp", "rm", "cp", "head", "tail", "sleep", "basename"} {
		p, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("no %s here", tool)
		}
		if err := os.Symlink(p, filepath.Join(dir, tool)); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func (e *exposeEnv) run(args ...string) (string, error) {
	e.t.Helper()
	script, _ := filepath.Abs("../../scripts/expose-nodes.sh")
	sh, err := exec.LookPath("sh")
	if err != nil {
		e.t.Skip("no sh")
	}
	cmd := exec.Command(sh, append([]string{script}, args...)...)
	cmd.Env = []string{"PATH=" + e.bin(), "HOME=" + e.root, "FAKE_LOG=" + e.log, "XHAOS_EXPOSE_ROOT=" + e.root, "XHAOS_INSTALL_SH=/nonexistent/install.sh"}
	for k, v := range e.env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	b, err := cmd.CombinedOutput()
	return string(b), err
}

// mutating: calls in the log that would change the system.
func (e *exposeEnv) mutating() []string {
	b, _ := os.ReadFile(e.log)
	var bad []string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		f := strings.Fields(l)
		if len(f) < 1 {
			continue
		}
		switch {
		case f[0] == "dig", f[0] == "getent", f[0] == "ip", f[0] == "curl", f[0] == "ss", f[0] == "id":
		case f[0] == "ufw" && len(f) > 1 && f[1] == "status":
		case f[0] == "systemctl" && len(f) > 1 && (f[1] == "show" || f[1] == "is-active"):
		default:
			bad = append(bad, l)
		}
	}
	return bad
}

func TestExposeNodesRefusesBadArguments(t *testing.T) {
	for _, args := range [][]string{
		{"localhost"},
		{"-a.example.com"},
		{"a..example.com"},
		{"nodes.example.com;reboot"},
		{"nodes_1.example.com"},
		{"nodes.example.com/x"},
		{"nodes.example.com'"},
		{"nodes.example.com\nexample.org"},
		{"nodes.example.com", "--port", "80x"},
		{"nodes.example.com", "--port", "70000"},
		{"nodes.example.com", "--bogus"},
		{"a.example.com", "b.example.com"},
		{},
	} {
		e := newExposeEnv(t)
		out, err := e.run(append(args, "--dry-run")...)
		if err == nil {
			t.Errorf("%q accepted:\n%s", args, out)
		}
		if bad := e.mutating(); len(bad) > 0 {
			t.Errorf("%q: ran %v", args, bad)
		}
	}
}

func TestExposeNodesDryRunFresh(t *testing.T) {
	e := newExposeEnv(t)
	e.noCaddy = true
	conf := e.read("etc/xhaos/install.conf")
	out, err := e.run("Nodes.Example.com", "--dry-run")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{
		"DNS: 203.0.113.7 is this server.",
		"would run: ufw allow 80,443/tcp",
		"would run: env DEBIAN_FRONTEND=noninteractive apt-get install -y caddy",
		"would write " + e.root + "/etc/caddy/sites/xhaos-nodes.caddy:",
		"nodes.example.com {",
		"handle /xhaos/node {",
		"reverse_proxy 127.0.0.1:5180",
		"respond 404",
		"import /etc/caddy/sites/*.caddy",
		"PUBLIC_HOST='nodes.example.com'",
		"would run: setsid -w sh /nonexistent/install.sh --version v0.1.9 --no-restart",
		"would ask, then run: systemctl restart xhaos",
		"would check https://nodes.example.com/xhaos/node",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in:\n%s", want, out)
		}
	}
	if bad := e.mutating(); len(bad) > 0 {
		t.Errorf("dry run ran %v", bad)
	}
	if e.read("etc/xhaos/install.conf") != conf {
		t.Error("dry run changed install.conf")
	}
	if _, err := os.Stat(filepath.Join(e.root, "etc/caddy")); err == nil {
		t.Error("dry run created /etc/caddy")
	}
}

func TestExposeNodesPortAndCaddyfiles(t *testing.T) {
	// A Caddyfile with a site of its own keeps it: one import line is added.
	e := newExposeEnv(t)
	e.write("etc/caddy/Caddyfile", "blog.example.org {\n\troot * /srv/blog\n\tfile_server\n}\n")
	out, err := e.run("nodes.example.com", "--port", "5190", "--dry-run")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{"add one line to", "blog.example.org {", "reverse_proxy 127.0.0.1:5190"} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "apt-get") {
		t.Errorf("caddy is here, yet:\n%s", out)
	}

	// The stock Caddyfile (comments differ between versions) is replaced.
	e = newExposeEnv(t)
	e.write("etc/caddy/Caddyfile", "# The Caddyfile is an easy way...\n\n:80 {\n\t# Set this path to your site's directory.\n\troot * /usr/share/caddy\n\n\t# Enable the static file server.\n\tfile_server\n\n\t# reverse_proxy localhost:8080\n}\n")
	out, err = e.run("nodes.example.com", "--dry-run")
	if err != nil || !strings.Contains(out, "replace the stock") {
		t.Errorf("stock Caddyfile not replaced: %v\n%s", err, out)
	}

	// A site for the domain already in the Caddyfile: refused.
	e = newExposeEnv(t)
	e.write("etc/caddy/Caddyfile", "nodes.example.com {\n\treverse_proxy 127.0.0.1:5180\n}\n")
	out, err = e.run("nodes.example.com", "--dry-run")
	if err == nil || !strings.Contains(out, "already has a site for nodes.example.com") {
		t.Errorf("duplicate site accepted: %v\n%s", err, out)
	}
}

func TestExposeNodesChecksDNSAndPorts(t *testing.T) {
	for _, c := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"no record", map[string]string{"FAKE_A": ""}, "has no A record yet"},
		{"elsewhere", map[string]string{"FAKE_A": "198.51.100.9"}, "which is not this server"},
		{"cloudflare", map[string]string{"FAKE_A": "104.21.3.4"}, `"DNS only"`},
		{"cloudflare v6", map[string]string{"FAKE_AAAA": "2606:4700:3030::6815:1234"}, `"DNS only"`},
		{"stray AAAA", map[string]string{"FAKE_AAAA": "2001:db8::1"}, "AAAA record must point here too"},
		{"nginx", map[string]string{"FAKE_UID": "0", "FAKE_SS": `LISTEN 0 511 0.0.0.0:80 0.0.0.0:* users:(("nginx",pid=812,fd=6))`}, "something other than Caddy"},
		{"public host taken", map[string]string{}, "PUBLIC_HOST is already 'xhaos.example.org'"},
	} {
		e := newExposeEnv(t)
		for k, v := range c.env {
			e.env[k] = v
		}
		if c.name == "public host taken" {
			e.write("etc/xhaos/install.conf", "PUBLIC_HOST='xhaos.example.org'\nPORT='5180'\nNODE_URL=''\n")
		}
		out, err := e.run("nodes.example.com", "--dry-run")
		if err == nil || !strings.Contains(out, c.want) {
			t.Errorf("%s: want %q, got %v:\n%s", c.name, c.want, err, out)
		}
		if strings.Contains(out, "would ") {
			t.Errorf("%s: planned changes before refusing:\n%s", c.name, out)
		}
	}

	// Caddy on the ports is fine.
	e := newExposeEnv(t)
	e.env["FAKE_UID"] = "0"
	e.env["FAKE_SS"] = `LISTEN 0 4096 *:443 *:* users:(("caddy",pid=900,fd=7))`
	if out, err := e.run("nodes.example.com", "--dry-run"); err != nil {
		t.Errorf("caddy's ports refused: %v\n%s", err, out)
	}
}

func TestExposeNodesIdempotent(t *testing.T) {
	e := newExposeEnv(t)
	site := "# Written by expose-nodes.sh (Xhaos): the node WebSocket only.\n# The Xhaos page is not served here; it stays behind the SSH tunnel.\nnodes.example.com {\n\thandle /xhaos/node {\n\t\treverse_proxy 127.0.0.1:5180\n\t}\n\trespond 404\n}\n"
	e.write("etc/caddy/sites/xhaos-nodes.caddy", site)
	e.write("etc/caddy/Caddyfile", "import /etc/caddy/sites/*.caddy\n")
	e.write("etc/xhaos/install.conf", "PUBLIC_HOST='nodes.example.com'\nTRUSTED_PROXY='127.0.0.1,::1'\nPORT='5180'\nNODE_URL=''\n")
	e.write("etc/systemd/system/xhaos.service", strings.Replace(exposeUnit, "-sandbox on", "-sandbox on -public-host nodes.example.com -trusted-proxy 127.0.0.1,::1", 1))
	e.env["FAKE_UFW"] = "Status: active\n\nTo Action From\n80,443/tcp                 ALLOW       Anywhere"
	// A running Xhaos started with -public-host (its command line in /proc).
	p := exec.Command("sh", "-c", "sleep 30; true", "xhaos", "-public-host", "nodes.example.com")
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Process.Kill(); _ = p.Wait() }()
	e.env["FAKE_PID"] = strconv.Itoa(p.Process.Pid)
	out, err := e.run("nodes.example.com", "--dry-run")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "Nothing to change") || strings.Contains(out, "would run") || strings.Contains(out, "would write") || strings.Contains(out, "would ask") {
		t.Errorf("a set-up server would change:\n%s", out)
	}
}

const stockCaddyfile = ":80 {\n\troot * /usr/share/caddy\n\tfile_server\n}\n"

// A real run (as a stubbed root, into the temporary /): the files it writes
// and the commands it runs.
func TestExposeNodesRun(t *testing.T) {
	e := newExposeEnv(t)
	e.env["FAKE_UID"] = "0"
	e.write("etc/caddy/Caddyfile", stockCaddyfile)
	out, err := e.run("nodes.example.com", "--yes")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "426") || !strings.Contains(out, "Nodes connect to https://nodes.example.com") {
		t.Errorf("check output:\n%s", out)
	}
	if got := e.read("etc/caddy/sites/xhaos-nodes.caddy"); !strings.Contains(got, "nodes.example.com {\n\thandle /xhaos/node {\n\t\treverse_proxy 127.0.0.1:5180\n\t}\n\trespond 404\n}\n") {
		t.Errorf("site:\n%s", got)
	}
	if got := e.read("etc/caddy/Caddyfile"); !strings.Contains(got, "\nimport /etc/caddy/sites/*.caddy\n") || strings.Contains(got, "file_server") {
		t.Errorf("Caddyfile:\n%s", got)
	}
	if e.read("etc/caddy/Caddyfile.stock") != stockCaddyfile {
		t.Error("the stock Caddyfile was not kept")
	}
	if got := e.read("etc/xhaos/install.conf"); !strings.Contains(got, "\nPUBLIC_HOST='nodes.example.com'\n") || !strings.Contains(got, "NODE_URL=''") {
		t.Errorf("install.conf:\n%s", got)
	}
	calls, _ := os.ReadFile(e.log)
	for _, want := range []string{"ufw allow 80,443/tcp", "caddy validate", "systemctl reload caddy", "setsid -w sh /nonexistent/install.sh --version v0.1.9 --no-restart", "systemctl restart xhaos"} {
		if !strings.Contains(string(calls), want) {
			t.Errorf("did not run %q:\n%s", want, calls)
		}
	}
	if strings.Contains(string(calls), "apt-get") {
		t.Errorf("installed caddy again:\n%s", calls)
	}
}

// Caddy refusing the new configuration puts its files back.
func TestExposeNodesRollsBackRefusedCaddyConfig(t *testing.T) {
	e := newExposeEnv(t)
	e.env["FAKE_UID"] = "0"
	e.env["FAKE_CADDY_FAIL"] = "1"
	mine := "blog.example.org {\n\tfile_server\n}\n"
	e.write("etc/caddy/Caddyfile", mine)
	conf := e.read("etc/xhaos/install.conf")
	out, err := e.run("nodes.example.com", "--yes")
	if err == nil || !strings.Contains(out, "ambiguous site definition") {
		t.Fatalf("refusal not reported: %v\n%s", err, out)
	}
	if e.read("etc/caddy/Caddyfile") != mine {
		t.Error("Caddyfile not restored")
	}
	if _, err := os.Stat(filepath.Join(e.root, "etc/caddy/sites/xhaos-nodes.caddy")); err == nil {
		t.Error("the new site was left behind")
	}
	if e.read("etc/xhaos/install.conf") != conf {
		t.Error("install.conf changed after Caddy refused")
	}
	calls, _ := os.ReadFile(e.log)
	if strings.Contains(string(calls), "reload") || strings.Contains(string(calls), "setsid") {
		t.Errorf("went on after the refusal:\n%s", calls)
	}
}
