package client

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/devark28/xhaos/app/node/wire"
	"github.com/devark28/xhaos/app/sandbox"
)

// Facts describes this machine for the server.
func Facts(c Config, version string) wire.Facts {
	host, _ := os.Hostname()
	f := wire.Facts{
		Version: version, Protocol: wire.Protocol, Hostname: host,
		OS: runtime.GOOS, Arch: runtime.GOARCH, CPUs: runtime.NumCPU(),
		Root: c.Root, Landlock: sandbox.ABI(), Devices: c.Devices, Unsandbox: c.Unsandboxed,
	}
	f.Sandboxed = !c.Unsandboxed && f.Landlock > 0
	if b, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		f.Kernel = strings.TrimSpace(string(b))
	}
	if b, err := os.ReadFile("/proc/device-tree/model"); err == nil {
		f.Model = strings.TrimRight(string(b), "\x00\n ")
	}
	f.Distro = osRelease()
	f.MemoryMB = memTotalMB()
	for _, pattern := range []string{"/dev/video*", "/dev/ttyUSB*", "/dev/ttyACM*", "/dev/nvidia[0-9]*", "/dev/dri"} {
		m, _ := filepath.Glob(pattern)
		f.Hardware = append(f.Hardware, m...)
	}
	return f
}

func osRelease() string {
	f, err := os.Open("/etc/os-release")
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "PRETTY_NAME="); ok {
			return strings.Trim(v, `"`)
		}
	}
	return ""
}

func memTotalMB() int64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "MemTotal:"); ok {
			kb, _ := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
			return kb / 1024
		}
	}
	return 0
}
