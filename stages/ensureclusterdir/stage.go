// Package ensureclusterdir creates (and on rollback removes) the cluster's
// working directory for openshift-install artifacts.
package ensureclusterdir

import (
	"context"
	"os"

	"github.com/TheEasyShift/easyshift/interfaces"
)

// Stage ensures the per-cluster working directory exists.
type Stage struct{}

// New returns the ensure-cluster-dir stage. No dependencies.
func New() *Stage { return &Stage{} }

func (*Stage) Name() string { return "ensure-cluster-dir" }

func (*Stage) Apply(_ context.Context, sc *interfaces.StageContext) error {
	return os.MkdirAll(sc.ClusterDir(), 0o700)
}

func (*Stage) Rollback(_ context.Context, sc *interfaces.StageContext) error {
	return os.RemoveAll(sc.ClusterDir())
}
