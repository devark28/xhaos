// Package usbconf reads the settings file of an XHAOS USB stick: Wi-Fi and a
// join code that a node applies when the stick is plugged in, so a node
// without a screen or SSH can be moved to another network or joined again
// (docs/nodes.md, "USB stick config").
//
// The file, xhaos-node.conf, has one setting per line:
//
//	# lines starting with # are comments
//	wifi_ssid=Lab 5G
//	wifi_password=correct horse battery
//	wifi_country=RW
//	server=https://xhaos.example.com
//	code=ABCD-EFGH-JKMN
//
// A value is taken without the spaces around it; put it in double quotes to
// keep them ("  pass  "). After the node has applied it, every setting is
// commented out and the password and the code are removed (Applied).
package usbconf

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// Labels the stick's file system may have (FAT or exFAT).
	Label = "XHAOS"
	// FileName is the settings file at the stick's top level.
	FileName = "xhaos-node.conf"
	// ResultName is what the node writes back: what it did and how to reach it.
	ResultName = "xhaos-node-result.txt"
	// MaxSize is the largest settings file read.
	MaxSize = 64 << 10
)

// Settings is a parsed xhaos-node.conf. Empty fields are not changed.
type Settings struct {
	WifiSSID     string
	WifiPassword string // empty: an open network
	WifiCountry  string // ISO 3166 code, upper case
	WifiHidden   bool
	Server       string
	Code         string
}

// Empty reports whether there is nothing to apply (only a result to write).
func (s Settings) Empty() bool { return s.WifiSSID == "" && s.Code == "" }

// Keys are the settings a file may have.
var Keys = []string{"wifi_ssid", "wifi_password", "wifi_country", "wifi_hidden", "server", "code"}

// secret settings are removed from the stick once applied.
func secret(key string) bool { return key == "wifi_password" || key == "code" }

func known(key string) bool {
	for _, k := range Keys {
		if k == key {
			return true
		}
	}
	return false
}

// line splits one line into a setting; ok is false for blanks and comments.
func line(raw string) (key, value string, ok bool, err error) {
	t := strings.TrimSpace(raw)
	if t == "" || strings.HasPrefix(t, "#") {
		return "", "", false, nil
	}
	k, v, found := strings.Cut(t, "=")
	if !found {
		return "", "", false, fmt.Errorf("%q is not a setting (name=value)", t)
	}
	key = strings.ToLower(strings.TrimSpace(k))
	value = strings.TrimSpace(v)
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		value = value[1 : len(value)-1]
	}
	return key, value, true, nil
}

func lines(raw []byte) []string {
	raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf")) // Notepad's UTF-8 mark
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 4096), MaxSize)
	for sc.Scan() {
		out = append(out, strings.TrimRight(sc.Text(), "\r"))
	}
	return out
}

// Parse reads and checks a settings file. Any mistake rejects the whole file,
// so a stick is applied completely or not at all.
func Parse(raw []byte) (Settings, error) {
	var s Settings
	if len(raw) > MaxSize {
		return s, fmt.Errorf("%s is larger than %d KB", FileName, MaxSize>>10)
	}
	seen := map[string]bool{}
	for i, l := range lines(raw) {
		key, value, ok, err := line(l)
		if err != nil {
			return s, fmt.Errorf("line %d: %w", i+1, err)
		}
		if !ok {
			continue
		}
		if !known(key) {
			return s, fmt.Errorf("line %d: unknown setting %q (known: %s)", i+1, key, strings.Join(Keys, ", "))
		}
		if seen[key] {
			return s, fmt.Errorf("line %d: %s is set twice", i+1, key)
		}
		seen[key] = true
		switch key {
		case "wifi_ssid":
			s.WifiSSID = value
		case "wifi_password":
			s.WifiPassword = value
		case "wifi_country":
			s.WifiCountry = strings.ToUpper(value)
		case "wifi_hidden":
			switch strings.ToLower(value) {
			case "yes", "true", "1":
				s.WifiHidden = true
			case "no", "false", "0", "":
			default:
				return s, fmt.Errorf("line %d: wifi_hidden is yes or no", i+1)
			}
		case "server":
			s.Server = strings.TrimRight(value, "/")
		case "code":
			s.Code = strings.ToUpper(value)
		}
	}
	return s, s.check(seen)
}

func (s Settings) check(seen map[string]bool) error {
	if s.WifiSSID == "" && (seen["wifi_password"] || seen["wifi_country"] || seen["wifi_hidden"]) {
		return errors.New("Wi-Fi settings need wifi_ssid")
	}
	if len(s.WifiSSID) > 32 {
		return errors.New("wifi_ssid is longer than 32 bytes")
	}
	if p := s.WifiPassword; p != "" {
		hex := len(p) == 64 && strings.Trim(strings.ToLower(p), "0123456789abcdef") == ""
		if !hex && (len(p) < 8 || len(p) > 63) {
			return errors.New("wifi_password has 8 to 63 characters (WPA2/WPA3), or leave it out for an open network")
		}
		for _, r := range p {
			if r < 0x20 || r > 0x7e {
				return errors.New("wifi_password can only have printable ASCII characters")
			}
		}
	}
	if c := s.WifiCountry; c != "" && (len(c) != 2 || strings.Trim(c, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "") {
		return errors.New("wifi_country is a two-letter country code, e.g. GB or RW")
	}
	if s.Server != "" && s.Code == "" {
		return errors.New("server needs code: a fresh join code from Nodes → Add node or New code")
	}
	if s.Server != "" && !strings.HasPrefix(s.Server, "https://") && !strings.HasPrefix(s.Server, "http://localhost") && !strings.HasPrefix(s.Server, "http://127.0.0.1") {
		return errors.New("server is an https:// address")
	}
	return nil
}

// Applied is the file to leave on the stick after applying it: every setting
// commented out, so plugging the stick in again only writes a new result, and
// the password and the code removed.
func Applied(raw []byte, at time.Time) []byte {
	var b strings.Builder
	header := []string{
		"# Applied by the node on " + at.UTC().Format("2006-01-02 15:04 UTC") + "; the settings below are commented out",
		"# so plugging this stick in again only writes a new " + ResultName + ".",
		"# Remove the # in front of a line (and fill in what was removed) to apply it again.",
	}
	for _, h := range header {
		b.WriteString(h + "\n")
	}
	old := func(l string) bool { // an earlier header
		for _, h := range header[1:] {
			if l == h {
				return true
			}
		}
		return strings.HasPrefix(l, "# Applied by the node on ")
	}
	for _, l := range lines(raw) {
		if old(strings.TrimSpace(l)) {
			continue
		}
		key, _, ok, err := line(l)
		switch {
		case err != nil:
			b.WriteString("#   (a line that was not a setting was removed)")
		case !ok:
			b.WriteString(l)
		case secret(key):
			b.WriteString("#" + key + "=   (removed from the stick after applying)")
		default:
			b.WriteString("#" + strings.TrimSpace(l))
		}
		b.WriteString("\n")
	}
	return []byte(b.String())
}

// Connection is a NetworkManager keyfile for the Wi-Fi network: its
// connection name, file name (in /etc/NetworkManager/system-connections) and
// content (for a file only root can read).
func (s Settings) Connection() (id, file string, content []byte) {
	id = "xhaos-usb-" + slug(s.WifiSSID)
	var b strings.Builder
	fmt.Fprintf(&b, "# Written by xhaos-node from an %s USB stick.\n", Label)
	fmt.Fprintf(&b, "[connection]\nid=%s\nuuid=%s\ntype=wifi\nautoconnect=true\nautoconnect-priority=10\n\n", id, newUUID())
	// The SSID as bytes (65;66;67;): no escaping to get wrong.
	b.WriteString("[wifi]\nmode=infrastructure\nssid=")
	for _, c := range []byte(s.WifiSSID) {
		fmt.Fprintf(&b, "%d;", c)
	}
	b.WriteString("\n")
	if s.WifiHidden {
		b.WriteString("hidden=true\n")
	}
	if s.WifiPassword != "" {
		fmt.Fprintf(&b, "\n[wifi-security]\nkey-mgmt=wpa-psk\npsk=%s\n", keyfileString(s.WifiPassword))
	}
	b.WriteString("\n[ipv4]\nmethod=auto\n\n[ipv6]\nmethod=auto\n")
	return id, id + ".nmconnection", []byte(b.String())
}

// keyfileString escapes a value for a GLib key file.
func keyfileString(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	if strings.HasPrefix(v, " ") {
		v = `\s` + v[1:]
	}
	return v
}

func slug(ssid string) string {
	var b strings.Builder
	for _, r := range ssid {
		if b.Len() >= 32 {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

func newUUID() string {
	var u [16]byte
	_, _ = rand.Read(u[:])
	u[6] = u[6]&0x0f | 0x40
	u[8] = u[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:])
}
