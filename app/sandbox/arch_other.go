//go:build !linux || (!amd64 && !arm64)

package sandbox

const auditArch = 0

// No filter on other systems and architectures: denySockets reports it.
var sysSocket = -1
