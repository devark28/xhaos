package sandbox

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// uringProbe drives io_uring by hand (no socket(2) call): it opens a UDP
// socket with IORING_OP_SOCKET, connects it with IORING_OP_CONNECT and sends
// a datagram with IORING_OP_SEND. It prints "ERR <call> <errno>" when a
// call fails, else "sent <n>".
const uringProbe = `import ctypes, mmap, socket, struct, sys
libc = ctypes.CDLL(None, use_errno=True)
libc.syscall.restype = ctypes.c_long
params = ctypes.create_string_buffer(120)
fd = libc.syscall(425, 8, params)
if fd < 0:
    print("ERR io_uring_setup", ctypes.get_errno()); sys.exit(0)
sq_entries, cq_entries = struct.unpack_from("II", params, 0)
sq = struct.unpack_from("IIIIIIIIQ", params, 40)
cq = struct.unpack_from("IIIIIIIIQ", params, 80)
sqr = mmap.mmap(fd, sq[6] + sq_entries * 4, mmap.MAP_SHARED, mmap.PROT_READ | mmap.PROT_WRITE, offset=0)
cqr = mmap.mmap(fd, cq[5] + cq_entries * 16, mmap.MAP_SHARED, mmap.PROT_READ | mmap.PROT_WRITE, offset=0x8000000)
sqes = mmap.mmap(fd, sq_entries * 64, mmap.MAP_SHARED, mmap.PROT_READ | mmap.PROT_WRITE, offset=0x10000000)
def submit(op, fdv, off, addr, ln, ud):
    tail = struct.unpack_from("I", sqr, sq[1])[0]
    idx = tail & struct.unpack_from("I", sqr, sq[2])[0]
    struct.pack_into("BBHiQQIIQHHiQQ", sqes, idx * 64, op, 0, 0, fdv, off, addr, ln, 0, ud, 0, 0, 0, 0, 0)
    struct.pack_into("I", sqr, sq[6] + idx * 4, idx)
    struct.pack_into("I", sqr, sq[1], (tail + 1) & 0xffffffff)
    if libc.syscall(426, fd, 1, 1, 1, None, 0) != 1:
        return -ctypes.get_errno()
    head = struct.unpack_from("I", cqr, cq[0])[0]
    res = struct.unpack_from("i", cqr, cq[5] + (head & struct.unpack_from("I", cqr, cq[2])[0]) * 16 + 8)[0]
    struct.pack_into("I", cqr, cq[0], head + 1)
    return res
s = submit(45, socket.AF_INET, socket.SOCK_DGRAM, 0, 0, 1)
if s < 0:
    print("ERR socket", -s); sys.exit(0)
addr = struct.pack("<H", socket.AF_INET) + struct.pack(">H", int(sys.argv[1])) + socket.inet_aton("127.0.0.1") + bytes(8)
sa = ctypes.create_string_buffer(addr, len(addr))
r = submit(16, s, len(addr), ctypes.addressof(sa), 0, 2)
if r < 0:
    print("ERR connect", -r); sys.exit(0)
msg = b"SECRET-VIA-URING"
buf = ctypes.create_string_buffer(msg, len(msg))
r = submit(26, s, 0, ctypes.addressof(buf), len(msg), 3)
print("sent", r) if r >= 0 else print("ERR send", -r)
`

// familyProbe prints "denied" when socket(2) gives EACCES or EPERM (the
// filter's answer), "opened" when it succeeds, else the error.
const familyProbe = `import socket, sys
try:
    socket.socket(int(sys.argv[1]), socket.SOCK_STREAM, 0)
    print("opened")
except PermissionError:
    print("denied")
except OSError as e:
    print("error", e.errno)
`

// listenUDP returns a local UDP port and a function that waits up to 2 s
// for one datagram and returns its payload ("" when none came).
func listenUDP(t *testing.T) (int, func() string) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	got := make(chan string, 1)
	go func() {
		pc.SetReadDeadline(time.Now().Add(2 * time.Second))
		buf := make([]byte, 256)
		n, _, err := pc.ReadFrom(buf)
		if err != nil {
			got <- ""
			return
		}
		got <- string(buf[:n])
	}()
	return pc.LocalAddr().(*net.UDPAddr).Port, func() string { return <-got }
}

// runIn runs script (a Python file in dir) with args under the mode: "plain"
// (no restriction), "nonet" (commands the classifier allows) or "sealed"
// (commands with secrets). It returns the command's output.
func runIn(t *testing.T, c Config, dir, mode, script string, args ...string) string {
	t.Helper()
	command := "python3 " + script
	for _, a := range args {
		command += " " + a
	}
	var out []byte
	var err error
	switch mode {
	case "plain":
		x := mustCommand(t, c, dir, command, Extra{})
		out, err = x.CombinedOutput()
	case "nonet":
		x := mustCommand(t, c, dir, command, Extra{NoNet: true})
		out, err = x.CombinedOutput()
	case "sealed":
		x, cleanup, e := c.CommandSealed(context.Background(), dir, command, Sealed{ProxyPort: 1})
		if e != nil {
			t.Fatal(e)
		}
		out, err = x.CombinedOutput()
		cleanup()
	}
	if err != nil {
		t.Logf("%s %s: %v: %s", mode, script, err, out)
	}
	return strings.TrimSpace(string(out))
}

// mustCommand builds a sandboxed command or fails the test.
func mustCommand(t *testing.T, c Config, dir, command string, x Extra) *exec.Cmd {
	t.Helper()
	cmd, err := c.Command(context.Background(), dir, command, x)
	if err != nil {
		t.Fatal(err)
	}
	return cmd
}

// No command may send a datagram to a local listener through io_uring: it
// makes sockets without the socket call the filter checks. io_uring itself
// is refused everywhere, so the probe is refused in every mode.
func TestNoNetBlocksDataThroughIoUring(t *testing.T) {
	// A direct call from this test process: ENOSYS means the kernel has no
	// io_uring at all, so there is nothing to block.
	if _, _, errno := unix.Syscall(425, 0, 0, 0); errno == unix.ENOSYS {
		t.Skip("this kernel has no io_uring")
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "uring.py"), []byte(uringProbe), 0o644)
	os.WriteFile(filepath.Join(dir, "plain.py"), []byte("import socket, sys\ns = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)\ns.sendto(b'SECRET-PLAIN', ('127.0.0.1', int(sys.argv[1])))\n"), 0o644)
	c := Config{Mode: On, Cache: filepath.Join(dir, "c")}

	// Control: a plain datagram reaches the listener without NoNet, and not with it.
	port, got := listenUDP(t)
	runIn(t, c, dir, "plain", "plain.py", strconv.Itoa(port))
	if got() != "SECRET-PLAIN" {
		t.Fatal("control: a plain datagram did not reach the listener, so the test cannot tell")
	}
	port, got = listenUDP(t)
	runIn(t, c, dir, "nonet", "plain.py", strconv.Itoa(port))
	if g := got(); g != "" {
		t.Fatalf("control: NoNet let a plain datagram through: %q", g)
	}

	for _, mode := range []string{"plain", "nonet", "sealed"} {
		port, got := listenUDP(t)
		out := runIn(t, c, dir, mode, "uring.py", strconv.Itoa(port))
		if !strings.HasPrefix(out, "ERR io_uring_setup") {
			t.Errorf("%s: io_uring_setup was not refused: %q", mode, out)
		}
		if g := got(); g != "" {
			t.Errorf("%s: a datagram sent through io_uring reached the listener: %q", mode, g)
		}
	}
}

// Under NoNet and sealed commands, socket families other than TCP over
// IPv4/IPv6 and netlink are refused by the filter, whatever the kernel
// supports (SMC could carry TCP past Landlock). Netlink stays open.
func TestNoNetDeniesOtherFamilies(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "family.py"), []byte(familyProbe), 0o644)
	c := Config{Mode: On, Cache: filepath.Join(dir, "c")}
	for _, fam := range []struct {
		name string
		n    int
	}{{"AF_SMC", 43}, {"AF_VSOCK", 40}, {"AF_ALG", 38}, {"AF_BLUETOOTH", 31}} {
		for _, mode := range []string{"nonet", "sealed"} {
			if got := runIn(t, c, dir, mode, "family.py", strconv.Itoa(fam.n)); got != "denied" {
				t.Errorf("%s: %s socket %q, want denied", mode, fam.name, got)
			}
		}
	}
	if got := runIn(t, c, dir, "nonet", "family.py", "16"); got == "denied" {
		t.Errorf("NoNet: AF_NETLINK socket was denied; it must stay open for interface queries")
	}
}

// netlinkProbe prints "denied" when socket(AF_NETLINK, SOCK_RAW, protocol)
// gives EACCES or EPERM (the filter's answer), "opened" when it succeeds.
const netlinkProbe = `import socket, sys
try:
    socket.socket(socket.AF_NETLINK, socket.SOCK_RAW, int(sys.argv[1]))
    print("opened")
except PermissionError:
    print("denied")
except OSError as e:
    print("error", e.errno)
`

// Under no-network, netlink stays open only for the kernel's route queries
// (NETLINK_ROUTE, protocol 0). Other protocols reach other parts of the
// kernel: NETLINK_USERSOCK (2) talks to user-space daemons.
func TestNoNetNetlinkOnlyRoute(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "netlink.py"), []byte(netlinkProbe), 0o644)
	c := Config{Mode: On, Cache: filepath.Join(dir, "c")}
	for _, mode := range []string{"nonet", "sealed"} {
		if got := runIn(t, c, dir, mode, "netlink.py", "2"); got != "denied" {
			t.Errorf("%s: NETLINK_USERSOCK socket %q, want denied", mode, got)
		}
		if got := runIn(t, c, dir, mode, "netlink.py", "0"); got != "opened" {
			t.Errorf("%s: NETLINK_ROUTE socket %q, want opened", mode, got)
		}
	}
}
