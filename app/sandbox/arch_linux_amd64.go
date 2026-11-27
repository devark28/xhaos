package sandbox

import "golang.org/x/sys/unix"

const (
	auditArch = unix.AUDIT_ARCH_X86_64
	sysSocket = unix.SYS_SOCKET
	// The kernel keyring calls: denied, so a secret cannot pass through it.
	sysAddKey     = unix.SYS_ADD_KEY
	sysRequestKey = unix.SYS_REQUEST_KEY
	sysKeyctl     = unix.SYS_KEYCTL
)
