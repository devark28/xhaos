package usbconf

import (
	"strings"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	s, err := Parse([]byte("\xef\xbb\xbf# lab\r\nwifi_ssid = Lab 5G\r\nWIFI_PASSWORD=\"  pass#word \"\r\nwifi_country=rw\nwifi_hidden=yes\nserver=https://x.example.com/\ncode=abcd-efgh-jkmn\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := Settings{WifiSSID: "Lab 5G", WifiPassword: "  pass#word ", WifiCountry: "RW", WifiHidden: true, Server: "https://x.example.com", Code: "ABCD-EFGH-JKMN"}
	if s != want {
		t.Fatalf("got %+v", s)
	}
	if s, err := Parse([]byte("# nothing\n\n#wifi_ssid=Lab\n")); err != nil || !s.Empty() {
		t.Fatalf("comments only: %+v %v", s, err)
	}
	for conf, why := range map[string]string{
		"wifi_ssid=a\nwifi_ssid=b\n":                      "set twice",
		"wifi_sid=a\n":                                    "unknown setting",
		"wifi_password=12345678\n":                        "need wifi_ssid",
		"wifi_ssid=a\nwifi_password=short\n":              "8 to 63",
		"wifi_ssid=a\nwifi_password=caf\u00e9caf\u00e9\n": "printable ASCII",
		"wifi_ssid=a\nwifi_country=RWA\n":                 "two-letter",
		"server=https://x.example.com\n":                  "server needs code",
		"server=http://x.example.com\ncode=A\n":           "https://",
		"wifi_ssid=a\nwifi_hidden=maybe\n":                "yes or no",
		"just words\n":                                    "not a setting",
		"wifi_ssid=" + strings.Repeat("x", 33):            "32 bytes",
	} {
		if _, err := Parse([]byte(conf)); err == nil || !strings.Contains(err.Error(), why) {
			t.Errorf("%q: %v, want %q", conf, err, why)
		}
	}
	hex := strings.Repeat("ab", 32)
	if _, err := Parse([]byte("wifi_ssid=a\nwifi_password=" + hex + "\n")); err != nil {
		t.Errorf("64 hex digits: %v", err)
	}
}

func TestApplied(t *testing.T) {
	raw := []byte("# my stick\nwifi_ssid=Lab\nwifi_password=correct horse\nwifi password is hunter22\ncode=ABCD-EFGH-JKMN\n")
	at := time.Date(2026, 11, 9, 22, 30, 0, 0, time.UTC)
	out := string(Applied(raw, at))
	for _, gone := range []string{"correct horse", "ABCD", "hunter22"} {
		if strings.Contains(out, gone) {
			t.Errorf("%q left on the stick:\n%s", gone, out)
		}
	}
	for _, kept := range []string{"# my stick\n", "#wifi_ssid=Lab\n", "#wifi_password=", "2026-11-09 22:30 UTC"} {
		if !strings.Contains(out, kept) {
			t.Errorf("lacks %q:\n%s", kept, out)
		}
	}
	if s, err := Parse([]byte(out)); err != nil || !s.Empty() {
		t.Fatalf("an applied file applies nothing: %+v %v", s, err)
	}
	// Applying again does not stack headers.
	if again := string(Applied([]byte(out), at)); again != out {
		t.Fatalf("not stable:\n%s\n---\n%s", out, again)
	}
}

func TestConnection(t *testing.T) {
	id, file, content := Settings{WifiSSID: "Lab \"5G\";x", WifiPassword: ` \pass word`, WifiHidden: true}.Connection()
	if id != "xhaos-usb-Lab__5G__x" || file != id+".nmconnection" {
		t.Fatalf("id %q file %q", id, file)
	}
	c := string(content)
	for _, want := range []string{"id=" + id + "\n", "type=wifi\n", "ssid=76;97;98;32;34;53;71;34;59;120;\n", "hidden=true\n", "key-mgmt=wpa-psk\n", `psk=\s\\pass word` + "\n", "autoconnect-priority=10\n"} {
		if !strings.Contains(c, want) {
			t.Errorf("lacks %q:\n%s", want, c)
		}
	}
	_, _, open := Settings{WifiSSID: "Cafe"}.Connection()
	if strings.Contains(string(open), "wifi-security") {
		t.Errorf("an open network has no security section:\n%s", open)
	}
}
