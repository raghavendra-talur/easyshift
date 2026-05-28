// Package registercluster adds the cluster to config.json with State=creating
// so partial builds are visible to `easyshift list` and removable via delete.
package registercluster

import (
	"context"

	"github.com/raghavendra-talur/easyshift/config"
	"github.com/raghavendra-talur/easyshift/interfaces"
)

// Stage registers the cluster in the global config.
type Stage struct{}

// New returns the register-cluster stage. It has no dependencies.
func New() *Stage { return &Stage{} }

func (*Stage) Name() string { return "register-cluster" }

func (*Stage) Apply(_ context.Context, sc *interfaces.StageContext) error {
	for _, c := range sc.Config.Clusters {
		if c.Name == sc.Cluster.Name {
			return nil
		}
	}
	sc.Cluster.State = config.ClusterStateCreating
	sc.Config.Clusters = append(sc.Config.Clusters, sc.Cluster)
	return sc.Config.Save()
}

func (*Stage) Rollback(_ context.Context, sc *interfaces.StageContext) error {
	for i, c := range sc.Config.Clusters {
		if c.Name == sc.Cluster.Name {
			sc.Config.Clusters = append(sc.Config.Clusters[:i], sc.Config.Clusters[i+1:]...)
			break
		}
	}
	return sc.Config.Save()
}
