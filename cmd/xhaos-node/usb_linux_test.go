//go:build linux

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devark28/xhaos/app/node/client"
	"github.com/devark28/xhaos/app/node/usbconf"
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
	if _, err := os.Stat(filepath.Join(state, ".rejoin")); !os.IsNotExist(err) {
		t.Errorf("the temporary join folder is left: %v", err)
	}
	t.Log(res)
}
