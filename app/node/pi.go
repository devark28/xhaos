package node

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

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

// piStamp is the line both first-boot files carry: flash-node.sh refuses a
// pair whose ids differ (one is left over from another node) or whose code
// has expired.
func piStamp(id string, name string, expires time.Time) string {
	return fmt.Sprintf("# xhaos-node-files id=%s expires=%d node=%s (the join code expires %s)\n",
		id, expires.Unix(), name, expires.UTC().Format("2006-01-02 15:04 UTC"))
}

// piCleanup runs after the install. Once the node has joined and been
// welcomed by the server (xhaos-node writes its "connected" file), the Wi-Fi
// password and the join code leave the card's boot partition, and cloud-init
// is turned off so it does not take the missing files for a new machine (new
// SSH host keys) on the next boot; the network stays, as netplan renders
// cloud-init's saved config on every boot by itself. The copies of the
// password that remain are made root-only. If the node never connects, the
// files stay, and the log says how to remove them.
const piCleanup = `log() { logger -t xhaos-node "$*" 2>/dev/null || true; echo "xhaos-node: $*"; }
i=0; while [ ! -f /var/lib/xhaos-node/connected ] && [ $i -lt 60 ]; do sleep 5; i=$((i+1)); done
if [ ! -f /var/lib/xhaos-node/connected ]; then
  log "the node has not connected to the server, so user-data and network-config stay on the card's boot partition (they hold the Wi-Fi password and the join code). Remove them by hand: sudo rm -f /boot/firmware/user-data /boot/firmware/network-config"
  exit 0
fi
for d in /boot/firmware /boot; do rm -f "$d/user-data" "$d/network-config"; done
touch /etc/cloud/cloud-init.disabled
chmod go-rwx /etc/netplan/*.yaml /etc/NetworkManager/system-connections/* /run/NetworkManager/system-connections/* 2>/dev/null || true
sync
log "the node is connected: deleted user-data and network-config from the boot partition and turned cloud-init off"`

func piFiles(p PiSetup, url, code, version, id string, expires time.Time) []File {
	stamp := piStamp(id, p.Hostname, expires)
	var u strings.Builder
	u.WriteString("#cloud-config\n")
	u.WriteString(stamp)
	u.WriteString("# Made by Xhaos for node " + q(p.Hostname) + ". Holds a one-time join code: it works once, for 1 hour.\n")
	u.WriteString("# Once the node has joined, it deletes this file and network-config from the card.\n")
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
	args := installArgs(url, code, version)
	if p.UsbConfig {
		args += " --usb-config"
	}
	install := fmt.Sprintf("for i in $(seq 1 60); do curl -fsSL %s | sh -s -- %s && break; sleep 10; done", ScriptURL("install-node.sh", version), args)
	u.WriteString("\nruncmd:\n")
	fmt.Fprintf(&u, "  - [sh, -c, %s]\n", q(install))
	fmt.Fprintf(&u, "  - [sh, -c, %s]\n", q(piCleanup))

	// Always written: an older network-config left in the downloads folder
	// (another network's password) must never go on the card instead.
	var n strings.Builder
	n.WriteString(stamp)
	n.WriteString("network:\n  version: 2\n")
	n.WriteString("  ethernets:\n    renderer: NetworkManager\n    wired:\n      match:\n        name: \"e*\"\n      dhcp4: true\n      optional: true\n")
	if p.WifiSSID != "" {
		n.WriteString("  wifis:\n    renderer: NetworkManager\n    wlan0:\n      dhcp4: true\n")
		fmt.Fprintf(&n, "      regulatory-domain: %s\n", q(p.Country))
		n.WriteString("      access-points:\n")
		fmt.Fprintf(&n, "        %s:\n", q(p.WifiSSID))
		if p.WifiPassword != "" {
			fmt.Fprintf(&n, "          password: %s\n", q(p.WifiPassword))
		}
		n.WriteString("      optional: true\n")
	}
	return []File{{Name: "user-data", Content: u.String()}, {Name: "network-config", Content: n.String()}}
}
