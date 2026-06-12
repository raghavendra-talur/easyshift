package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/TheEasyShift/easyshift/config"
)

// Convergence tuning. Vars (not consts) so tests shrink them.
var (
	apiWaitTimeout       = 15 * time.Minute // SNO cold boot is slow
	convergeTimeout      = 10 * time.Minute // after the API is up
	convergePollInterval = 10 * time.Second
)

// convergeAfterStart waits for the API to come back after a VM boot, then
// approves pending node CSRs (the kubelet's client cert can expire while the
// VM is off; until the renewal CSRs are approved the node never rejoins)
// until the node reports Ready. The stopped-cluster analog of the approver
// that runs during install.
func (cm *ClusterManager) convergeAfterStart(ctx context.Context, c *config.ClusterConfig) error {
	oc := filepath.Join(config.BinariesDir(cm.cfg.ConfigDir, c.OCPVersion), "oc")
	kubeconfig := filepath.Join(config.ClusterDir(cm.cfg.ConfigDir, c.Name), "auth", "kubeconfig")

	if err := cm.waitForAPI(ctx, oc, kubeconfig); err != nil {
		return err
	}

	csrCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { _ = cm.deps.CSR.Run(csrCtx, oc, kubeconfig) }()

	deadline := time.Now().Add(convergeTimeout)
	for {
		ready := cm.nodeReady(ctx, oc, kubeconfig)
		pending := cm.csrsPending(ctx, oc, kubeconfig)
		if ready && !pending {
			logrus.Infof("cluster %s converged: node Ready, no pending CSRs", c.Name)
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("cluster %s did not converge within %s (node Ready=%t, CSRs pending=%t); "+
				"it may still recover on its own — check `easyshift status %s`",
				c.Name, convergeTimeout, ready, pending, c.Name)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(convergePollInterval):
		}
	}
}

// waitForAPI polls /readyz until the kube-apiserver answers.
func (cm *ClusterManager) waitForAPI(ctx context.Context, oc, kubeconfig string) error {
	deadline := time.Now().Add(apiWaitTimeout)
	for {
		if _, err := cm.deps.Cmd.Run(ctx, oc, "--kubeconfig", kubeconfig, "get", "--raw", "/readyz"); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("API did not become ready within %s after VM start", apiWaitTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(convergePollInterval):
		}
	}
}

// nodeReady reports whether every node's Ready condition is True.
func (cm *ClusterManager) nodeReady(ctx context.Context, oc, kubeconfig string) bool {
	out, err := cm.deps.Cmd.Run(ctx, oc, "--kubeconfig", kubeconfig, "get", "nodes",
		"-o", `jsonpath={.items[*].status.conditions[?(@.type=="Ready")].status}`)
	if err != nil {
		return false
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return false
	}
	for _, f := range fields {
		if f != "True" {
			return false
		}
	}
	return true
}

// csrsPending reports whether `oc get csr` lists any Pending request.
func (cm *ClusterManager) csrsPending(ctx context.Context, oc, kubeconfig string) bool {
	out, err := cm.deps.Cmd.Run(ctx, oc, "--kubeconfig", kubeconfig, "get", "csr")
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "Pending")
}
