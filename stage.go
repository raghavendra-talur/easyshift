package easyshift

import (
	"context"
	"time"
)

// StageOutcome records whether a stage's Apply succeeded.
type StageOutcome string

const (
	StageOutcomeOK     StageOutcome = "ok"
	StageOutcomeFailed StageOutcome = "failed"
)

// StageRecord is the persisted result of one stage Apply.
type StageRecord struct {
	AppliedAt time.Time    `json:"appliedAt"`
	Outcome   StageOutcome `json:"outcome"`
}

// ClusterState is the on-disk record of which stages have been applied to a
// cluster. It is stored at <configDir>/clusters/<name>/state.json so that
// re-running create can resume from the last failed stage, and delete can
// roll back only what was actually applied.
type ClusterState struct {
	Stages map[string]StageRecord `json:"stages"`
}

// StageContext bundles everything a Stage needs to act. Stages should treat
// Cluster and Config as mutable; the runner persists the resulting changes.
type StageContext struct {
	Cluster *ClusterConfig
	Config  *Config
	Deps    Deps
}

// Stage is one idempotent step in the cluster lifecycle. Apply must be
// safe to retry after a partial failure (the runner skips stages already
// recorded as applied, but Apply should also tolerate observing partial
// real-world state). Rollback undoes whatever Apply produced; it is only
// invoked for stages that successfully applied.
type Stage interface {
	Name() string
	Apply(ctx context.Context, sc *StageContext) error
	Rollback(ctx context.Context, sc *StageContext) error
}

// Preflighter is an optional interface a Stage can implement to declare
// checks that must pass *before* any stage runs. Preflight checks should
// only validate things that are independent of pipeline state — host
// services, network reachability, required binaries on PATH — not artifacts
// produced by earlier stages. The runner invokes every stage's Preflight
// up front and aggregates the failures, so the user sees ALL problems at
// once instead of one Apply attempt at a time.
type Preflighter interface {
	Preflight(ctx context.Context, sc *StageContext) error
}
