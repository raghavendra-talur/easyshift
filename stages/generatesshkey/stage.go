// Package generatesshkey creates the per-cluster SSH keypair embedded in
// install-config so the operator can SSH to RHCOS nodes as `core`.
package generatesshkey

import (
	"context"
	"os"
	"path/filepath"

	"github.com/TheEasyShift/easyshift/interfaces"
)

// Stage generates the cluster SSH keypair.
type Stage struct {
	cmd  interfaces.CommandRunner
	host interfaces.HostInspector
}

// New returns the generate-ssh-key stage.
func New(cmd interfaces.CommandRunner, host interfaces.HostInspector) *Stage {
	return &Stage{cmd: cmd, host: host}
}

func (*Stage) Name() string { return "generate-ssh-key" }

// Preflight verifies `ssh-keygen` is on PATH.
func (s *Stage) Preflight(_ context.Context, _ *interfaces.StageContext) error {
	return s.host.LookPath("ssh-keygen")
}

func (s *Stage) Apply(ctx context.Context, sc *interfaces.StageContext) error {
	keyPath := filepath.Join(sc.ClusterDir(), "id_rsa")
	if _, err := os.Stat(keyPath); err == nil {
		return nil
	}
	_, err := s.cmd.Run(ctx, "ssh-keygen", "-t", "rsa", "-b", "4096", "-f", keyPath, "-N", "", "-q")
	return err
}

func (*Stage) Rollback(_ context.Context, sc *interfaces.StageContext) error {
	keyPath := filepath.Join(sc.ClusterDir(), "id_rsa")
	_ = os.Remove(keyPath)
	_ = os.Remove(keyPath + ".pub")
	return nil
}
