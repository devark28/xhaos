package node

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/devark28/xhaos/app/apperr"
)

// Raspberry Pi first-boot files: Raspberry Pi OS (Trixie images from
// 24 November 2025 on) runs cloud-init from the boot partition's user-data
// and network-config. Copied over the ones on a freshly flashed card, they
// set the hostname, a user (SSH key login only), Wi-Fi, and install
// xhaos-node with the join code once the network is up.

var (
	hostRE    = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	userRE    = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	countryRE = regexp.MustCompile(`^[A-Z]{2}$`)
	sshKeyRE  = regexp.MustCompile(`^(ssh-(ed25519|rsa)|ecdsa-sha2-nistp(256|384|521)|sk-(ssh-ed25519|ecdsa-sha2-nistp256)@openssh\.com) [A-Za-z0-9+/=]+( [^\r\n]*)?$`)
)

func (p *PiSetup) check() error {
	bad := func(why string) error { return fmt.Errorf("%w: %s", apperr.Invalid, why) }
	p.Hostname = strings.ToLower(strings.TrimSpace(p.Hostname))
	p.User = strings.TrimSpace(p.User)
	if p.User == "" {
		p.User = "pi"
	}
	p.SSHKey = strings.TrimSpace(p.SSHKey)
	p.Country = strings.ToUpper(strings.TrimSpace(p.Country))
	switch {
	case !hostRE.MatchString(p.Hostname):
		return bad("the hostname is letters, digits and dashes (e.g. pi5)")
	case !userRE.MatchString(p.User) || p.User == "root":
		return bad("the user name is lower-case letters, digits, dashes or underscores, and not root")
	case p.SSHKey != "" && !sshKeyRE.MatchString(p.SSHKey):
		return bad("the SSH key is one public key line (ssh-ed25519 AAAA… comment)")
	case p.WifiSSID != "" && len(p.WifiSSID) > 32:
		return bad("a Wi-Fi name is at most 32 bytes")
	case p.WifiSSID != "" && p.WifiPassword != "" && (len(p.WifiPassword) < 8 || len(p.WifiPassword) > 63):
		return bad("a WPA Wi-Fi password is 8 to 63 characters")
	case p.WifiSSID != "" && !countryRE.MatchString(p.Country):
		return bad("Wi-Fi needs the country the Pi is used in, as a two-letter code (e.g. GB, RW, US)")
	}
	return nil
}

// q quotes a YAML string (a JSON string is a YAML double-quoted scalar),
// keeping & < > as they are so the commands stay readable.
func q(s string) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(b.String(), "\n")
}

func piFiles(p PiSetup, url, code, version string) []File {
	var u strings.Builder
	u.WriteString("#cloud-config\n")
	u.WriteString("# Made by Xhaos for node " + q(p.Hostname) + ". Holds a one-time join code: it works once, for 1 hour.\n")
	fmt.Fprintf(&u, "hostname: %s\nmanage_etc_hosts: true\n\n", q(p.Hostname))
	u.WriteString("users:\n")
	fmt.Fprintf(&u, "  - name: %s\n", q(p.User))
	u.WriteString("    groups: users,adm,dialout,audio,netdev,video,plugdev,cdrom,games,input,gpio,spi,i2c,render,sudo\n")
	u.WriteString("    shell: /bin/bash\n")
	u.WriteString("    lock_passwd: true\n")
	u.WriteString("    sudo: ALL=(ALL) NOPASSWD:ALL\n")
	if p.SSHKey != "" {
		fmt.Fprintf(&u, "    ssh_authorized_keys:\n      - %s\n", q(p.SSHKey))
		u.WriteString("\nenable_ssh: true\nssh_pwauth: false\n")
	}
	// The network may come up after runcmd starts: retry for ten minutes.
	install := fmt.Sprintf("for i in $(seq 1 60); do curl -fsSL %s | sh -s -- %s && break; sleep 10; done", InstallScriptURL, installArgs(url, code, version))
	u.WriteString("\nruncmd:\n")
	fmt.Fprintf(&u, "  - [sh, -c, %s]\n", q(install))

	files := []File{{Name: "user-data", Content: u.String()}}
	if p.WifiSSID != "" {
		var n strings.Builder
		n.WriteString("network:\n  version: 2\n  wifis:\n    renderer: NetworkManager\n    wlan0:\n      dhcp4: true\n")
		fmt.Fprintf(&n, "      regulatory-domain: %s\n", q(p.Country))
		n.WriteString("      access-points:\n")
		fmt.Fprintf(&n, "        %s:\n", q(p.WifiSSID))
		if p.WifiPassword != "" {
			fmt.Fprintf(&n, "          password: %s\n", q(p.WifiPassword))
		}
		n.WriteString("      optional: true\n")
		files = append(files, File{Name: "network-config", Content: n.String()})
	}
	return files
}
