// Package waitforinstall blocks on `openshift-install wait-for
// install-complete` while running CSR-approver and hostname-injector
// goroutines, and retries on the installer's 40-min initialization timeout.
package waitforinstall

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/raghavendra-talur/easyshift/config"
	"github.com/raghavendra-talur/easyshift/interfaces"
)

// waitForInstallRetries caps how many times we re-invoke wait-for
// install-complete on its 40-min timeout. SNO bootstrap-in-place with an
// mco-firstboot ostree pivot routinely exceeds the first window; the
// installer itself tells you to re-run, so we automate it.
const waitForInstallRetries = 3

// vmWatchdogInterval is how often the watchdog polls the master VM state
// during the wait. SNO bootstrap-in-place reboots the VM (live ISO -> installed
// disk) and the MCO firstboot can trigger more reboots; libvirt is known not to
// reliably honor the guest reboot for this flow, leaving the domain in shut-off
// state. The watchdog restarts it so the install resumes, matching the
// community-prescribed pattern for KVM SNO.
const vmWatchdogInterval = 15 * time.Second

// Stage waits for the cluster to finish installing.
type Stage struct {
	installer interfaces.Installer
	csr       interfaces.CSRApprover
	hostname  interfaces.HostnameInjector
	vm        interfaces.VMManager
}

// New returns the wait-for-install stage.
func New(installer interfaces.Installer, csr interfaces.CSRApprover, hostname interfaces.HostnameInjector, vm interfaces.VMManager) *Stage {
	return &Stage{installer: installer, csr: csr, hostname: hostname, vm: vm}
}

func (*Stage) Name() string { return "wait-for-install" }

func (s *Stage) Apply(ctx context.Context, sc *interfaces.StageContext) error {
	helperCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	vmName := fmt.Sprintf("master-0-%s", sc.Cluster.Name)

	csrDone := make(chan struct{})
	go func() {
		defer close(csrDone)
		_ = s.csr.Run(helperCtx, sc.OCBinaryPath(), sc.KubeconfigPath())
	}()

	hostnameDone := make(chan struct{})
	if sc.Cluster.NetworkMode == config.NetworkModeBridge && sc.Cluster.MasterIP != "" {
		go func() {
			defer close(hostnameDone)
			_ = s.hostname.Run(helperCtx,
				sc.Cluster.MasterIP,
				sshKeyPath(sc),
				config.MasterHostname(sc.Cluster))
		}()
	} else {
		close(hostnameDone)
	}

	watchdogDone := make(chan struct{})
	go func() {
		defer close(watchdogDone)
		s.watchdog(helperCtx, vmName)
	}()

	spec, closeFn := s.installerWaitSpec(sc)
	defer closeFn()
	err := s.waitWithRetry(ctx, sc, spec)
	cancel()
	<-csrDone
	<-hostnameDone
	<-watchdogDone
	return err
}

func (*Stage) Rollback(_ context.Context, _ *interfaces.StageContext) error { return nil }

// watchdog polls the master VM and restarts it whenever libvirt drops it into
// shut-off, which happens because the SNO bootstrap-in-place reboot (live ISO
// -> installed disk, then MCO firstboot) doesn't reliably trigger libvirt's
// on_reboot handling. Runs until ctx is canceled (i.e. the wait completes).
func (s *Stage) watchdog(ctx context.Context, vmName string) {
	ticker := time.NewTicker(vmWatchdogInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			running, err := s.vm.IsRunning(ctx, vmName)
			if err != nil || running {
				continue
			}
			logrus.Warnf("VM %s is shut off during install; restarting (libvirt did not honor the guest reboot)", vmName)
			if serr := s.vm.Start(ctx, vmName); serr != nil {
				logrus.Warnf("restart VM %s: %v", vmName, serr)
			}
		}
	}
}

func (s *Stage) waitWithRetry(ctx context.Context, _ *interfaces.StageContext, spec interfaces.InstallerSpec) error {
	var err error
	for attempt := 1; attempt <= waitForInstallRetries; attempt++ {
		err = s.installer.WaitForInstallComplete(ctx, spec)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return err
		}
		// exit status 6 == "timed out waiting for the condition"; the cluster
		// keeps progressing and the next call resumes. Other exits are
		// unrecoverable — surface them immediately.
		if !isInstallerTimeoutError(err) {
			return err
		}
		logrus.Warnf("wait-for install-complete timed out (attempt %d/%d), retrying: %v",
			attempt, waitForInstallRetries, err)
	}
	return fmt.Errorf("wait-for install-complete: gave up after %d timeouts: %w",
		waitForInstallRetries, err)
}

// installerWaitSpec tees the installer's output to stdout + the easyshift log
// file so a backgrounded run can be inspected later. The close func releases
// the log handle.
func (s *Stage) installerWaitSpec(sc *interfaces.StageContext) (interfaces.InstallerSpec, func()) {
	spec := sc.InstallerSpec()
	out, closeFn := openTeeWriter(sc.Config.LogFile)
	spec.Out = out
	return spec, closeFn
}

func openTeeWriter(logPath string) (io.Writer, func()) {
	if logPath == "" {
		return os.Stdout, func() {}
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		logrus.Debugf("open installer tee log %s: %v", logPath, err)
		return os.Stdout, func() {}
	}
	return io.MultiWriter(os.Stdout, f), func() { _ = f.Close() }
}

func sshKeyPath(sc *interfaces.StageContext) string {
	return sc.ClusterDir() + "/id_rsa"
}

// isInstallerTimeoutError matches openshift-install's exit-status-6 wrapper.
func isInstallerTimeoutError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "exit status 6")
}
