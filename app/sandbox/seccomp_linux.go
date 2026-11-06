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
// datagram sockets fail too (sealed commands: a DNS lookup could carry a
// secret out; their proxy resolves names for them).
func denySockets(udp bool) error {
	if sysSocket < 0 {
		return fmt.Errorf("no seccomp filter for %s", runtime.GOARCH)
	}
	const (
		allow  = 0x7fff0000
		denied = unix.SECCOMP_RET_ERRNO | uint32(unix.EACCES)
	)
	var a asm
	a.ld(4)                       // arch
	a.jeq(auditArch, "", "deny")  // another ABI (32-bit calls): deny
	a.ld(0)                       // syscall number
	a.jge(0x40000000, "deny", "") // x32 numbers: deny
	a.jeq(uint32(sysSocket), "", "allow")
	a.ld(16) // domain (low half of the first argument)
	a.jeq(unix.AF_UNIX, "deny", "")
	if udp {
		a.jeq(unix.AF_INET, "type", "")
		a.jeq(unix.AF_INET6, "type", "allow")
		a.label("type")
		a.ld(24)   // type (low half of the second argument)
		a.and(0xf) // without SOCK_NONBLOCK / SOCK_CLOEXEC
		a.jeq(unix.SOCK_DGRAM, "deny", "allow")
	}
	a.label("allow")
	a.ret(allow)
	a.label("deny")
	a.ret(denied)
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
