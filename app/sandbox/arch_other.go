//go:build !amd64 && !arm64

package sandbox

const auditArch = 0

// No filter on other architectures: denyUnixSockets reports it.
var sysSocket = -1
