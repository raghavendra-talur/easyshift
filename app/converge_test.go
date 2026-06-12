package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/TheEasyShift/easyshift/config"
	"github.com/TheEasyShift/easyshift/providers/fakes"
)

func newStartEnv(t *testing.T) (*ClusterManager, *fakes.Bundle) {
	t.Helper()
	deps, bundle := fakes.All()
	cfg := config.NewDefaultConfig(t.TempDir())
	cfg.Clusters = []*config.ClusterConfig{{
		Name: "dr1", Domain: "example.test", OCPVersion: "4.99.0",
		MasterCount: 1, State: config.ClusterStateStopped,
	}}
	return NewClusterManager(cfg, deps), bundle
}

func shortTimeouts(t *testing.T) {
	t.Helper()
	oldAPI, oldConv, oldPoll := apiWaitTimeout, convergeTimeout, convergePollInterval
	apiWaitTimeout, convergeTimeout, convergePollInterval = 200*time.Millisecond, 200*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { apiWaitTimeout, convergeTimeout, convergePollInterval = oldAPI, oldConv, oldPoll })
}

// TestStart_ConvergesAndApprovesCSRs: happy path — API answers, node Ready,
// the CSR approver was launched, no error.
func TestStart_ConvergesAndApprovesCSRs(t *testing.T) {
	shortTimeouts(t)
	mgr, bundle := newStartEnv(t)

	if err := mgr.Start(context.Background(), "dr1"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !bundle.CSR.WasStarted() {
		t.Error("CSR approver should run during start convergence")
	}
	if got := mgr.cfg.Clusters[0].State; got != config.ClusterStateRunning {
		t.Errorf("state = %q, want running", got)
	}
}

// TestStart_TimesOutWhenNodeNotReady: node stays NotReady -> Start returns an
// error naming the condition, but the cluster stays marked running (VMs are
// up; it may still recover).
func TestStart_TimesOutWhenNodeNotReady(t *testing.T) {
	shortTimeouts(t)
	mgr, bundle := newStartEnv(t)
	bundle.Cmd.RunFunc = func(_ string, args []string) ([]byte, error) {
		for _, a := range args {
			if strings.Contains(a, `?(@.type=="Ready")`) {
				return []byte("False"), nil
			}
		}
		return nil, nil // readyz + csr checks succeed
	}

	err := mgr.Start(context.Background(), "dr1")
	if err == nil || !strings.Contains(err.Error(), "did not converge") {
		t.Fatalf("want converge timeout error, got %v", err)
	}
	if got := mgr.cfg.Clusters[0].State; got != config.ClusterStateRunning {
		t.Errorf("state = %q, want running despite convergence timeout", got)
	}
}

// TestStart_TimesOutWhenAPINeverUp: readyz keeps failing -> API wait error.
func TestStart_TimesOutWhenAPINeverUp(t *testing.T) {
	shortTimeouts(t)
	mgr, bundle := newStartEnv(t)
	bundle.Cmd.RunFunc = func(_ string, args []string) ([]byte, error) {
		for _, a := range args {
			if a == "/readyz" {
				return nil, context.DeadlineExceeded
			}
		}
		return nil, nil
	}

	err := mgr.Start(context.Background(), "dr1")
	if err == nil || !strings.Contains(err.Error(), "did not become ready") {
		t.Fatalf("want API wait error, got %v", err)
	}
}
