package host

import (
	"context"
	"strings"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/raghavendra-talur/easyshift/interfaces"
)

// hostnamePollInterval governs how often the SSHHostnameInjector polls.
// Tunable for tests but otherwise a hard-coded production value. 30s is
// long enough to avoid hammering the VM during the install (every set
// kicks NetworkManager and a few other services) but short enough to set
// the hostname inside node-valid-hostname.service's 5-min TimeoutSec
// after a reboot.
var hostnamePollInterval = 30 * time.Second

// SSHHostnameInjector is the real HostnameInjector. It SSHes into the
// master VM and runs `hostnamectl set-hostname <name>` ONLY when the
// current hostname is wrong. Without the conditional check, setting on
// every tick caused enough systemd/NM churn during long installs to
// interrupt mco-firstboot's ostree pivot.
//
// The injector must keep polling because the VM goes through phases —
// live ISO → coreos-installer install → reboot into installed RHCOS, and
// possibly an mco-firstboot ostree pivot reboot on top — and each reboot
// resets the hostname to localhost.localdomain. The poll keeps re-fixing
// it after each reboot, while no-op'ing when the hostname is already
// correct.
type SSHHostnameInjector struct {
	cmd interfaces.CommandRunner
}

// NewSSHHostnameInjector returns an injector backed by cmd.
func NewSSHHostnameInjector(cmd interfaces.CommandRunner) *SSHHostnameInjector {
	return &SSHHostnameInjector{cmd: cmd}
}

// Run loops until ctx is cancelled. Connect failures (VM is rebooting, SSH
// not up yet) are logged at debug level and the loop continues.
func (h *SSHHostnameInjector) Run(ctx context.Context, ip, sshKeyPath, hostname string) error {
	t := time.NewTicker(hostnamePollInterval)
	defer t.Stop()
	h.try(ctx, ip, sshKeyPath, hostname)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			h.try(ctx, ip, sshKeyPath, hostname)
		}
	}
}

// try runs a shell snippet on the VM that checks the current hostname and
// only invokes hostnamectl if it's wrong. Idempotent — if the hostname is
// already correct, this is a no-op on the VM side as well.
func (h *SSHHostnameInjector) try(ctx context.Context, ip, sshKeyPath, hostname string) {
	remote := `cur=$(hostname); if [ "$cur" != "` + hostname + `" ]; then sudo hostnamectl set-hostname ` + hostname + `; fi`
	_, err := h.cmd.Run(ctx,
		"ssh",
		"-i", sshKeyPath,
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "ConnectTimeout=8",
		"-o", "LogLevel=ERROR",
		"core@"+ip,
		remote,
	)
	if err != nil && !strings.Contains(err.Error(), "context canceled") {
		logrus.Debugf("hostname inject %s: %v", ip, err)
	}
}
