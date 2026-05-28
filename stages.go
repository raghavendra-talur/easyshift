package easyshift

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/sirupsen/logrus"
)

// ClusterCreateStages is the ordered pipeline used by ClusterManager.Create.
// Adding or removing entries changes the cluster lifecycle; renaming an entry
// is a breaking change for clusters that have already recorded the old name
// in state.json.
//
// Phase 1 is SNO-only (MasterCount=1, WorkerCount=0). A create-worker-vms
// stage will be re-added in Phase 4 alongside the `addnode` command.
func ClusterCreateStages() []Stage {
	return []Stage{
		&registerClusterStage{},
		&allocateNetworkStage{},
		&upsertDNSStage{},
		&ensureClusterDirStage{},
		&downloadBinariesStage{},
		&downloadRHCOSStage{},
		&generateSSHKeyStage{},
		&generateIgnitionStage{},
		&embedIgnitionISOStage{},
		&createLibvirtNetworkStage{},
		&createMasterVMsStage{},
		// wait-for-bootstrap was removed: openshift-install wait-for
		// install-complete already waits for the API to come up and supersedes
		// bootstrap-complete's 45-min budget (which is too tight for SNO
		// bootstrap-in-place once mco-firstboot's ostree pivot is involved).
		&waitForInstallStage{},
		&applyTLSCertsStage{},
		&finalizeStage{},
	}
}

// --- register-cluster ----------------------------------------------------

// registerClusterStage adds the cluster to cfg.Clusters with State=creating
// and persists config.json. Partially-created clusters are visible to
// `easyshift list` and can be removed via `easyshift delete`.
type registerClusterStage struct{}

func (registerClusterStage) Name() string { return "register-cluster" }

func (registerClusterStage) Apply(_ context.Context, sc *StageContext) error {
	for _, c := range sc.Config.Clusters {
		if c.Name == sc.Cluster.Name {
			return nil
		}
	}
	sc.Cluster.State = ClusterStateCreating
	sc.Config.Clusters = append(sc.Config.Clusters, sc.Cluster)
	return sc.Config.Save()
}

func (registerClusterStage) Rollback(_ context.Context, sc *StageContext) error {
	for i, c := range sc.Config.Clusters {
		if c.Name == sc.Cluster.Name {
			sc.Config.Clusters = append(sc.Config.Clusters[:i], sc.Config.Clusters[i+1:]...)
			break
		}
	}
	return sc.Config.Save()
}

// --- allocate-network ----------------------------------------------------

type allocateNetworkStage struct{}

func (allocateNetworkStage) Name() string { return "allocate-network" }

func (allocateNetworkStage) Apply(_ context.Context, sc *StageContext) error {
	if len(sc.Cluster.MACAddresses) > 0 {
		return nil
	}
	if sc.Cluster.NetworkMode == NetworkModeBridge {
		// User supplied the MAC + IP at the router. Just record them and
		// register the MAC so a future cluster doesn't try to claim it.
		sc.Cluster.MACAddresses = []string{sc.Cluster.MasterMAC}
		sc.Cluster.IPAddresses = []string{sc.Cluster.MasterIP}
		sc.Config.GlobalState.UsedMACs[sc.Cluster.MasterMAC] = true
		return sc.Config.Save()
	}
	// NAT mode: auto-allocate MAC and IP, and set MachineCIDR to the
	// libvirt subnet so install-config knows where kubelet should live.
	if err := NewNetworkAllocator(sc.Config).Allocate(sc.Cluster); err != nil {
		return err
	}
	sc.Cluster.MachineCIDR = sc.Cluster.NetworkSubnet + ".0/24"
	return sc.Config.Save()
}

func (allocateNetworkStage) Rollback(_ context.Context, sc *StageContext) error {
	NewNetworkAllocator(sc.Config).Release(sc.Cluster)
	sc.Cluster.IPAddresses = nil
	sc.Cluster.MACAddresses = nil
	return sc.Config.Save()
}

// --- upsert-dns ----------------------------------------------------------

// upsertDNSStage creates the cluster's public A records (api, api-int,
// *.apps) via the configured DNS provider. No-op when DNSProvider is
// unset — in that mode the user is responsible for the records and the
// generate-ignition preflight will verify they resolve correctly.
type upsertDNSStage struct{}

func (upsertDNSStage) Name() string { return "upsert-dns" }

// Preflight verifies a token exists for the named provider. Done at
// preflight time (not Apply) so we fail before any side effects.
func (upsertDNSStage) Preflight(_ context.Context, sc *StageContext) error {
	if sc.Cluster.DNSProvider == "" {
		return nil
	}
	return EnsureDNSToken(sc.Config.ConfigDir, sc.Cluster.DNSProvider)
}

func (upsertDNSStage) Apply(ctx context.Context, sc *StageContext) error {
	if sc.Cluster.DNSProvider == "" {
		return nil
	}
	zone, fqdn := dnsZoneAndFQDN(sc.Cluster)
	return sc.Deps.DNSManager.Upsert(ctx, zone, fqdn, sc.Cluster.MasterIP)
}

func (upsertDNSStage) Rollback(ctx context.Context, sc *StageContext) error {
	if sc.Cluster.DNSProvider == "" {
		return nil
	}
	zone, fqdn := dnsZoneAndFQDN(sc.Cluster)
	return sc.Deps.DNSManager.Delete(ctx, zone, fqdn)
}

// dnsZoneAndFQDN returns the parent zone and the cluster's full FQDN.
// DNSZone defaults to the cluster's base Domain.
func dnsZoneAndFQDN(c *ClusterConfig) (zone, fqdn string) {
	zone = c.DNSZone
	if zone == "" {
		zone = c.Domain
	}
	fqdn = c.Name + "." + c.Domain
	return zone, fqdn
}

// --- ensure-cluster-dir --------------------------------------------------

type ensureClusterDirStage struct{}

func (ensureClusterDirStage) Name() string { return "ensure-cluster-dir" }

func (ensureClusterDirStage) Apply(_ context.Context, sc *StageContext) error {
	// Only the working dir for openshift-install artifacts (install-config,
	// ignition, auth/kubeconfig, staged ISO). VM disks and the boot ISO live
	// in the libvirt default pool, not here — see embed-ignition-iso and
	// create-master-vms.
	return os.MkdirAll(clusterDir(sc), 0o700)
}

func (ensureClusterDirStage) Rollback(_ context.Context, sc *StageContext) error {
	return os.RemoveAll(clusterDir(sc))
}

// --- download-binaries ---------------------------------------------------

// downloadBinariesStage fetches openshift-install, oc, and coreos-installer
// into the shared per-version bin/ cache. Other clusters of the same OCP
// version reuse the same files.
type downloadBinariesStage struct{}

func (downloadBinariesStage) Name() string { return "download-binaries" }

// Preflight verifies `tar` is on PATH; the stage extracts the
// openshift-install and openshift-client tarballs via it.
func (downloadBinariesStage) Preflight(_ context.Context, sc *StageContext) error {
	return sc.Deps.Host.LookPath("tar")
}

func (downloadBinariesStage) Apply(ctx context.Context, sc *StageContext) error {
	binDir := BinariesDir(sc.Config.ConfigDir, sc.Cluster.OCPVersion)
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		return err
	}

	// openshift-install
	if _, err := os.Stat(filepath.Join(binDir, "openshift-install")); err != nil {
		if err := downloadTarball(ctx, sc, OCPClientURL(sc.Cluster.OCPVersion, "openshift-install-linux.tar.gz"), binDir); err != nil {
			return fmt.Errorf("download openshift-install: %w", err)
		}
	}
	// oc + kubectl
	if _, err := os.Stat(filepath.Join(binDir, "oc")); err != nil {
		if err := downloadTarball(ctx, sc, OCPClientURL(sc.Cluster.OCPVersion, "openshift-client-linux.tar.gz"), binDir); err != nil {
			return fmt.Errorf("download oc: %w", err)
		}
	}
	// coreos-installer
	coreosPath := filepath.Join(binDir, "coreos-installer")
	if _, err := os.Stat(coreosPath); err != nil {
		if err := sc.Deps.Download.Download(ctx, CoreOSInstallerURL, coreosPath); err != nil {
			return fmt.Errorf("download coreos-installer: %w", err)
		}
		if _, err := sc.Deps.Cmd.Run(ctx, "chmod", "+x", coreosPath); err != nil {
			return fmt.Errorf("chmod coreos-installer: %w", err)
		}
	}
	return nil
}

// Rollback is a no-op: the binaries cache is shared, so removing it here
// would break parallel cluster builds. The entire <configDir>/bin tree can
// be cleared manually with `easyshift purge` (future).
func (downloadBinariesStage) Rollback(_ context.Context, _ *StageContext) error { return nil }

// --- download-rhcos ------------------------------------------------------

type downloadRHCOSStage struct{}

func (downloadRHCOSStage) Name() string { return "download-rhcos" }

func (downloadRHCOSStage) Apply(ctx context.Context, sc *StageContext) error {
	dest := rhcosLiveISOPath(sc)
	if _, err := os.Stat(dest); err == nil {
		return nil // already cached
	}
	// Ask openshift-install which RHCOS live ISO it pins, rather than guessing
	// a mirror path. Requires the binary from download-binaries (earlier stage).
	url, err := sc.Deps.Installer.CoreOSLiveISOURL(ctx, installerSpec(sc))
	if err != nil {
		return fmt.Errorf("determine RHCOS live ISO url: %w", err)
	}
	return sc.Deps.Download.Download(ctx, url, dest)
}

// Same rationale as download-binaries: cache is shared across clusters.
func (downloadRHCOSStage) Rollback(_ context.Context, _ *StageContext) error { return nil }

// --- generate-ssh-key ----------------------------------------------------

// generateSSHKeyStage runs ssh-keygen to produce an RSA keypair scoped to
// this cluster. The public key is embedded in install-config.yaml so the
// host operator can SSH to RHCOS nodes as `core`.
type generateSSHKeyStage struct{}

func (generateSSHKeyStage) Name() string { return "generate-ssh-key" }

// Preflight verifies `ssh-keygen` is on PATH.
func (generateSSHKeyStage) Preflight(_ context.Context, sc *StageContext) error {
	return sc.Deps.Host.LookPath("ssh-keygen")
}

func (s generateSSHKeyStage) Apply(ctx context.Context, sc *StageContext) error {
	keyPath := filepath.Join(clusterDir(sc), "id_rsa")
	if _, err := os.Stat(keyPath); err == nil {
		return nil
	}
	_, err := sc.Deps.Cmd.Run(ctx, "ssh-keygen", "-t", "rsa", "-b", "4096", "-f", keyPath, "-N", "", "-q")
	return err
}

func (s generateSSHKeyStage) Rollback(_ context.Context, sc *StageContext) error {
	keyPath := filepath.Join(clusterDir(sc), "id_rsa")
	_ = os.Remove(keyPath)
	_ = os.Remove(keyPath + ".pub")
	return nil
}

// --- generate-ignition ---------------------------------------------------

// generateIgnitionStage writes install-config.yaml and then runs
// `openshift-install create single-node-ignition-config`. Both steps share
// a stage because `create single-node-ignition-config` consumes (deletes)
// install-config.yaml as a side effect, so a partial failure cannot be
// retried piecemeal.
type generateIgnitionStage struct{}

func (generateIgnitionStage) Name() string { return "generate-ignition" }

// Preflight parses the persisted pull secret as JSON and (in bridge mode)
// also verifies the required DNS records point at MasterIP. Catching DNS
// misconfig here saves the user the eventual "etcd timed out" failure
// 15 minutes into the install.
func (generateIgnitionStage) Preflight(ctx context.Context, sc *StageContext) error {
	if err := checkPullSecretJSON(sc.Config.ConfigDir); err != nil {
		return err
	}
	if sc.Cluster.NetworkMode == NetworkModeBridge && sc.Cluster.DNSProvider == "" {
		// User manages DNS by hand — verify the records resolve before we
		// burn 30+ minutes on an install that can't reach the API. When
		// DNSProvider is set, easyshift will create the records itself in
		// the upsert-dns stage, so this check would always fail here.
		if err := sc.Deps.Host.LookPath("dig"); err != nil {
			return fmt.Errorf("DNS preflight needs `dig`: %w\n  hint: install bind-utils (Fedora/RHEL) or dnsutils (Debian/Ubuntu)", err)
		}
		if err := checkBridgeModeDNS(ctx, sc); err != nil {
			return err
		}
	}
	return nil
}

// ClusterDNSNames returns the DNS names a bridge-mode cluster needs, all of
// which must resolve to the master IP. fqdn is "<name>.<base-domain>".
// console-openshift-console.apps.<fqdn> stands in for the *.apps wildcard
// (a literal "*" isn't a resolvable query).
func ClusterDNSNames(fqdn string) []string {
	return []string{
		"api." + fqdn,
		"api-int." + fqdn,
		"console-openshift-console.apps." + fqdn,
	}
}

// checkBridgeModeDNS verifies every required record resolves to the master
// IP, aggregating all problems into one error that also prints the exact
// records the user must create.
func checkBridgeModeDNS(ctx context.Context, sc *StageContext) error {
	fqdn := sc.Cluster.Name + "." + sc.Cluster.Domain
	ip := sc.Cluster.MasterIP

	var problems []string
	for _, name := range ClusterDNSNames(fqdn) {
		ips, err := sc.Deps.DNS.Resolve(ctx, name)
		switch {
		case err != nil:
			problems = append(problems, fmt.Sprintf("%s: lookup failed: %v", name, err))
		case len(ips) == 0:
			problems = append(problems, fmt.Sprintf("%s: no records", name))
		case !containsStr(ips, ip):
			problems = append(problems, fmt.Sprintf("%s: resolves to %v, want %s", name, ips, ip))
		}
	}
	if len(problems) == 0 {
		return nil
	}

	var b strings.Builder
	b.WriteString("bridge-mode DNS is not configured correctly:\n")
	for _, p := range problems {
		fmt.Fprintf(&b, "  - %s\n", p)
	}
	b.WriteString("create these records (all pointing at the master IP) on your DNS server, then retry:\n")
	fmt.Fprintf(&b, "    api.%s.\tA\t%s\n", fqdn, ip)
	fmt.Fprintf(&b, "    api-int.%s.\tA\t%s\n", fqdn, ip)
	fmt.Fprintf(&b, "    *.apps.%s.\tA\t%s\n", fqdn, ip)
	b.WriteString("optional but recommended (OpenShift docs): also add a PTR record so any node-discovery / cert-validation paths work; easyshift bakes /etc/hostname so this is not strictly required:\n")
	fmt.Fprintf(&b, "    PTR for %s -> master-0.%s.\n", ip, fqdn)
	return errors.New(b.String())
}

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func checkPullSecretJSON(configDir string) error {
	data, err := os.ReadFile(PullSecretPath(configDir))
	if err != nil {
		return fmt.Errorf("read pull secret: %w", err)
	}
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(data, &parsed); err != nil {
		return fmt.Errorf("pull secret is not valid JSON: %w", err)
	}
	if _, ok := parsed["auths"]; !ok {
		return fmt.Errorf("pull secret is missing required 'auths' key (download a fresh secret from console.redhat.com)")
	}
	return nil
}

// checkDirectModeDNS asserts that api.<fqdn>, api-int.<fqdn>, and a
// wildcard probe under apps.<fqdn> all resolve to MasterIP. <fqdn> is the
// cluster's name + domain.
func checkDirectModeDNS(ctx context.Context, sc *StageContext) error {
	fqdn := sc.Cluster.Name + "." + sc.Cluster.Domain
	targets := []string{
		"api." + fqdn,
		"api-int." + fqdn,
		// A literal *.apps lookup isn't legal DNS; probing a synthetic
		// label exercises the wildcard the user must have configured.
		"console-openshift-console.apps." + fqdn,
	}
	for _, name := range targets {
		ips, err := sc.Deps.DNS.Resolve(ctx, name)
		if err != nil {
			return fmt.Errorf("DNS lookup for %s failed: %w", name, err)
		}
		if len(ips) == 0 {
			return fmt.Errorf("DNS lookup for %s returned no records; add an A record pointing to %s", name, sc.Cluster.MasterIP)
		}
		var matched bool
		for _, ip := range ips {
			if ip == sc.Cluster.MasterIP {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("DNS %s resolves to %v but expected %s — check your DNS records", name, ips, sc.Cluster.MasterIP)
		}
	}
	return nil
}

func (s generateIgnitionStage) Apply(ctx context.Context, sc *StageContext) error {
	pullSecret, err := ReadPullSecret(sc.Config.ConfigDir)
	if err != nil {
		return err
	}
	pubKey, err := os.ReadFile(filepath.Join(clusterDir(sc), "id_rsa.pub"))
	if err != nil {
		return fmt.Errorf("read ssh public key: %w", err)
	}

	spec := installerSpec(sc)
	spec.PullSecret = pullSecret
	spec.SSHPublicKey = string(pubKey)
	if err := sc.Deps.Installer.WriteInstallConfig(ctx, spec); err != nil {
		return err
	}
	return sc.Deps.Installer.CreateSingleNodeIgnition(ctx, spec)
}

func (s generateIgnitionStage) Rollback(_ context.Context, sc *StageContext) error {
	for _, name := range []string{
		"install-config.yaml",
		"bootstrap-in-place-for-live-iso.ign",
		"master.ign",
		"worker.ign",
		"bootstrap.ign",
		"metadata.json",
	} {
		_ = os.Remove(filepath.Join(clusterDir(sc), name))
	}
	return nil
}

// MasterHostname is the FQDN baked into the master node's kernel cmdline
// via `systemd.hostname=`. We pin it deterministically rather than relying
// on DHCP option 12 or a PTR record (home routers usually provide neither),
// because RHCOS's node-valid-hostname.service has a 5-min TimeoutSec and
// kubelet will register the node with whatever hostname is set when the
// timer expires — and that registration is permanent.
func MasterHostname(c *ClusterConfig) string {
	return fmt.Sprintf("master-0.%s.%s", c.Name, c.Domain)
}

// --- embed-ignition-iso --------------------------------------------------

// embedIgnitionISOStage produces the per-cluster master ISO by embedding the
// SNO ignition into a copy of the cached live ISO via coreos-installer.
type embedIgnitionISOStage struct{}

func (embedIgnitionISOStage) Name() string { return "embed-ignition-iso" }

// Preflight verifies the storage pool is ready before we try to upload the
// boot ISO into it.
func (embedIgnitionISOStage) Preflight(ctx context.Context, sc *StageContext) error {
	return checkStoragePool(ctx, sc.Deps.Cmd, sc.Cluster.StoragePool)
}

func (embedIgnitionISOStage) Apply(ctx context.Context, sc *StageContext) error {
	srcISO := rhcosLiveISOPath(sc)
	ignition := filepath.Join(clusterDir(sc), "bootstrap-in-place-for-live-iso.ign")
	local := masterISOPath(sc)
	if err := sc.Deps.Installer.EmbedIgnitionInISO(ctx, installerSpec(sc), srcISO, ignition, local); err != nil {
		return err
	}
	// Upload the embedded ISO into the libvirt default pool so qemu can read
	// it (a file under $HOME is not reachable by the qemu user).
	volPath, err := sc.Deps.VM.ImportISO(ctx, sc.Cluster.StoragePool, masterISOVolName(sc.Cluster), local)
	if err != nil {
		return err
	}
	sc.Cluster.BootISOVolPath = volPath
	return sc.Config.Save()
}

func (embedIgnitionISOStage) Rollback(ctx context.Context, sc *StageContext) error {
	_ = sc.Deps.VM.RemoveISO(ctx, sc.Cluster.StoragePool, masterISOVolName(sc.Cluster))
	_ = os.Remove(masterISOPath(sc))
	sc.Cluster.BootISOVolPath = ""
	return sc.Config.Save()
}

// --- create-libvirt-network ---------------------------------------------

type createLibvirtNetworkStage struct{}

func (createLibvirtNetworkStage) Name() string { return "create-libvirt-network" }

// Preflight ensures qemu:///system is reachable before any NAT-mode side
// effects begin. In bridge mode the stage is a no-op, so the check is
// skipped (it still runs from createMasterVMsStage).
func (createLibvirtNetworkStage) Preflight(ctx context.Context, sc *StageContext) error {
	if sc.Cluster.NetworkMode != NetworkModeNAT {
		return nil
	}
	return checkLibvirtAccess(ctx, sc.Deps.Cmd)
}

func (createLibvirtNetworkStage) Apply(ctx context.Context, sc *StageContext) error {
	if sc.Cluster.NetworkMode != NetworkModeNAT {
		return nil
	}
	return sc.Deps.Net.CreateNetwork(ctx, NetworkSpec{
		Name:   networkName(sc.Cluster),
		Bridge: networkName(sc.Cluster),
		Subnet: sc.Cluster.NetworkSubnet,
		Domain: sc.Cluster.Name + "." + sc.Cluster.Domain,
	})
}

func (createLibvirtNetworkStage) Rollback(ctx context.Context, sc *StageContext) error {
	if sc.Cluster.NetworkMode != NetworkModeNAT {
		return nil
	}
	return sc.Deps.Net.DeleteNetwork(ctx, networkName(sc.Cluster))
}

// --- create-master-vms --------------------------------------------------

type createMasterVMsStage struct{}

func (createMasterVMsStage) Name() string { return "create-master-vms" }

// Preflight runs every host-environment check needed before VM creation can
// hope to succeed: libvirt reachable, virt-install on PATH, CPU has
// virtualization extensions, enough disk under the config dir for the
// master qcow2, and (in bridge mode) the named host bridge exists.
func (createMasterVMsStage) Preflight(ctx context.Context, sc *StageContext) error {
	if err := checkLibvirtAccess(ctx, sc.Deps.Cmd); err != nil {
		return err
	}
	if err := checkStoragePool(ctx, sc.Deps.Cmd, sc.Cluster.StoragePool); err != nil {
		return err
	}
	if err := sc.Deps.Host.LookPath("virt-install"); err != nil {
		return err
	}
	hasVT, err := sc.Deps.Host.HasCPUVirtualization()
	if err != nil {
		return fmt.Errorf("detect cpu virtualization: %w", err)
	}
	if !hasVT {
		return fmt.Errorf("host CPU does not advertise vmx/svm — virtualization extensions are required")
	}
	avail, err := sc.Deps.Host.AvailableDiskBytes(sc.Config.ConfigDir)
	if err != nil {
		return fmt.Errorf("query disk space at %s: %w", sc.Config.ConfigDir, err)
	}
	need := uint64(sc.Cluster.MasterDiskGB) * 1024 * 1024 * 1024
	if avail < need {
		return fmt.Errorf("insufficient disk under %s: have %d GiB, need %d GiB for master disk",
			sc.Config.ConfigDir, avail>>30, sc.Cluster.MasterDiskGB)
	}
	if sc.Cluster.NetworkMode == NetworkModeBridge {
		br, err := sc.Deps.Host.InspectBridge(sc.Cluster.Bridge)
		if err != nil {
			return fmt.Errorf("inspect bridge %s: %w", sc.Cluster.Bridge, err)
		}
		if !br.Exists {
			return fmt.Errorf("bridge %q does not exist (or is not a Linux bridge) on this host; create it and enslave your LAN interface before running easyshift", sc.Cluster.Bridge)
		}
		if len(br.Slaves) == 0 {
			return fmt.Errorf("bridge %q exists but has no slave interfaces — VMs attached to it have no path to the LAN; enslave your LAN NIC (e.g. `sudo nmcli con add type bridge-slave ifname <NIC> master %s`)", sc.Cluster.Bridge, sc.Cluster.Bridge)
		}
		if !br.Up {
			return fmt.Errorf("bridge %q is not up (operstate != \"up\") with slaves %v; bring it up (e.g. `sudo ip link set %s up`)", sc.Cluster.Bridge, br.Slaves, sc.Cluster.Bridge)
		}
	}
	return nil
}

func (s createMasterVMsStage) Apply(ctx context.Context, sc *StageContext) error {
	for i := 0; i < sc.Cluster.MasterCount; i++ {
		if err := createMasterVM(ctx, sc, i); err != nil {
			return err
		}
	}
	return nil
}

func (s createMasterVMsStage) Rollback(ctx context.Context, sc *StageContext) error {
	for i := sc.Cluster.MasterCount - 1; i >= 0; i-- {
		name := fmt.Sprintf("master-%d-%s", i, sc.Cluster.Name)
		if err := sc.Deps.VM.Delete(ctx, name); err != nil {
			return err
		}
	}
	return nil
}

// --- wait-for-install ---------------------------------------------------

// waitForInstallStage runs `openshift-install wait-for install-complete`.
// install-complete already waits for the API to come up (no need for a
// separate wait-for-bootstrap), then for every cluster operator to become
// Available, with a 90-min budget — generous enough for SNO bootstrap-in-
// place even when mco-firstboot does an ostree pivot.
//
// A CSR-approving goroutine runs alongside to sweep pending CSRs against
// the cluster's kubeconfig (kubelet generates them as it joins).
type waitForInstallStage struct{}

func (waitForInstallStage) Name() string { return "wait-for-install" }

func (waitForInstallStage) Apply(ctx context.Context, sc *StageContext) error {
	helperCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	csrDone := make(chan struct{})
	go func() {
		defer close(csrDone)
		_ = sc.Deps.CSR.Run(helperCtx, ocBinaryPath(sc), filepath.Join(clusterDir(sc), "auth", "kubeconfig"))
	}()

	hostnameDone := make(chan struct{})
	if sc.Cluster.NetworkMode == NetworkModeBridge && sc.Cluster.MasterIP != "" {
		go func() {
			defer close(hostnameDone)
			_ = sc.Deps.Hostname.Run(helperCtx,
				sc.Cluster.MasterIP,
				filepath.Join(clusterDir(sc), "id_rsa"),
				MasterHostname(sc.Cluster))
		}()
	} else {
		close(hostnameDone)
	}

	spec, closeFn := installerWaitSpec(sc)
	defer closeFn()
	err := waitForInstallWithRetry(ctx, sc, spec)
	cancel()
	<-csrDone
	<-hostnameDone
	return err
}

// waitForInstallRetries is the max number of times we'll re-invoke
// openshift-install wait-for install-complete on its 40-min initialization
// timeout. SNO bootstrap-in-place with mco-firstboot's ostree pivot can
// easily run past the initial budget; openshift-install's own error message
// instructs the operator to re-run the command, so we automate it.
const waitForInstallRetries = 3

func waitForInstallWithRetry(ctx context.Context, sc *StageContext, spec InstallerSpec) error {
	vmName := fmt.Sprintf("master-0-%s", sc.Cluster.Name)
	var err error
	for attempt := 1; attempt <= waitForInstallRetries; attempt++ {
		err = sc.Deps.Installer.WaitForInstallComplete(ctx, spec)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return err
		}
		// openshift-install exits with status 6 ("timed out waiting for the
		// condition") when init or progress windows expire mid-rollout; the
		// cluster keeps making progress on its own and the next call will
		// pick up where this one left off. Other exits (validation errors,
		// pull-secret failures) are unrecoverable — surface them immediately.
		if !isInstallerTimeoutError(err) {
			return err
		}
		logrus.Warnf("wait-for install-complete timed out (attempt %d/%d), retrying: %v",
			attempt, waitForInstallRetries, err)
		// If the VM has shut itself off (common after mco-firstboot reboot
		// loops or a hung bootstrap), the next retry will burn its 40-min
		// budget on a dead API. Restart it so the retry actually has a
		// chance.
		if running, ierr := sc.Deps.VM.IsRunning(ctx, vmName); ierr == nil && !running {
			logrus.Warnf("VM %s is shut off between retries; restarting", vmName)
			if serr := sc.Deps.VM.Start(ctx, vmName); serr != nil {
				logrus.Warnf("restart VM %s: %v", vmName, serr)
			}
		}
	}
	return fmt.Errorf("wait-for install-complete: gave up after %d timeouts: %w",
		waitForInstallRetries, err)
}

// isInstallerTimeoutError matches openshift-install's exit-status-6 wrapper
// produced by our CommandRunner. We treat the textual marker "exit status 6"
// as authoritative; openshift-install reserves 6 specifically for "timed out
// waiting for the condition" (see installer's pkg/asset/installconfig).
func isInstallerTimeoutError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "exit status 6")
}

func (waitForInstallStage) Rollback(_ context.Context, _ *StageContext) error { return nil }

// --- apply-tls-certs ----------------------------------------------------

// applyTLSCertsStage issues Let's Encrypt certs for api.<fqdn> and
// *.apps.<fqdn> via ACME DNS-01, plants them as TLS secrets in the
// cluster, and patches APIServer + IngressController to use them. No-op
// when TLSEmail is unset.
type applyTLSCertsStage struct{}

func (applyTLSCertsStage) Name() string { return "apply-tls-certs" }

// Preflight verifies the prerequisites for TLS: a DNS provider must be
// configured (we reuse its token for the ACME DNS-01 challenge), and an
// email must be set. Done at preflight time so easyshift fails fast
// before consuming any Let's Encrypt rate-limit budget.
func (applyTLSCertsStage) Preflight(_ context.Context, sc *StageContext) error {
	if sc.Cluster.TLSEmail == "" {
		return nil
	}
	if sc.Cluster.DNSProvider == "" {
		return fmt.Errorf("TLS issuance requires --dns-provider (used for the ACME DNS-01 challenge)")
	}
	return EnsureDNSToken(sc.Config.ConfigDir, sc.Cluster.DNSProvider)
}

func (applyTLSCertsStage) Apply(ctx context.Context, sc *StageContext) error {
	if sc.Cluster.TLSEmail == "" {
		return nil
	}
	token, err := ReadDNSToken(sc.Config.ConfigDir, sc.Cluster.DNSProvider)
	if err != nil {
		return err
	}
	issuer, err := sc.Deps.NewCertIssuer(CertIssuerOpts{
		Email:       sc.Cluster.TLSEmail,
		AccountDir:  ACMEAccountDir(sc.Config.ConfigDir, sc.Cluster.DNSProvider, sc.Cluster.TLSStaging),
		DNSProvider: sc.Cluster.DNSProvider,
		Token:       token,
		Staging:     sc.Cluster.TLSStaging,
	})
	if err != nil {
		return fmt.Errorf("cert issuer: %w", err)
	}

	tlsDir := filepath.Join(clusterDir(sc), "tls")
	if err := os.MkdirAll(tlsDir, 0o700); err != nil {
		return fmt.Errorf("create tls dir: %w", err)
	}

	fqdn := sc.Cluster.Name + "." + sc.Cluster.Domain
	apiCertPath := filepath.Join(tlsDir, "api.crt")
	apiKeyPath := filepath.Join(tlsDir, "api.key")
	if err := issueToFiles(ctx, issuer, []string{"api." + fqdn}, apiCertPath, apiKeyPath); err != nil {
		return fmt.Errorf("issue api cert: %w", err)
	}
	appsCertPath := filepath.Join(tlsDir, "apps.crt")
	appsKeyPath := filepath.Join(tlsDir, "apps.key")
	if err := issueToFiles(ctx, issuer, []string{"*.apps." + fqdn}, appsCertPath, appsKeyPath); err != nil {
		return fmt.Errorf("issue apps cert: %w", err)
	}

	oc := ocBinaryPath(sc)
	kubeconfig := filepath.Join(clusterDir(sc), "auth", "kubeconfig")
	apiSecret := sc.Cluster.Name + "-api-cert"
	appsSecret := sc.Cluster.Name + "-apps-cert"

	if err := applyTLSSecret(ctx, sc.Deps.Cmd, oc, kubeconfig, "openshift-config", apiSecret, apiCertPath, apiKeyPath); err != nil {
		return err
	}
	if err := applyTLSSecret(ctx, sc.Deps.Cmd, oc, kubeconfig, "openshift-ingress", appsSecret, appsCertPath, appsKeyPath); err != nil {
		return err
	}

	apiPatch := fmt.Sprintf(
		`{"spec":{"servingCerts":{"namedCertificates":[{"names":["api.%s"],"servingCertificate":{"name":"%s"}}]}}}`,
		fqdn, apiSecret)
	if _, err := sc.Deps.Cmd.Run(ctx, oc, "--kubeconfig", kubeconfig,
		"patch", "apiserver/cluster", "--type=merge", "-p", apiPatch); err != nil {
		return fmt.Errorf("patch apiserver/cluster: %w", err)
	}

	ingressPatch := fmt.Sprintf(`{"spec":{"defaultCertificate":{"name":"%s"}}}`, appsSecret)
	if _, err := sc.Deps.Cmd.Run(ctx, oc, "--kubeconfig", kubeconfig,
		"-n", "openshift-ingress-operator", "patch", "ingresscontroller/default",
		"--type=merge", "-p", ingressPatch); err != nil {
		return fmt.Errorf("patch ingresscontroller/default: %w", err)
	}
	return nil
}

// Rollback is a no-op: when a cluster is deleted the API server and
// ingress objects are torn down too, so unpicking the patches would just
// thrash. The on-disk cert files are removed with the cluster dir.
func (applyTLSCertsStage) Rollback(_ context.Context, _ *StageContext) error { return nil }

// issueToFiles obtains a cert for domains and writes the PEM cert+key to
// disk with mode 0600.
func issueToFiles(ctx context.Context, issuer CertIssuer, domains []string, certPath, keyPath string) error {
	cert, key, err := issuer.Issue(ctx, domains)
	if err != nil {
		return err
	}
	if err := os.WriteFile(certPath, cert, 0o600); err != nil {
		return err
	}
	return os.WriteFile(keyPath, key, 0o600)
}

// applyTLSSecret creates-or-updates a kubernetes.io/tls Secret in the
// given namespace by piping `oc apply` against the dry-run YAML. Using
// apply (instead of create) makes this idempotent across re-runs.
func applyTLSSecret(ctx context.Context, cmd CommandRunner, oc, kubeconfig, namespace, name, certPath, keyPath string) error {
	out, err := cmd.Run(ctx, oc, "--kubeconfig", kubeconfig,
		"-n", namespace,
		"create", "secret", "tls", name,
		"--cert="+certPath, "--key="+keyPath,
		"--dry-run=client", "-o", "yaml")
	if err != nil {
		return fmt.Errorf("render tls secret %s/%s: %w", namespace, name, err)
	}
	// Write the rendered YAML so we can pipe it to `oc apply -f`.
	tmp := filepath.Join(filepath.Dir(certPath), name+".secret.yaml")
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return fmt.Errorf("write rendered secret %s: %w", tmp, err)
	}
	if _, err := cmd.Run(ctx, oc, "--kubeconfig", kubeconfig, "apply", "-f", tmp); err != nil {
		return fmt.Errorf("apply tls secret %s/%s: %w", namespace, name, err)
	}
	return nil
}

// --- finalize -----------------------------------------------------------

type finalizeStage struct{}

func (finalizeStage) Name() string { return "finalize" }

func (finalizeStage) Apply(_ context.Context, sc *StageContext) error {
	sc.Cluster.State = ClusterStateRunning
	return sc.Config.Save()
}

func (finalizeStage) Rollback(_ context.Context, sc *StageContext) error {
	sc.Cluster.State = ClusterStateCreating
	return sc.Config.Save()
}

// --- helpers ------------------------------------------------------------

func clusterDir(sc *StageContext) string {
	return filepath.Join(sc.Config.ConfigDir, "clusters", sc.Cluster.Name)
}

// installerSpec builds the per-call InstallerSpec for a stage. Binary paths
// resolve against the cluster's currently-known OCPVersion, which by the
// time stages run is always a concrete release (channel aliases like
// "stable" are resolved by ClusterManager.Create before runner.Apply).
func installerSpec(sc *StageContext) InstallerSpec {
	bin := BinariesDir(sc.Config.ConfigDir, sc.Cluster.OCPVersion)
	return InstallerSpec{
		ClusterDir:          clusterDir(sc),
		Cluster:             sc.Cluster,
		InstallerPath:       filepath.Join(bin, "openshift-install"),
		CoreOSInstallerPath: filepath.Join(bin, "coreos-installer"),
	}
}

// ocBinaryPath returns the path to the `oc` binary for the cluster's
// resolved OCP version.
func ocBinaryPath(sc *StageContext) string {
	return filepath.Join(BinariesDir(sc.Config.ConfigDir, sc.Cluster.OCPVersion), "oc")
}

// installerWaitSpec builds an InstallerSpec for the long-running wait stages,
// teeing the installer's output to both os.Stdout (so foreground runs see
// progress live) and the easyshift log file (so a backgrounded run can be
// inspected post-mortem). The returned close func releases the log handle.
func installerWaitSpec(sc *StageContext) (InstallerSpec, func()) {
	spec := installerSpec(sc)
	out, closeFn := openTeeWriter(sc.Config.LogFile)
	spec.Out = out
	return spec, closeFn
}

// openTeeWriter returns a writer that fans out to os.Stdout and (if logPath
// is openable) the named append-mode log file. The close func closes the
// log handle; it is safe to call when the log file couldn't be opened.
func openTeeWriter(logPath string) (io.Writer, func()) {
	if logPath == "" {
		return os.Stdout, func() {}
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		// Log file open failed; just stream to stdout. The error surfaces
		// nowhere visible because we're inside a long-running wait — log
		// it at debug level via logrus, then carry on.
		logrus.Debugf("open installer tee log %s: %v", logPath, err)
		return os.Stdout, func() {}
	}
	return io.MultiWriter(os.Stdout, f), func() { _ = f.Close() }
}

func networkName(c *ClusterConfig) string {
	return "easyshift-" + c.Name
}

func masterISOPath(sc *StageContext) string {
	return filepath.Join(clusterDir(sc), "master.iso")
}

// rhcosLiveISOPath is the cached RHCOS live ISO for the cluster's OCP
// version, shared across clusters of the same version. The filename is
// stable (the actual RHCOS version is determined at download time from the
// installer's stream metadata, so we don't encode it here).
func rhcosLiveISOPath(sc *StageContext) string {
	return filepath.Join(RHCOSCacheDir(sc.Config.ConfigDir, sc.Cluster.OCPVersion), "rhcos-live.iso")
}

// masterISOVolName is the default-pool volume name for a cluster's master ISO.
func masterISOVolName(c *ClusterConfig) string {
	return "easyshift-" + c.Name + "-master.iso"
}

// createMasterVM provisions a single master VM that boots from the embedded
// SNO ISO (bootstrap-in-place). The disk is empty until first boot; the
// live ISO's coreos-installer writes RHCOS onto it on first boot.
func createMasterVM(ctx context.Context, sc *StageContext, index int) error {
	c := sc.Cluster
	role := fmt.Sprintf("master-%d", index)
	vmName := fmt.Sprintf("%s-%s", role, c.Name)

	mac := macFor(c, role)
	return sc.Deps.VM.Create(ctx, VMSpec{
		Name:        vmName,
		MemoryMiB:   c.MasterRAM,
		VCPUs:       c.MasterCPUs,
		DiskSizeGiB: c.MasterDiskGB,
		StoragePool: c.StoragePool,
		MAC:         mac,
		NetworkArg:  networkArgFor(c, mac),
		BootISO:     c.BootISOVolPath,
	})
}

func macFor(c *ClusterConfig, role string) string {
	for i, mac := range c.MACAddresses {
		if i < c.MasterCount && role == fmt.Sprintf("master-%d", i) {
			return mac
		}
		if i >= c.MasterCount && role == fmt.Sprintf("worker-%d", i-c.MasterCount) {
			return mac
		}
	}
	return ""
}

// networkArgFor constructs the `virt-install --network` argument appropriate
// for the cluster's NetworkMode. model=virtio is force-set because
// virt-install's default (e1000) hangs the Tx queue under sustained load on
// modern kernels (`e1000 ens3: Detected Tx Unit Hang`), making the VM
// completely unreachable mid-bootstrap.
func networkArgFor(c *ClusterConfig, mac string) string {
	if c.NetworkMode == NetworkModeBridge {
		// Attach to the existing host bridge; VMs get a LAN IP from the
		// upstream router's DHCP and can reach the host.
		return fmt.Sprintf("bridge=%s,mac=%s,model=virtio", c.Bridge, mac)
	}
	return fmt.Sprintf("network=%s,mac=%s,model=virtio", networkName(c), mac)
}

// LibvirtSystemURI is the libvirt connection URI that easyshift uses for
// every VM and network operation. It's a constant today; future versions
// may make it configurable.
const LibvirtSystemURI = "qemu:///system"

// checkLibvirtAccess runs a cheap probe against qemu:///system to detect
// the most common deploy-time problems (libvirtd not running, user not in
// the libvirt group, polkit denial). It is called from Preflight on every
// stage that subsequently talks to libvirt.
func checkLibvirtAccess(ctx context.Context, cmd CommandRunner) error {
	if _, err := cmd.Run(ctx, "virsh", "-c", LibvirtSystemURI, "list", "--name"); err != nil {
		return fmt.Errorf("libvirt at %s is not reachable: %w\n  hint: ensure libvirtd/virtqemud is running and your user is in the 'libvirt' group", LibvirtSystemURI, err)
	}
	return nil
}

// checkStoragePool verifies the named libvirt pool exists and is running,
// since both the master disk and the boot ISO are created there.
func checkStoragePool(ctx context.Context, cmd CommandRunner, pool string) error {
	out, err := cmd.Run(ctx, "virsh", "-c", LibvirtSystemURI, "pool-info", "--pool", pool)
	if err != nil {
		return fmt.Errorf("libvirt storage pool %q not found: %w\n  hint: pass --storage-pool <name> to use an existing pool (see `virsh pool-list --all`), or create it: virsh pool-define-as %s dir --target /var/lib/libvirt/images && virsh pool-autostart %s && virsh pool-start %s",
			pool, err, pool, pool, pool)
	}
	if !strings.Contains(string(out), "running") {
		return fmt.Errorf("libvirt storage pool %q exists but is not running\n  hint: virsh pool-start %s", pool, pool)
	}
	return nil
}

// downloadTarball downloads a .tar.gz, extracts it into destDir, then removes
// the tarball. Uses CommandRunner so tests don't need a real `tar` binary.
func downloadTarball(ctx context.Context, sc *StageContext, url, destDir string) error {
	tmp := filepath.Join(destDir, "_download.tar.gz")
	if err := sc.Deps.Download.Download(ctx, url, tmp); err != nil {
		return err
	}
	defer os.Remove(tmp)
	if _, err := sc.Deps.Cmd.Run(ctx, "tar", "xzf", tmp, "-C", destDir); err != nil {
		return fmt.Errorf("extract %s: %w", tmp, err)
	}
	return nil
}
