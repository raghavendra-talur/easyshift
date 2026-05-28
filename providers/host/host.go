package host

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/raghavendra-talur/easyshift/interfaces"
)

// SystemHostInspector is the real HostInspector backed by the running OS.
type SystemHostInspector struct{}

// NewSystemHostInspector returns a HostInspector that reads the actual host.
func NewSystemHostInspector() *SystemHostInspector { return &SystemHostInspector{} }

// HasCPUVirtualization checks /proc/cpuinfo for the `vmx` (Intel VT-x) or
// `svm` (AMD-V) CPU flag. On a non-Linux host the file is absent and the
// call returns an error.
func (SystemHostInspector) HasCPUVirtualization() (bool, error) {
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return false, fmt.Errorf("read /proc/cpuinfo: %w", err)
	}
	return bytes.Contains(data, []byte(" vmx ")) ||
		bytes.Contains(data, []byte("\tvmx ")) ||
		bytes.Contains(data, []byte(" svm ")) ||
		bytes.Contains(data, []byte("\tsvm ")), nil
}

// InspectBridge inspects /sys/class/net/<name>/ to determine whether name is
// a Linux bridge, what interfaces are enslaved to it, and whether it is
// operationally up. A bridge with no slaves is L2-isolated — VMs attached to
// it can boot but have no path to the LAN.
func (SystemHostInspector) InspectBridge(name string) (interfaces.BridgeInfo, error) {
	base := filepath.Join("/sys/class/net", name)
	if _, err := os.Stat(filepath.Join(base, "bridge")); err != nil {
		if os.IsNotExist(err) {
			return interfaces.BridgeInfo{Exists: false}, nil
		}
		return interfaces.BridgeInfo{}, err
	}
	info := interfaces.BridgeInfo{Exists: true}
	entries, err := os.ReadDir(filepath.Join(base, "brif"))
	if err != nil && !os.IsNotExist(err) {
		return interfaces.BridgeInfo{}, fmt.Errorf("read brif for %s: %w", name, err)
	}
	for _, e := range entries {
		info.Slaves = append(info.Slaves, e.Name())
	}
	state, err := os.ReadFile(filepath.Join(base, "operstate"))
	if err != nil {
		return interfaces.BridgeInfo{}, fmt.Errorf("read operstate for %s: %w", name, err)
	}
	info.Up = strings.TrimSpace(string(state)) == "up"
	return info, nil
}

// LookPath returns nil if name resolves via exec.LookPath, else an error
// that names the binary so preflight output is obvious.
func (SystemHostInspector) LookPath(name string) error {
	if _, err := exec.LookPath(name); err != nil {
		return fmt.Errorf("required binary %q not found on PATH: %w", name, err)
	}
	return nil
}

// AvailableDiskBytes returns the bytes free on the filesystem holding path.
// path must exist; callers should pass a directory known to be present
// (e.g., the configDir, not an as-yet-uncreated cluster directory).
func (SystemHostInspector) AvailableDiskBytes(path string) (uint64, error) {
	var s unix.Statfs_t
	if err := unix.Statfs(path, &s); err != nil {
		return 0, fmt.Errorf("statfs %s: %w", path, err)
	}
	return uint64(s.Bavail) * uint64(s.Bsize), nil
}

// ARPLookup scans /proc/net/arp for an entry matching mac (case-insensitive)
// and returns the associated IPv4 address, or "" if no entry exists.
func (SystemHostInspector) ARPLookup(mac string) (string, error) {
	data, err := os.ReadFile("/proc/net/arp")
	if err != nil {
		return "", fmt.Errorf("read /proc/net/arp: %w", err)
	}
	want := strings.ToLower(mac)
	// First line is a header.
	lines := strings.Split(string(data), "\n")
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		if strings.ToLower(fields[3]) == want {
			return fields[0], nil
		}
	}
	return "", nil
}

// DialTCP returns nil if a TCP connect to addr succeeds within timeout.
func (SystemHostInspector) DialTCP(addr string, timeout time.Duration) error {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return err
	}
	return conn.Close()
}
