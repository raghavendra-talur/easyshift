package easyshift

import (
	"context"
	"io"
	"time"
)

// CommandRunner abstracts process execution so business logic can be tested
// without invoking real binaries (virsh, virt-install, openshift-install, ...).
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
	RunStreaming(ctx context.Context, stdout, stderr io.Writer, name string, args ...string) error
}

// Downloader abstracts HTTP downloads (OCP binaries, RHCOS images, ...).
type Downloader interface {
	Download(ctx context.Context, url, destPath string) error
}

// VMSpec describes a VM the VMManager should create. The disk is always
// created in the libvirt default storage pool (so qemu:///system can access
// it); only its size is specified here.
type VMSpec struct {
	Name        string
	MemoryMiB   int
	VCPUs       int
	DiskSizeGiB int
	// StoragePool is the libvirt pool the disk is created in.
	StoragePool string
	MAC         string
	// NetworkArg is passed verbatim to `virt-install --network`.
	// Examples: "network=easyshift-foo,mac=...", "bridge=br0,mac=..."
	NetworkArg string
	// BootISO, if non-empty, boots the VM from this ISO. It must be a path
	// libvirtd/qemu can read — typically a default-pool volume path returned
	// by ImportISO, not a file under the user's home directory.
	BootISO string
	// PXE kernel args (used when BootISO is empty). Includes coreos.inst.* values.
	KernelArgs string
}

// VMManager abstracts libvirt VM lifecycle operations plus the storage
// helpers needed to make boot media accessible to qemu:///system.
type VMManager interface {
	Create(ctx context.Context, spec VMSpec) error
	Start(ctx context.Context, name string) error
	Stop(ctx context.Context, name string) error
	Delete(ctx context.Context, name string) error
	IsRunning(ctx context.Context, name string) (bool, error)
	// ImportISO uploads localPath into the named storage pool as volName and
	// returns the pool volume path for use as VMSpec.BootISO.
	ImportISO(ctx context.Context, pool, volName, localPath string) (string, error)
	// RemoveISO deletes a volume previously created by ImportISO.
	RemoveISO(ctx context.Context, pool, volName string) error
}

// NetworkSpec describes a libvirt NAT network for a cluster.
type NetworkSpec struct {
	Name   string
	Bridge string
	Subnet string // e.g. "192.168.140"
	Domain string // libvirt DNS domain
}

// NetworkProvisioner abstracts virtual network creation. Today this is just
// libvirt NAT networks; later it will also cover HAProxy and DNSmasq configuration.
type NetworkProvisioner interface {
	CreateNetwork(ctx context.Context, spec NetworkSpec) error
	DeleteNetwork(ctx context.Context, name string) error
}

// InstallerSpec carries everything the Installer needs for one call. Binary
// paths live here (not on the Installer struct) so the same Installer can
// service clusters built against different OCP versions — important because
// the version is resolved from a channel alias after deps wiring.
type InstallerSpec struct {
	ClusterDir          string
	Cluster             *ClusterConfig
	PullSecret          string
	SSHPublicKey        string
	InstallerPath       string // path to openshift-install
	CoreOSInstallerPath string // path to coreos-installer
	// Out, if non-nil, receives the streaming stdout+stderr of the wait-for
	// commands. Stages typically set this to MultiWriter(os.Stdout, logFile)
	// so the installer's progress survives a backgrounded run.
	Out io.Writer
}

// Installer abstracts invocations of openshift-install and coreos-installer.
// Every method receives an InstallerSpec so binary paths can be derived
// per-cluster from the resolved OCP version.
type Installer interface {
	WriteInstallConfig(ctx context.Context, spec InstallerSpec) error
	CreateIgnitionConfigs(ctx context.Context, spec InstallerSpec) error
	CreateSingleNodeIgnition(ctx context.Context, spec InstallerSpec) error
	EmbedIgnitionInISO(ctx context.Context, spec InstallerSpec, isoPath, ignitionPath, outputPath string) error
	WaitForInstallComplete(ctx context.Context, spec InstallerSpec) error
	// CoreOSLiveISOURL returns the download URL of the RHCOS live ISO that
	// this openshift-install build pins, via `openshift-install coreos
	// print-stream-json`. This is authoritative — it avoids guessing mirror
	// paths and handles RHCOS versions that differ from the OCP version.
	CoreOSLiveISOURL(ctx context.Context, spec InstallerSpec) (string, error)
}

// FileServer abstracts the HTTP server that hosts ignition + RHCOS files for VMs.
type FileServer interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	RootDir() string
	BaseURL() string
}

// CSRApprover is a long-running task that approves pending CSRs against a
// cluster's kubeconfig. Run blocks until ctx is cancelled; callers launch
// it as a goroutine and cancel when their wait-for-bootstrap has returned.
// ocPath identifies the oc binary, derived per-cluster from the resolved
// OCP version, so the approver does not need to know about version state.
type CSRApprover interface {
	Run(ctx context.Context, ocPath, kubeconfigPath string) error
}

// HostnameInjector is a long-running task that polls SSH on the master VM
// and runs `hostnamectl set-hostname <hostname>` until ctx is cancelled.
// We need this because RHCOS's node-valid-hostname.service has a 5-min
// TimeoutSec — if no valid hostname arrives in that window, kubelet starts
// and registers the node permanently as `localhost.localdomain`. The
// injector keeps retrying so it survives the live-ISO → installed-RHCOS
// reboot (each reboot resets hostname to localhost.localdomain).
type HostnameInjector interface {
	Run(ctx context.Context, ip, sshKeyPath, hostname string) error
}

// DNSResolver looks up A records for a name. It exists as an interface so
// the direct-mode DNS preflight is fully fakeable in tests. The real impl
// shells out to `dig` so the resolver path matches what users typically
// use to debug DNS themselves.
type DNSResolver interface {
	Resolve(ctx context.Context, name string) ([]string, error)
}

// CertIssuer obtains a TLS certificate covering the given domains via an
// ACME DNS-01 challenge. The DNS-01 path is mandatory for wildcard certs
// (e.g. `*.apps.<fqdn>`) and convenient for the API name too because we
// already have DNS-provider credentials from the DNSManager wiring.
// Issue blocks until the cert is signed (typically 30–90s). Returned PEM
// includes the leaf + any intermediates so it can be planted directly
// into an OpenShift TLS secret.
type CertIssuer interface {
	Issue(ctx context.Context, domains []string) (certPEM, keyPEM []byte, err error)
}

// DNSManager mutates A records on a public DNS provider (Cloudflare,
// Route53, etc.). It's behind an interface so the rest of easyshift never
// imports a provider SDK directly: production wires up libdns + a chosen
// provider package, tests inject a fake. zone is the parent DNS zone
// (e.g. "rtalur.dev"); fqdn is the cluster's name + base domain
// (e.g. "test.rtalur.dev") and must be a subdomain of zone. Upsert
// creates/updates `api.<fqdn>`, `api-int.<fqdn>`, and `*.apps.<fqdn>`,
// all pointing at ip. Delete removes the same set.
type DNSManager interface {
	Upsert(ctx context.Context, zone, fqdn, ip string) error
	Delete(ctx context.Context, zone, fqdn string) error
}

// BridgeInfo is the host's view of a Linux bridge. Exists is false when no
// /sys/class/net/<name>/bridge directory is present (e.g. name is a physical
// NIC, not a bridge). Slaves lists the interfaces enslaved to the bridge —
// an empty list means VMs attached to it have no L2 path to anything. Up is
// true when operstate is "up".
type BridgeInfo struct {
	Exists bool
	Slaves []string
	Up     bool
}

// HostInspector queries the host environment for preflight checks. It
// exists as an interface (parallel to CommandRunner) so tests can simulate
// environments without CPU virtualization, missing binaries, etc.
type HostInspector interface {
	// HasCPUVirtualization returns true if the host CPU advertises VT-x or AMD-V.
	HasCPUVirtualization() (bool, error)
	// InspectBridge returns the host's view of the named bridge: whether it
	// is a bridge at all, what interfaces are enslaved to it, and whether it
	// is operationally up. Used to catch the "br0 exists but is empty / down"
	// trap where VMs boot fine but have no L2 path to the LAN.
	InspectBridge(name string) (BridgeInfo, error)
	// LookPath returns nil if the binary is on PATH, else an error.
	LookPath(name string) error
	// AvailableDiskBytes returns bytes free on the filesystem holding path.
	AvailableDiskBytes(path string) (uint64, error)
	// ARPLookup returns the IPv4 address the host has cached for mac (from
	// /proc/net/arp), or "" if no entry exists.
	ARPLookup(mac string) (string, error)
	// DialTCP returns nil if a TCP connection to addr (host:port) succeeds
	// within timeout, else the dial error.
	DialTCP(addr string, timeout time.Duration) error
}
