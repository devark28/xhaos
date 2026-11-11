//go:build linux

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/devark28/xhaos/app/node/client"
	"github.com/devark28/xhaos/app/node/usbconf"
	"github.com/devark28/xhaos/app/node/wire"
)

func stick(t *testing.T, conf string) string {
	t.Helper()
	dir := t.TempDir()
	if conf != "" {
		if err := os.WriteFile(filepath.Join(dir, usbconf.FileName), []byte(conf), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestApplyStickWithoutSettings(t *testing.T) {
	dir := stick(t, "")
	if !applyDir(context.Background(), dir, t.TempDir()) {
		t.Fatal("a stick with no settings only reports")
	}
	res := read(t, filepath.Join(dir, usbconf.ResultName))
	for _, want := range []string{"nothing applied", "Hostname: ", "Node: not joined"} {
		if !strings.Contains(res, want) {
			t.Errorf("result lacks %q:\n%s", want, res)
		}
	}
}

func TestApplyBadStickRemovesSecrets(t *testing.T) {
	dir := stick(t, "wifi_ssid=Lab\nwifi_password=correct horse\nwifi_colour=blue\n")
	if applyDir(context.Background(), dir, t.TempDir()) {
		t.Fatal("a wrong setting is a failure")
	}
	res := read(t, filepath.Join(dir, usbconf.ResultName))
	if !strings.Contains(res, `unknown setting "wifi_colour"`) || !strings.Contains(res, "Nothing was applied") {
		t.Errorf("result:\n%s", res)
	}
	if conf := read(t, filepath.Join(dir, usbconf.FileName)); strings.Contains(conf, "correct horse") || !strings.Contains(conf, "#wifi_ssid=Lab") {
		t.Errorf("conf left on the stick:\n%s", conf)
	}
}

func TestApplyWifiWithoutNetworkManager(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no nmcli
	dir := stick(t, "wifi_ssid=Lab\nwifi_password=correct horse\n")
	if applyDir(context.Background(), dir, t.TempDir()) {
		t.Fatal("Wi-Fi without NetworkManager is a failure")
	}
	if res := read(t, filepath.Join(dir, usbconf.ResultName)); !strings.Contains(res, "no NetworkManager") {
		t.Errorf("result:\n%s", res)
	}
	if conf := read(t, filepath.Join(dir, usbconf.FileName)); strings.Contains(conf, "correct horse") {
		t.Errorf("password left on the stick:\n%s", conf)
	}
}

func TestApplyCodeWithoutServer(t *testing.T) {
	dir := stick(t, "code=ABCD-EFGH-JKMN\n")
	if applyDir(context.Background(), dir, t.TempDir()) {
		t.Fatal("a code for a node that never joined needs server=")
	}
	if res := read(t, filepath.Join(dir, usbconf.ResultName)); !strings.Contains(res, "needs server=") {
		t.Errorf("result:\n%s", res)
	}
}

// TestApplyRejoin joins a real server: XHAOS_TEST_NODE_SERVER and
// XHAOS_TEST_NODE_CODE (a fresh code from its Nodes tab). Skipped without them.
func TestApplyRejoin(t *testing.T) {
	server, code := os.Getenv("XHAOS_TEST_NODE_SERVER"), os.Getenv("XHAOS_TEST_NODE_CODE")
	if server == "" || code == "" {
		t.Skip("set XHAOS_TEST_NODE_SERVER and XHAOS_TEST_NODE_CODE to join a real server")
	}
	state := t.TempDir()
	dir := stick(t, "server="+server+"\ncode="+code+"\n")
	if !applyDir(context.Background(), dir, state) {
		t.Fatalf("result:\n%s", read(t, filepath.Join(dir, usbconf.ResultName)))
	}
	c, _, err := client.Load(state)
	if err != nil || c.Server != server {
		t.Fatalf("joined %+v: %v", c, err)
	}
	res := read(t, filepath.Join(dir, usbconf.ResultName))
	if !strings.Contains(res, "Joined "+server) || strings.Contains(read(t, filepath.Join(dir, usbconf.FileName)), code) {
		t.Errorf("result:\n%s", res)
	}
	for _, name := range []string{"node.key", "node.json"} {
		if _, err := os.Lstat(filepath.Join(state, stagedName(name))); !os.IsNotExist(err) {
			t.Errorf("staged %s is left: %v", name, err)
		}
	}
	t.Log(res)
}

// joinedState is a state folder whose node.json has the given root.
func joinedState(t *testing.T, root string) string {
	t.Helper()
	state := t.TempDir()
	raw, _ := json.Marshal(client.Config{Server: "http://127.0.0.1:1", Node: 7, Name: "pi", Root: root})
	if err := os.WriteFile(filepath.Join(state, "node.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return state
}

// A node.json written by the node's user must not steer root: a folder
// outside the state folder that is not the user's own is refused before
// anything is joined, created or chowned.
func TestRejoinRefusesForeignRoot(t *testing.T) {
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{
		"/etc/systemd/system/xhaos-node-usb@.service.d", // missing, outside the state folder
		"/etc", // exists, root's (when the test runs as another user)
		link,   // a symlink
		"relative/work",
		"/tmp/../etc",
	} {
		if root == "/etc" && os.Geteuid() == 0 {
			continue
		}
		state := joinedState(t, root)
		dir := stick(t, "code=ABCD-EFGH-JKMN\n")
		if applyDir(context.Background(), dir, state) {
			t.Fatalf("root %s: accepted", root)
		}
		res := read(t, filepath.Join(dir, usbconf.ResultName))
		if !strings.Contains(res, "node.json's folder") || !strings.Contains(res, "Nothing was changed") {
			t.Errorf("root %s: result:\n%s", root, res)
		}
		if _, err := os.Lstat("/etc/systemd/system/xhaos-node-usb@.service.d"); err == nil && root == "/etc/systemd/system/xhaos-node-usb@.service.d" {
			t.Errorf("root %s was created", root)
		}
	}
}

func TestCheckRootAccepts(t *testing.T) {
	state := t.TempDir()
	d, err := openState(state)
	if err != nil {
		t.Fatal(err)
	}
	defer d.close()
	own := t.TempDir() // the test user's, like the node user's own folder
	for _, root := range []string{filepath.Join(state, "xhaos-work"), own} {
		if err := d.checkRoot(root, "node.json"); err != nil {
			t.Errorf("%s: %v", root, err)
		}
	}
}

// Symlinks the node's user plants in the state folder (at the final names or
// the staged ones) are replaced, never written through.
func TestStageIgnoresSymlinks(t *testing.T) {
	state := t.TempDir()
	victims := t.TempDir()
	for _, name := range []string{"node.key", stagedName("node.key"), "node.json", stagedName("node.json")} {
		victim := filepath.Join(victims, name)
		if err := os.WriteFile(victim, []byte("precious\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(victim, filepath.Join(state, name)); err != nil {
			t.Fatal(err)
		}
	}
	d, err := openState(state)
	if err != nil {
		t.Fatal(err)
	}
	defer d.close()
	if _, err := d.config(); err == nil {
		t.Error("node.json through a symlink was read")
	}
	files := []stateFile{{"node.key", []byte("new key\n")}, {"node.json", []byte("{}\n")}}
	if err := d.stage(files); err != nil {
		t.Fatal(err)
	}
	if n, err := d.commit(files); err != nil || n != 2 {
		t.Fatal(n, err)
	}
	ents, _ := os.ReadDir(victims)
	for _, e := range ents {
		if got := read(t, filepath.Join(victims, e.Name())); got != "precious\n" {
			t.Errorf("%s was written through a symlink: %q", e.Name(), got)
		}
	}
	for _, f := range files {
		fi, err := os.Lstat(filepath.Join(state, f.name))
		if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v %v", f.name, fi, err)
		}
		if got := read(t, filepath.Join(state, f.name)); got != string(f.content) {
			t.Errorf("%s = %q", f.name, got)
		}
		if _, err := os.Lstat(filepath.Join(state, stagedName(f.name))); !os.IsNotExist(err) {
			t.Errorf("staged %s left: %v", f.name, err)
		}
	}
}

func TestStateFolderSymlinkRefused(t *testing.T) {
	link := filepath.Join(t.TempDir(), "state")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	if _, err := openState(link); err == nil {
		t.Error("a state folder that is a symlink was opened")
	}
}

// A rename that fails half-way says how far it got, and leaves no staged file.
func TestCommitPartly(t *testing.T) {
	state := t.TempDir()
	d, err := openState(state)
	if err != nil {
		t.Fatal(err)
	}
	defer d.close()
	files := []stateFile{{"node.json", []byte("{}\n")}, {"node.key", []byte("k\n")}}
	if err := d.stage(files); err != nil {
		t.Fatal(err)
	}
	// node.key is a folder that is not empty: renaming a file over it fails.
	if err := os.MkdirAll(filepath.Join(state, "node.key", "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	n, err := d.commit(files)
	if err == nil || n != 1 {
		t.Fatalf("n = %d, err = %v", n, err)
	}
	if got := read(t, filepath.Join(state, "node.json")); got != "{}\n" {
		t.Errorf("node.json = %q", got)
	}
	for _, f := range files {
		if _, err := os.Lstat(filepath.Join(state, stagedName(f.name))); !os.IsNotExist(err) {
			t.Errorf("staged %s left: %v", f.name, err)
		}
	}
}

// A node installed with --no-join joins by stick with the folder, devices
// and sandbox mode install-node.sh kept in join.conf.
func TestReadJoinConf(t *testing.T) {
	joinConfOwner = os.Getuid()
	t.Cleanup(func() { joinConfOwner = 0 })
	write := func(content string, mode os.FileMode) string {
		path := filepath.Join(t.TempDir(), "join.conf")
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		os.Chmod(path, mode)
		return path
	}
	byPath := "/dev/serial/by-path/pci-0000:00:14.0-usb-0:2:1.0-port0"
	c, err := readJoinConf(write("# Managed by install-node.sh\nroot=/srv/work\ndevice=/dev/video0\ndevice="+byPath+"\nunsandboxed=no\n", 0o644))
	if err != nil || c.Root != "/srv/work" || strings.Join(c.Devices, ",") != "/dev/video0,"+byPath || c.Unsandboxed {
		t.Errorf("read %+v %v", c, err)
	}
	if c, err := readJoinConf(write("unsandboxed=yes\n", 0o644)); err != nil || !c.Unsandboxed {
		t.Errorf("unsandboxed %+v %v", c, err)
	}
	if c, err := readJoinConf(filepath.Join(t.TempDir(), "none")); err != nil || c.Root != "" || len(c.Devices) > 0 {
		t.Errorf("missing file %+v %v", c, err)
	}
	for name, content := range map[string]string{
		"device outside /dev": "device=/etc/shadow\n",
		"device with ..":      "device=/dev/../etc/shadow\n",
		"unknown line":        "server=https://evil.example\n",
		"bad unsandboxed":     "unsandboxed=maybe\n",
		"root twice":          "root=/a\nroot=/b\n",
		"unsandboxed twice":   "unsandboxed=no\nunsandboxed=yes\n",
		"CRLF":                "root=/srv/work\r\n",
		"leading space":       " root=/srv/work\n",
		"bare root":           "root\n",
		"empty root":          "root=\n",
		"root with a colon":   "root=/srv/a:b\n",
		"device with a space": "device=/dev/a b\n",
		"bare /dev/":          "device=/dev/\n",
	} {
		if _, err := readJoinConf(write(content, 0o644)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := readJoinConf(write("root=/srv/work\n", 0o666)); err == nil {
		t.Error("a file others can write was accepted")
	}
	link := filepath.Join(t.TempDir(), "join.conf")
	os.Symlink(write("root=/srv/work\n", 0o644), link)
	if _, err := readJoinConf(link); err == nil {
		t.Error("a symlink was accepted")
	}
	joinConfOwner = os.Getuid() + 1
	if _, err := readJoinConf(write("root=/srv/work\n", 0o644)); err == nil {
		t.Error("another user's file was accepted")
	}
}

// A node installed with --no-join, joined by stick, joins with join.conf's
// folder, devices and sandbox mode (node.json is not there yet).
func TestApplyRejoinWithJoinConf(t *testing.T) {
	joined := make(chan wire.Facts, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		var m wire.Msg
		if wsjson.Read(r.Context(), conn, &m) != nil || m.Type != wire.TJoin || m.Facts == nil {
			return
		}
		joined <- *m.Facts
		wsjson.Write(r.Context(), conn, wire.Msg{Type: wire.TJoined, Node: 9, Name: "cam"})
	}))
	defer srv.Close()

	state := t.TempDir()
	root := filepath.Join(state, "work")
	conf := filepath.Join(t.TempDir(), "join.conf")
	os.WriteFile(conf, []byte("root="+root+"\ndevice=/dev/video0\nunsandboxed=yes\n"), 0o644)
	joinConf, joinConfOwner = conf, os.Getuid()
	t.Cleanup(func() { joinConf, joinConfOwner = "/etc/xhaos-node/join.conf", 0 })

	dir := stick(t, "server="+srv.URL+"\ncode=ABCD-EFGH-JKMN\n")
	if !applyDir(context.Background(), dir, state) {
		t.Fatalf("result:\n%s", read(t, filepath.Join(dir, usbconf.ResultName)))
	}
	f := <-joined
	if f.Root != root || strings.Join(f.Devices, ",") != "/dev/video0" || !f.Unsandbox {
		t.Errorf("joined with %+v", f)
	}
	c, _, err := client.Load(state)
	if err != nil || c.Root != root || strings.Join(c.Devices, ",") != "/dev/video0" || !c.Unsandboxed || c.Node != 9 {
		t.Errorf("node.json %+v: %v", c, err)
	}

	// A join.conf someone else could have written is refused.
	os.Remove(filepath.Join(state, "node.json"))
	os.Remove(filepath.Join(state, "node.key"))
	os.Chmod(conf, 0o666)
	dir = stick(t, "server="+srv.URL+"\ncode=ABCD-EFGH-JKMN\n")
	if applyDir(context.Background(), dir, state) {
		t.Fatal("a join.conf others can write was used")
	}
	if res := read(t, filepath.Join(dir, usbconf.ResultName)); !strings.Contains(res, "refusing it") || !strings.Contains(res, "Nothing was changed") {
		t.Errorf("result:\n%s", res)
	}
}
