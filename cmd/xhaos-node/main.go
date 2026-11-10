// Command xhaos-node joins this machine to an Xhaos server as a node, so the
// server's agents can run commands and copy files here (docs/nodes.md).
//
//	xhaos-node join --server https://nodes.example.com --code XXXX-XXXX-XXXX
//	xhaos-node run
//	xhaos-node status
//	xhaos-node leave
//	xhaos-node apply-usb /dev/sda1   (as root, from install-node.sh's udev rule)
//
// It dials out to the server and keeps one WebSocket open: the machine needs
// no open port. Commands run inside the node's folder, under Landlock.
// install-node.sh installs it as a systemd service of its own user.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/devark28/xhaos/app/node/client"
	"github.com/devark28/xhaos/app/node/wire"
	"github.com/devark28/xhaos/app/sandbox"
)

// exitRefused is the exit code when the server refused this node.
const exitRefused = 3

// version is set by release builds (-ldflags -X main.version=…).
var version = "0.1.0-dev"

const usage = `xhaos-node: this machine as a node of an Xhaos server

  xhaos-node join --server URL --code CODE [--root DIR] [--device PATH]... [--unsandboxed]
  xhaos-node run       serve the server (what the systemd service runs)
  xhaos-node status    what this node is joined to, and its sandbox
  xhaos-node leave     forget the server: delete this node's key
  xhaos-node apply-usb DEVICE
                       apply an XHAOS USB stick's xhaos-node.conf (as root;
                       install-node.sh's udev rule runs it when one is plugged in)
  xhaos-node version

Every command takes --state DIR (default: $XHAOS_NODE_STATE, else
/var/lib/xhaos-node when it exists, else ~/.local/state/xhaos-node).
`

func main() {
	sandbox.MaybeRun() // started as the command sandbox helper: never returns
	log.SetFlags(0)
	log.SetPrefix("xhaos-node: ")
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "join":
		err = join(args)
	case "run":
		err = run(args)
	case "status":
		err = status(args)
	case "leave":
		err = leave(args)
	case "apply-usb":
		err = applyUSB(args)
	case "version", "--version", "-version":
		fmt.Println(version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		log.Fatal(err)
	}
}

func defaultState() string {
	if s := os.Getenv("XHAOS_NODE_STATE"); s != "" {
		return s
	}
	if fi, err := os.Stat("/var/lib/xhaos-node"); err == nil && fi.IsDir() {
		return "/var/lib/xhaos-node"
	}
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "xhaos-node")
}

type list []string

func (l *list) String() string     { return strings.Join(*l, ",") }
func (l *list) Set(v string) error { *l = append(*l, v); return nil }

func flags(name string) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	state := fs.String("state", defaultState(), "folder for this node's key and settings")
	return fs, state
}

func join(args []string) error {
	fs, state := flags("join")
	server := fs.String("server", "", "the Xhaos server's public address (https://…)")
	code := fs.String("code", "", "the one-time join code from the Nodes tab")
	root := fs.String("root", "", "the folder agents work in (default: ~/xhaos-work of the user running it)")
	unsandboxed := fs.Bool("unsandboxed", false, "run commands without Landlock (only for kernels without it; agents can then reach everything this user can)")
	var devices list
	fs.Var(&devices, "device", "a device commands may open, e.g. /dev/video0 (repeat for more)")
	_ = fs.Parse(args)
	if *server == "" || *code == "" {
		return errors.New("join needs --server and --code (both are in the command the Nodes tab shows)")
	}
	if *root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		*root = filepath.Join(home, "xhaos-work")
	}
	abs, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	for i, d := range devices {
		if !strings.HasPrefix(d, "/dev/") {
			return fmt.Errorf("--device %s: a device is a path under /dev", d)
		}
		devices[i] = filepath.Clean(d)
	}
	if !*unsandboxed && sandbox.ABI() == 0 {
		log.Printf("warning: this kernel has no Landlock, so this node will refuse to run commands; join with --unsandboxed to allow them unconfined")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	c, err := client.Join(ctx, *state, client.Config{Server: strings.TrimRight(*server, "/"), Root: abs, Devices: devices, Unsandboxed: *unsandboxed}, *code, version)
	if err != nil {
		return err
	}
	fmt.Printf("joined %s as %s (node %d); agents work in %s\n", c.Server, c.Name, c.Node, c.Root)
	fmt.Println("start serving with: xhaos-node run (install-node.sh sets it up as a service)")
	return nil
}

func run(args []string) error {
	fs, state := flags("run")
	_ = fs.Parse(args)
	c, key, err := client.Load(*state)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	n := &client.Node{Config: c, Key: key, Version: version, State: *state, Log: log.New(os.Stderr, "xhaos-node: ", 0)}
	if err := n.Run(ctx); client.Refused(err) {
		// The service does not restart on this code (install-node.sh).
		log.Print(err)
		os.Exit(exitRefused)
	} else if err != nil {
		return err
	}
	return nil
}

func status(args []string) error {
	fs, state := flags("status")
	_ = fs.Parse(args)
	c, _, err := client.Load(*state)
	if err != nil {
		return err
	}
	f := client.Facts(c, version)
	fmt.Printf("server:   %s\nnode:     %s (#%d)\nfolder:   %s\nversion:  %s (protocol %d)\n", c.Server, c.Name, c.Node, c.Root, version, wire.Protocol)
	switch {
	case c.Unsandboxed:
		fmt.Println("sandbox:  off (--unsandboxed): commands run as this user, unconfined")
	case f.Landlock == 0:
		fmt.Println("sandbox:  no Landlock in this kernel: commands are refused (join with --unsandboxed to allow them)")
	default:
		fmt.Printf("sandbox:  Landlock ABI %d\n", f.Landlock)
	}
	if len(c.Devices) > 0 {
		fmt.Printf("devices:  %s\n", strings.Join(c.Devices, ", "))
	}
	return nil
}

func leave(args []string) error {
	fs, state := flags("leave")
	_ = fs.Parse(args)
	if err := client.Leave(*state); err != nil {
		return err
	}
	fmt.Println("left: this node's key is deleted. Stop its service (sudo systemctl disable --now xhaos-node) and remove it from the Nodes tab.")
	return nil
}
