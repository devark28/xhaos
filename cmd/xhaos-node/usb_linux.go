//go:build linux

package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/devark28/xhaos/app/node/client"
	"github.com/devark28/xhaos/app/node/usbconf"
)

// applyUSB applies an XHAOS stick's xhaos-node.conf (new Wi-Fi, joining
// again) and writes what happened back to the stick. It runs as root, from
// the xhaos-node-usb@ service that install-node.sh's udev rule starts when
// such a stick is plugged in; the node itself never runs as root.
func applyUSB(args []string) error {
	fs, state := flags("apply-usb")
	_ = fs.Parse(args)
	if fs.NArg() != 1 || !strings.HasPrefix(fs.Arg(0), "/dev/") {
		return errors.New("apply-usb needs the stick's partition, e.g. /dev/sda1 (the udev rule passes it)")
	}
	if os.Geteuid() != 0 {
		return errors.New("apply-usb runs as root (the xhaos-node-usb service does)")
	}
	dev := filepath.Clean(fs.Arg(0))
	dir, err := os.MkdirTemp("/run", "xhaos-node-usb-")
	if err != nil {
		return err
	}
	defer os.Remove(dir)
	if err := mountStick(dev, dir); err != nil {
		return err
	}
	defer func() {
		unix.Sync()
		if err := unix.Unmount(dir, 0); err != nil {
			logf("unmounting %s: %v", dev, err)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	if !applyDir(ctx, dir, *state) {
		return errors.New("the stick's settings were not all applied (see " + usbconf.ResultName + " on it)")
	}
	return nil
}

// applyDir applies the stick mounted at dir and writes its result file; it
// reports whether everything worked.
func applyDir(ctx context.Context, dir, state string) bool {
	r := &report{}
	r.line("xhaos-node %s, %s", version, time.Now().UTC().Format("2006-01-02 15:04:05 UTC"))
	confPath := filepath.Join(dir, usbconf.FileName)
	raw, err := readSmall(confPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		r.line("No %s on the stick: nothing applied, this file only reports.", usbconf.FileName)
	case err != nil:
		r.fail("reading %s: %v", usbconf.FileName, err)
	default:
		s, err := usbconf.Parse(raw)
		if err != nil {
			r.fail("%s: %v. Nothing was applied.", usbconf.FileName, err)
		} else if s.Empty() {
			r.line("%s has no settings (it was applied before?): nothing applied, this file only reports.", usbconf.FileName)
		} else {
			if s.WifiSSID != "" {
				applyWifi(ctx, r, s)
			}
			if s.Code != "" {
				rejoin(ctx, r, state, s)
			}
		}
		// Whatever happened, the password and the code leave the stick.
		if err := writeAtomic(confPath, usbconf.Applied(raw, time.Now())); err != nil {
			r.fail("could not remove the secrets from %s: %v. Delete the file yourself.", usbconf.FileName, err)
		}
	}
	describeNode(r, state)
	if err := writeAtomic(filepath.Join(dir, usbconf.ResultName), []byte(r.String())); err != nil {
		logf("writing %s: %v", usbconf.ResultName, err)
		return false
	}
	return !r.failed
}

// report collects the result file; every line also goes to the journal.
type report struct {
	b      strings.Builder
	failed bool
}

func (r *report) line(format string, a ...any) {
	s := fmt.Sprintf(format, a...)
	logf("%s", s)
	r.b.WriteString(s + "\n")
}

func (r *report) fail(format string, a ...any) {
	r.failed = true
	r.line("ERROR: "+format, a...)
}

func (r *report) String() string { return r.b.String() }

func logf(format string, a ...any) { fmt.Fprintf(os.Stderr, "xhaos-node: "+format+"\n", a...) }

// mountStick mounts a FAT or exFAT stick where nothing on it can run.
func mountStick(dev, dir string) error {
	var errs []string
	for _, fsType := range []string{"vfat", "exfat"} {
		err := unix.Mount(dev, dir, fsType, unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC|unix.MS_NOATIME, "umask=0077")
		if err == nil {
			return nil
		}
		errs = append(errs, fsType+": "+err.Error())
	}
	return fmt.Errorf("mounting %s: %s (the stick should be FAT32 or exFAT, labelled %s)", dev, strings.Join(errs, "; "), usbconf.Label)
}

func readSmall(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New("not a file")
	}
	return io.ReadAll(io.LimitReader(f, usbconf.MaxSize+1))
}

func writeAtomic(path string, content []byte) error {
	tmp := path + ".new"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := f.Write(content); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func command(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// ---- Wi-Fi -----------------------------------------------------------------

const nmConnections = "/etc/NetworkManager/system-connections"

func applyWifi(ctx context.Context, r *report, s usbconf.Settings) {
	if _, err := exec.LookPath("nmcli"); err != nil {
		r.fail("Wi-Fi: this system has no NetworkManager (nmcli); Wi-Fi was not changed")
		return
	}
	if c := s.WifiCountry; c != "" {
		if _, err := exec.LookPath("raspi-config"); err == nil {
			if out, err := command(ctx, "raspi-config", "nonint", "do_wifi_country", c); err != nil {
				r.fail("Wi-Fi country %s: %v %s", c, err, out)
			} else {
				r.line("Wi-Fi country set to %s.", c)
			}
		} else if out, err := command(ctx, "iw", "reg", "set", c); err != nil {
			r.fail("Wi-Fi country %s: %v %s", c, err, out)
		} else {
			r.line("Wi-Fi country set to %s until the next restart (no raspi-config here to keep it).", c)
		}
	}
	id, file, content := s.Connection()
	if err := os.MkdirAll(nmConnections, 0o700); err != nil {
		r.fail("Wi-Fi: %v", err)
		return
	}
	path := filepath.Join(nmConnections, file)
	if err := os.WriteFile(path+".new", content, 0o600); err != nil {
		r.fail("Wi-Fi: %v", err)
		return
	}
	if err := os.Rename(path+".new", path); err != nil {
		r.fail("Wi-Fi: %v", err)
		return
	}
	if out, err := command(ctx, "nmcli", "connection", "reload"); err != nil {
		r.fail("Wi-Fi: nmcli connection reload: %v %s", err, out)
		return
	}
	r.line("Wi-Fi network %q saved (connection %s, preferred over the others).", s.WifiSSID, id)
	upCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if out, err := command(upCtx, "nmcli", "--wait", "45", "connection", "up", "id", id); err != nil {
		// Not an error of the stick: the network may be out of reach now.
		r.line("Could not connect to %q now (%s); the node connects when it is in range, and stays on its other networks meanwhile.", s.WifiSSID, firstLine(out, err))
	} else {
		r.line("Connected to %q.", s.WifiSSID)
	}
}

func firstLine(out string, err error) string {
	if out == "" {
		return err.Error()
	}
	l, _, _ := strings.Cut(out, "\n")
	return l
}

// ---- joining again ---------------------------------------------------------

// rejoin joins with the stick's code. The node's current key stays until the
// new join has succeeded; the node keeps its folder, devices and sandbox mode.
//
// This runs as root, but the state folder and everything in it belong to the
// xhaos-node user, who can run agents' commands: nothing read from there is
// trusted with root's rights. node.json is read without following symlinks,
// its folder is never created or chowned here (the node service makes it),
// and the new files are written beside the old ones with O_NOFOLLOW|O_EXCL,
// handed over with fchown and renamed into place.
func rejoin(ctx context.Context, r *report, state string, s usbconf.Settings) {
	dir, err := openState(state)
	if err != nil {
		r.fail("join: %v (was install-node.sh run here?)", err)
		return
	}
	defer dir.close()
	cur, loadErr := dir.config()
	server := s.Server
	if server == "" {
		if loadErr != nil {
			r.fail("join: this node has not joined a server, so the stick needs server= with the code")
			return
		}
		server = cur.Server
	}
	cfg := client.Config{Server: server, Root: cur.Root, Devices: cur.Devices, Unsandboxed: cur.Unsandboxed}
	if cfg.Root == "" {
		cfg.Root = filepath.Join(state, "xhaos-work")
	}
	if err := dir.checkRoot(cfg.Root); err != nil {
		r.fail("join: %v. Nothing was changed; join by hand (xhaos-node leave, then install-node.sh or xhaos-node join).", err)
		return
	}

	var c client.Config
	var key ed25519.PrivateKey
	for attempt := 1; ; attempt++ {
		c, key, err = client.Enroll(ctx, cfg, s.Code, version)
		if err == nil || client.Refused(err) || attempt == 12 || ctx.Err() != nil {
			break
		}
		logf("join: %v; trying again in 10 s", err) // the network may still be coming up
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Second):
		}
	}
	if err != nil {
		r.fail("join %s: %v. This node keeps its old key.", server, err)
		return
	}
	keyFile, configFile := client.Encode(c, key)
	files := []stateFile{{"node.key", keyFile}, {"node.json", configFile}}
	if err := dir.stage(files); err != nil {
		r.fail("join: saving the new key: %v. This node keeps its old key; the new entry on %s stays offline (remove it there).", err, c.Server)
		return
	}
	// The service install-node.sh made, when there is one.
	systemd := isDir("/run/systemd/system") && exists("/etc/systemd/system/xhaos-node.service")
	if systemd {
		_, _ = command(ctx, "systemctl", "stop", "xhaos-node")
	}
	if err := dir.commit(files); err != nil {
		r.fail("join: saving the new key: %v", err)
		return
	}
	r.line("Joined %s as %s (node %d).", c.Server, c.Name, c.Node)
	if loadErr == nil && (cur.Server != c.Server || cur.Node != c.Node) {
		r.line("The old entry, %s on %s, stays there as offline: remove it on its Nodes tab.", cur.Name, cur.Server)
	}
	if systemd {
		_, _ = command(ctx, "systemctl", "reset-failed", "xhaos-node")
		if out, err := command(ctx, "systemctl", "start", "xhaos-node"); err != nil {
			r.fail("starting xhaos-node: %v %s", err, out)
		}
	}
}

// stateDir is the node's state folder, opened once (not followed if it is a
// symlink); every file in it is reached relative to that descriptor.
type stateDir struct {
	path     string
	fd       int
	uid, gid int
}

type stateFile struct {
	name    string
	content []byte
}

func openState(path string) (*stateDir, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return nil, err
	}
	return &stateDir{path: path, fd: fd, uid: int(st.Uid), gid: int(st.Gid)}, nil
}

func (d *stateDir) close() { unix.Close(d.fd) }

// config reads node.json; a symlink or anything but a small file is refused,
// and errors never quote its content (it might be another file's).
func (d *stateDir) config() (client.Config, error) {
	var c client.Config
	fd, err := unix.Openat(d.fd, "node.json", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return c, fmt.Errorf("node.json: %w", err)
	}
	f := os.NewFile(uintptr(fd), filepath.Join(d.path, "node.json"))
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return c, err
	}
	if !fi.Mode().IsRegular() {
		return c, errors.New("node.json is not a file")
	}
	raw, err := io.ReadAll(io.LimitReader(f, 64<<10))
	if err != nil {
		return c, err
	}
	if json.Unmarshal(raw, &c) != nil {
		return client.Config{}, errors.New("node.json is not a node config")
	}
	return c, nil
}

// checkRoot vets node.json's folder for agents before root joins with it:
// an absolute path that is either a real folder of the node's user, or not
// there yet and inside the state folder (the node service then makes it).
// Anything else (a system folder, a symlink, someone else's) is refused.
func (d *stateDir) checkRoot(root string) error {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return fmt.Errorf("node.json's folder %q is not a clean absolute path", root)
	}
	var st unix.Stat_t
	err := unix.Lstat(root, &st)
	switch {
	case errors.Is(err, unix.ENOENT):
		if !strings.HasPrefix(root, filepath.Clean(d.path)+"/") {
			return fmt.Errorf("node.json's folder %s does not exist and is outside %s", root, d.path)
		}
		return nil
	case err != nil:
		return fmt.Errorf("node.json's folder %s: %w", root, err)
	case st.Mode&unix.S_IFMT != unix.S_IFDIR:
		return fmt.Errorf("node.json's folder %s is not a folder (a symlink?)", root)
	case int(st.Uid) != d.uid:
		return fmt.Errorf("node.json's folder %s does not belong to the node's user (uid %d)", root, d.uid)
	}
	return nil
}

func stagedName(name string) string { return "." + name + ".rejoin" }

// stage writes each file beside its final name, owned by the node's user. A
// leftover (or planted) staged name is removed first; O_EXCL|O_NOFOLLOW then
// makes sure the file written is the one just created, never a link's target.
func (d *stateDir) stage(files []stateFile) error {
	for _, f := range files {
		if err := d.stageOne(f); err != nil {
			d.unstage(files)
			return fmt.Errorf("%s: %w", f.name, err)
		}
	}
	return nil
}

func (d *stateDir) stageOne(f stateFile) error {
	tmp := stagedName(f.name)
	if err := unix.Unlinkat(d.fd, tmp, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	fd, err := unix.Openat(d.fd, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), tmp)
	defer file.Close()
	if err := unix.Fchown(fd, d.uid, d.gid); err != nil {
		return err
	}
	if _, err := file.Write(f.content); err != nil {
		return err
	}
	return file.Sync()
}

func (d *stateDir) unstage(files []stateFile) {
	for _, f := range files {
		_ = unix.Unlinkat(d.fd, stagedName(f.name), 0)
	}
}

// commit renames the staged files over the old ones (a rename replaces a
// symlink, it does not write through it).
func (d *stateDir) commit(files []stateFile) error {
	for _, f := range files {
		if err := unix.Renameat(d.fd, stagedName(f.name), d.fd, f.name); err != nil {
			d.unstage(files)
			return fmt.Errorf("%s: %w", f.name, err)
		}
	}
	return unix.Fsync(d.fd)
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// ---- what the node looks like now ------------------------------------------

func describeNode(r *report, state string) {
	r.line("")
	host, _ := os.Hostname()
	r.line("Hostname: %s", host)
	if addrs := addresses(); len(addrs) > 0 {
		r.line("Addresses: %s", strings.Join(addrs, ", "))
	} else {
		r.line("Addresses: none (no network yet)")
	}
	if out, err := command(context.Background(), "nmcli", "-t", "-f", "NAME,DEVICE", "connection", "show", "--active"); err == nil && out != "" {
		r.line("Active connections: %s", strings.ReplaceAll(out, "\n", ", "))
	}
	if c, err := nodeConfig(state); err == nil {
		r.line("Node: %s (#%d) of %s", c.Name, c.Node, c.Server)
	} else {
		r.line("Node: not joined")
	}
	if isDir("/run/systemd/system") && exists("/etc/systemd/system/xhaos-node.service") {
		out, _ := command(context.Background(), "systemctl", "is-active", "xhaos-node")
		r.line("Service xhaos-node: %s", out)
	}
}

func nodeConfig(state string) (client.Config, error) {
	d, err := openState(state)
	if err != nil {
		return client.Config{}, err
	}
	defer d.close()
	return d.config()
}

func addresses() []string {
	var out []string
	ifaces, _ := net.Interfaces()
	for _, i := range ifaces {
		if i.Flags&net.FlagLoopback != 0 || i.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			ip, _, err := net.ParseCIDR(a.String())
			if err != nil || ip.IsLinkLocalUnicast() {
				continue
			}
			out = append(out, i.Name+" "+ip.String())
		}
	}
	return out
}
