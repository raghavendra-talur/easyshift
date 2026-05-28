package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/raghavendra-talur/easyshift/interfaces"

	"github.com/sirupsen/logrus"
)

// Runner walks an ordered list of Stages, persisting progress to state.json
// after each successful Apply, so that re-invocations resume from the last
// completed stage. Rollback walks the same list in reverse and only undoes
// stages that the on-disk state shows as applied.
type Runner struct {
	// configDir is the root under which each cluster's state.json lives.
	configDir string
	// now is injectable for deterministic tests.
	now func() time.Time
}

// NewRunner returns a Runner rooted at configDir.
func NewRunner(configDir string) *Runner {
	return &Runner{
		configDir: configDir,
		now:       func() time.Time { return time.Now().UTC() },
	}
}

// Preflight runs each stage's Preflight check (if any), aggregating all
// failures into a single error. Stages without a Preflight method are
// skipped. Preflight is independent of state.json — it is safe to call on
// a fresh or partially-applied cluster.
func (r *Runner) Preflight(ctx context.Context, sc *interfaces.StageContext, stages []interfaces.Stage) error {
	var errs []error
	for _, s := range stages {
		p, ok := s.(interfaces.Preflighter)
		if !ok {
			continue
		}
		if err := p.Preflight(ctx, sc); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", s.Name(), err))
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return errors.Join(errs...)
}

// Apply executes each stage in order, skipping any already recorded as
// applied in state.json. On the first failing stage, Apply returns the
// error without recording the stage as applied.
func (r *Runner) Apply(ctx context.Context, sc *interfaces.StageContext, stages []interfaces.Stage) error {
	state, err := r.loadState(sc.Cluster.Name)
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	for _, s := range stages {
		if _, done := state.Stages[s.Name()]; done {
			logrus.Debugf("stage %q already applied; skipping", s.Name())
			continue
		}
		logrus.Infof("applying stage %q", s.Name())
		if err := s.Apply(ctx, sc); err != nil {
			return fmt.Errorf("stage %s: %w", s.Name(), err)
		}
		state.Stages[s.Name()] = interfaces.StageRecord{
			AppliedAt: r.now(),
			Outcome:   interfaces.StageOutcomeOK,
		}
		if err := r.saveState(sc.Cluster.Name, state); err != nil {
			return fmt.Errorf("save state after %s: %w", s.Name(), err)
		}
	}
	return nil
}

// Rollback walks stages in reverse, invoking Rollback on each one that the
// on-disk state shows as applied. Errors from individual rollbacks are
// logged but do not stop the chain — rollback is best-effort cleanup.
func (r *Runner) Rollback(ctx context.Context, sc *interfaces.StageContext, stages []interfaces.Stage) error {
	state, err := r.loadState(sc.Cluster.Name)
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	for i := len(stages) - 1; i >= 0; i-- {
		s := stages[i]
		if _, done := state.Stages[s.Name()]; !done {
			continue
		}
		logrus.Infof("rolling back stage %q", s.Name())
		if err := s.Rollback(ctx, sc); err != nil {
			logrus.Warnf("rollback %s: %v", s.Name(), err)
		}
		delete(state.Stages, s.Name())
		if err := r.saveState(sc.Cluster.Name, state); err != nil {
			return fmt.Errorf("save state after rollback %s: %w", s.Name(), err)
		}
	}
	return nil
}

// statePath returns the path to a cluster's state.json.
func (r *Runner) statePath(clusterName string) string {
	return filepath.Join(r.configDir, "clusters", clusterName, "state.json")
}

func (r *Runner) loadState(clusterName string) (*interfaces.ClusterState, error) {
	path := r.statePath(clusterName)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &interfaces.ClusterState{Stages: map[string]interfaces.StageRecord{}}, nil
	}
	if err != nil {
		return nil, err
	}
	state := &interfaces.ClusterState{}
	if err := json.Unmarshal(data, state); err != nil {
		return nil, fmt.Errorf("parse state.json: %w", err)
	}
	if state.Stages == nil {
		state.Stages = map[string]interfaces.StageRecord{}
	}
	return state, nil
}

func (r *Runner) saveState(clusterName string, state *interfaces.ClusterState) error {
	path := r.statePath(clusterName)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	return os.WriteFile(path, data, 0o600)
}
