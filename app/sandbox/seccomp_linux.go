package sandbox

import (
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// denySockets installs a seccomp filter on this process and everything it
// starts. It makes socket(AF_UNIX, …) fail with EACCES: Landlock before
// ABI 9 does not cover connecting to socket files, so without this a
// command could reach the user's SSH agent (and push), the keyring, D-Bus
// or the Docker socket by path. socketpair(2), which pipes between a
// process and its children use, still works. With udp, IPv4 and IPv6
// sockets fail too unless they are TCP (SOCK_STREAM with protocol 0 or
// IPPROTO_TCP): a datagram, raw, SCTP or MPTCP socket could carry a secret
// out, and Landlock's TCP rules do not cover them (sealed commands: a DNS
// lookup could; their proxy resolves names for them). Where udp is set, only
// TCP over IPv4 and IPv6 and NETLINK_ROUTE (the kernel's own interface
// queries) may be opened: any other family (SMC, vsock, AF_ALG, packet, ...) could
// reach the network past Landlock's TCP rules.
//
// io_uring (in every mode) is denied outright: it can create, connect and
// send on sockets without a socket(2) call, so the filter cannot see them.
// The kernel keyring calls (add_key, request_key, keyctl) are denied too: a
// command could pass a secret through the keyring to another process.
func denySockets(udp bool) error {
	if sysSocket < 0 {
		return fmt.Errorf("no seccomp filter for %s", runtime.GOARCH)
	}
	const (
		allow  = 0x7fff0000
		denied = unix.SECCOMP_RET_ERRNO | uint32(unix.EACCES)
		// io_uring: ENOSYS, as on a kernel without it, so libraries fall back.
		noURing = unix.SECCOMP_RET_ERRNO | uint32(unix.ENOSYS)
	)
	var a asm
	a.ld(4)                       // arch
	a.jeq(auditArch, "", "deny")  // another ABI (32-bit calls): deny
	a.ld(0)                       // syscall number
	a.jge(0x40000000, "deny", "") // x32 numbers: deny
	a.jeq(sysIoUringSetup, "uring", "")
	a.jeq(sysIoUringEnter, "uring", "")
	a.jeq(sysIoUringRegister, "uring", "")
	a.jeq(uint32(sysAddKey), "deny", "")
	a.jeq(uint32(sysRequestKey), "deny", "")
	a.jeq(uint32(sysKeyctl), "deny", "")
	a.jeq(uint32(sysSocket), "", "allow")
	a.ld(16) // domain (low half of the first argument)
	a.jeq(unix.AF_UNIX, "deny", "")
	if udp {
		a.jeq(unix.AF_INET, "inet", "")
		a.jeq(unix.AF_INET6, "inet", "")
		a.jeq(unix.AF_NETLINK, "netlink", "deny")
		a.label("inet")
		a.ld(24)   // type (low half of the second argument)
		a.and(0xf) // without SOCK_NONBLOCK / SOCK_CLOEXEC
		a.jeq(unix.SOCK_STREAM, "", "deny")
		a.ld(32) // protocol (low half of the third argument)
		a.jeq(0, "allow", "")
		a.jeq(unix.IPPROTO_TCP, "allow", "deny")
		// Netlink: only NETLINK_ROUTE (the kernel's interface and route
		// queries). Other protocols reach user-space daemons (USERSOCK) or
		// other kernel subsystems, so they are denied.
		a.label("netlink")
		a.ld(32) // protocol
		a.jeq(unix.NETLINK_ROUTE, "allow", "deny")
	}
	a.label("allow")
	a.ret(allow)
	a.label("deny")
	a.ret(denied)
	a.label("uring")
	a.ret(noURing)
	prog, err := a.program()
	if err != nil {
		return err
	}
	fprog := unix.SockFprog{Len: uint16(len(prog)), Filter: &prog[0]}
	// Landlock already set no_new_privs, which an unprivileged filter needs.
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return err
	}
	if _, _, errno := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER, unix.SECCOMP_FILTER_FLAG_TSYNC, uintptr(unsafe.Pointer(&fprog))); errno != 0 {
		return fmt.Errorf("seccomp: %w", errno)
	}
	return nil
}

// io_uring's system call numbers: the same on x86-64 and arm64.
const (
	sysIoUringSetup    = 425
	sysIoUringEnter    = 426
	sysIoUringRegister = 427
)

// asm builds a classic BPF program with named jump targets ("" means the
// next instruction), so offsets are worked out rather than counted by hand.
type asm struct {
	ins    []unix.SockFilter
	jumps  map[int][2]string
	labels map[string]int
}

func (a *asm) add(f unix.SockFilter, jt, jf string) {
	if a.jumps == nil {
		a.jumps, a.labels = map[int][2]string{}, map[string]int{}
	}
	if jt != "" || jf != "" {
		a.jumps[len(a.ins)] = [2]string{jt, jf}
	}
	a.ins = append(a.ins, f)
}
func (a *asm) ld(off uint32)               { a.add(unix.SockFilter{Code: 0x20, K: off}, "", "") } // BPF_LD|BPF_W|BPF_ABS
func (a *asm) and(k uint32)                { a.add(unix.SockFilter{Code: 0x54, K: k}, "", "") }   // BPF_ALU|BPF_AND|BPF_K
func (a *asm) ret(k uint32)                { a.add(unix.SockFilter{Code: 0x06, K: k}, "", "") }   // BPF_RET|BPF_K
func (a *asm) jeq(k uint32, jt, jf string) { a.add(unix.SockFilter{Code: 0x15, K: k}, jt, jf) }
func (a *asm) jge(k uint32, jt, jf string) { a.add(unix.SockFilter{Code: 0x35, K: k}, jt, jf) }
func (a *asm) label(name string) {
	if a.labels == nil {
		a.jumps, a.labels = map[int][2]string{}, map[string]int{}
	}
	a.labels[name] = len(a.ins)
}

func (a *asm) program() ([]unix.SockFilter, error) {
	out := append([]unix.SockFilter(nil), a.ins...)
	for i, j := range a.jumps {
		for k, name := range j {
			if name == "" {
				continue
			}
			to, ok := a.labels[name]
			off := to - i - 1
			if !ok || off < 0 || off > 255 {
				return nil, fmt.Errorf("seccomp: bad jump to %q", name)
			}
			if k == 0 {
				out[i].Jt = uint8(off)
			} else {
				out[i].Jf = uint8(off)
			}
		}
	}
	return out, nil
}
