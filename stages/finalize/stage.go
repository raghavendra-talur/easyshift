// Package finalize flips the cluster's State to running once the install
// has completed.
package finalize

import (
	"context"

	"github.com/TheEasyShift/easyshift/config"
	"github.com/TheEasyShift/easyshift/interfaces"
)

// Stage marks the cluster as running.
type Stage struct{}

// New returns the finalize stage. No dependencies.
func New() *Stage { return &Stage{} }

func (*Stage) Name() string { return "finalize" }

func (*Stage) Apply(_ context.Context, sc *interfaces.StageContext) error {
	sc.Cluster.State = config.ClusterStateRunning
	return sc.Config.Save()
}

func (*Stage) Rollback(_ context.Context, sc *interfaces.StageContext) error {
	sc.Cluster.State = config.ClusterStateCreating
	return sc.Config.Save()
}
