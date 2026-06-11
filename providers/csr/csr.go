package csr

import (
	"context"
	"strings"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/TheEasyShift/easyshift/interfaces"
)

// csrPollInterval governs how often the OCCSRApprover sweeps for Pending CSRs.
// Tunable for tests but otherwise a hard-coded production value.
var csrPollInterval = 10 * time.Second

// OCCSRApprover is the real CSRApprover that shells out to `oc`. The oc
// binary path comes in on every Run call so the same approver works across
// clusters and OCP versions.
type OCCSRApprover struct {
	cmd interfaces.CommandRunner
}

// NewOCCSRApprover returns an approver backed by cmd.
func NewOCCSRApprover(cmd interfaces.CommandRunner) *OCCSRApprover {
	return &OCCSRApprover{cmd: cmd}
}

// Run loops until ctx is cancelled, sweeping for CSRs and approving them.
// Individual approval failures (e.g. "already approved") are logged at debug
// level and do not stop the loop.
func (a *OCCSRApprover) Run(ctx context.Context, ocPath, kubeconfigPath string) error {
	t := time.NewTicker(csrPollInterval)
	defer t.Stop()
	a.sweep(ctx, ocPath, kubeconfigPath)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			a.sweep(ctx, ocPath, kubeconfigPath)
		}
	}
}

func (a *OCCSRApprover) sweep(ctx context.Context, ocPath, kubeconfigPath string) {
	out, err := a.cmd.Run(ctx, ocPath, "--kubeconfig", kubeconfigPath, "get", "csr", "-o", "name")
	if err != nil {
		logrus.Debugf("csr list: %v", err)
		return
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Strip the kind prefix ("certificatesigningrequest.certificates.k8s.io/csr-foo").
		if idx := strings.LastIndex(line, "/"); idx >= 0 {
			line = line[idx+1:]
		}
		if _, err := a.cmd.Run(ctx, ocPath, "--kubeconfig", kubeconfigPath, "adm", "certificate", "approve", line); err != nil {
			logrus.Debugf("csr approve %s: %v", line, err)
		}
	}
}
