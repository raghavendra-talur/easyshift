package verifymasterip

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/raghavendra-talur/easyshift/config"
	"github.com/raghavendra-talur/easyshift/interfaces"
	"github.com/raghavendra-talur/easyshift/providers/fakes"
)

const (
	testMAC    = "52:54:00:de:ad:02"
	testWantIP = "192.168.50.236"
	testWrong  = "192.168.50.140"
)

// fastStage returns a stage with tiny timing so tests don't actually wait.
func fastStage(host interfaces.HostInspector) *Stage {
	s := New(host)
	s.timeout = 60 * time.Millisecond
	s.poll = 2 * time.Millisecond
	s.dial = time.Millisecond
	return s
}

func bridgeCtx() *interfaces.StageContext {
	return &interfaces.StageContext{
		Cluster: &config.ClusterConfig{
			Name:        "dr1",
			Domain:      "example.test",
			NetworkMode: config.NetworkModeBridge,
			MasterMAC:   testMAC,
			MasterIP:    testWantIP,
		},
	}
}

// TestApply_ConfirmsReservedIP: the MAC is at the reserved IP and it answers,
// so the stage succeeds.
func TestApply_ConfirmsReservedIP(t *testing.T) {
	host := &fakes.HostInspector{
		ARPTable: map[string]string{testMAC: testWantIP},
		// TCPReachable empty -> everything reachable, so DialTCP(wantIP) == nil.
	}
	if err := fastStage(host).Apply(context.Background(), bridgeCtx()); err != nil {
		t.Fatalf("expected success when node is at the reserved IP, got %v", err)
	}
}

// TestApply_AbortsOnWrongIP: the node grabbed a different (pool) address that
// is itself live — the .140 failure mode. The stage must fail fast and name
// both the wrong and the expected IP.
func TestApply_AbortsOnWrongIP(t *testing.T) {
	host := &fakes.HostInspector{
		ARPTable: map[string]string{testMAC: testWrong},
		TCPReachable: map[string]error{
			testWantIP + ":22": errors.New("no route to host"), // reserved IP silent
			testWrong + ":22":  nil,                            // wrong IP answers
		},
	}
	err := fastStage(host).Apply(context.Background(), bridgeCtx())
	if err == nil {
		t.Fatal("expected an abort when the node came up on the wrong IP")
	}
	if !strings.Contains(err.Error(), testWrong) || !strings.Contains(err.Error(), testWantIP) {
		t.Errorf("error should name both the wrong IP %s and reserved IP %s: %v", testWrong, testWantIP, err)
	}
}

// TestApply_TimesOutWhenReservedIPNeverComesUp: neither the reserved IP nor
// the MAC ever appears (node silent / on an unknown address). The stage gives
// up after the timeout with an actionable message rather than hanging.
func TestApply_TimesOutWhenReservedIPNeverComesUp(t *testing.T) {
	host := &fakes.HostInspector{
		ARPTable:     map[string]string{}, // MAC never resolves
		TCPReachable: map[string]error{testWantIP + ":22": errors.New("timeout")},
	}
	err := fastStage(host).Apply(context.Background(), bridgeCtx())
	if err == nil {
		t.Fatal("expected a timeout error when the reserved IP never comes up")
	}
	if !strings.Contains(err.Error(), "did not come up") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestApply_SkipsNATMode: NAT mode must short-circuit without touching the
// host (libvirt's reservation is deterministic). A host wired to error proves
// it is never called.
func TestApply_SkipsNATMode(t *testing.T) {
	host := &fakes.HostInspector{Err: errors.New("host must not be consulted in NAT mode")}
	sc := bridgeCtx()
	sc.Cluster.NetworkMode = config.NetworkModeNAT
	if err := fastStage(host).Apply(context.Background(), sc); err != nil {
		t.Fatalf("NAT mode should skip the check, got %v", err)
	}
}
