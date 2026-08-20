package fake

import (
	"context"
	"fmt"
	"sort"

	"github.com/google/uuid"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// releaseRunFake implements repository.ReleaseRunRepository against
// Registry.state's ReleaseRuns/ReleaseRunTargets maps -- see fake.go's
// state doc comment and postgres/release_run.go's doc comment for the
// shape this mirrors (NOT SCD2; release_run_target mutated in place).
type releaseRunFake struct{ r *Registry }

// legalReleaseRunTargetTransitions mirrors
// postgres.legalReleaseRunTargetTransitions -- see that map's doc comment
// for the transition table (ReleaseRunTargetState's doc comment,
// repository/models.go, is the source of truth both mirror).
var legalReleaseRunTargetTransitions = map[repository.ReleaseRunTargetState]map[repository.ReleaseRunTargetState]bool{
	repository.ReleaseRunTargetStateQueued: {
		repository.ReleaseRunTargetStateBuilding: true,
		repository.ReleaseRunTargetStateFailed:   true,
	},
	repository.ReleaseRunTargetStateBuilding: {
		repository.ReleaseRunTargetStatePublishing: true,
		repository.ReleaseRunTargetStateFailed:     true,
	},
	repository.ReleaseRunTargetStatePublishing: {
		repository.ReleaseRunTargetStateRecording: true,
		repository.ReleaseRunTargetStateFailed:    true,
	},
	repository.ReleaseRunTargetStateRecording: {
		repository.ReleaseRunTargetStateSucceeded: true,
		repository.ReleaseRunTargetStateFailed:    true,
	},
}

// CreateReleaseRun mirrors postgres's releaseRunRepo.CreateReleaseRun: one
// release_run row plus one release_run_target row per target, all starting
// ReleaseRunTargetStateQueued.
func (f releaseRunFake) CreateReleaseRun(ctx context.Context, run repository.ReleaseRun, targets []repository.ReleaseRunTarget) (*repository.ReleaseRun, []repository.ReleaseRunTarget, error) {
	if len(targets) == 0 {
		return nil, nil, fmt.Errorf("%w: release run must cover at least one target", repository.ErrInvalidArgument)
	}
	// Non-terminal-scoped duplicate check (issue #889, FR11, migration
	// 017) mirroring postgres's releaseRunRepo.CreateReleaseRun -- a prior
	// release_run sharing this workflow id only conflicts while at least
	// one of its targets hasn't reached succeeded/failed yet.
	for _, existing := range f.r.state.ReleaseRuns {
		if existing.TemporalWorkflowID != run.TemporalWorkflowID {
			continue
		}
		for _, t := range f.r.state.ReleaseRunTargets {
			if t.ReleaseRunID != existing.ReleaseRunID {
				continue
			}
			if t.State != repository.ReleaseRunTargetStateSucceeded && t.State != repository.ReleaseRunTargetStateFailed {
				return nil, nil, fmt.Errorf("%w: release run for workflow %s already in progress", repository.ErrAlreadyExists, run.TemporalWorkflowID)
			}
		}
	}

	run.ReleaseRunID = uuid.NewString()
	if run.CreatedAt.IsZero() {
		run.CreatedAt = timeNow()
	}
	f.r.state.ReleaseRuns[run.ReleaseRunID] = run

	outTargets := make([]repository.ReleaseRunTarget, 0, len(targets))
	for _, t := range targets {
		t.ReleaseRunTargetID = uuid.NewString()
		t.ReleaseRunID = run.ReleaseRunID
		t.State = repository.ReleaseRunTargetStateQueued
		t.StateChangedAt = timeNow()
		t.BuildID = ""
		t.ErrorDetail = ""
		f.r.state.ReleaseRunTargets[t.ReleaseRunTargetID] = t
		outTargets = append(outTargets, t)
	}

	created := run
	return &created, outTargets, nil
}

// UpdateTargetState mirrors postgres's releaseRunRepo.UpdateTargetState --
// same legalReleaseRunTargetTransitions table, same
// preserve-existing-buildID-when-empty behavior.
func (f releaseRunFake) UpdateTargetState(ctx context.Context, releaseRunTargetID string, newState repository.ReleaseRunTargetState, buildID, errorDetail string) error {
	existing, ok := f.r.state.ReleaseRunTargets[releaseRunTargetID]
	if !ok {
		return fmt.Errorf("%w: release run target %s", repository.ErrNotFound, releaseRunTargetID)
	}

	if !legalReleaseRunTargetTransitions[existing.State][newState] {
		return fmt.Errorf("%w: release run target %s is %q, cannot transition to %q",
			repository.ErrFailedPrecondition, releaseRunTargetID, existing.State, newState)
	}

	if buildID != "" {
		existing.BuildID = buildID
	}
	existing.State = newState
	existing.StateChangedAt = timeNow()
	existing.ErrorDetail = errorDetail
	f.r.state.ReleaseRunTargets[releaseRunTargetID] = existing
	return nil
}

// GetReleaseRun mirrors postgres's releaseRunRepo.GetReleaseRun.
func (f releaseRunFake) GetReleaseRun(ctx context.Context, releaseRunID string) (*repository.ReleaseRun, []repository.ReleaseRunTarget, error) {
	run, ok := f.r.state.ReleaseRuns[releaseRunID]
	if !ok {
		return nil, nil, repository.ErrNotFound
	}

	var targets []repository.ReleaseRunTarget
	for _, t := range f.r.state.ReleaseRunTargets {
		if t.ReleaseRunID == releaseRunID {
			targets = append(targets, t)
		}
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].ReleaseRunTargetID < targets[j].ReleaseRunTargetID })
	return &run, targets, nil
}

// ListReleaseRunsByTarget mirrors postgres's
// releaseRunRepo.ListReleaseRunsByTarget -- every release_run with at least
// one release_run_target row for ownerFullName, most-recent-first.
func (f releaseRunFake) ListReleaseRunsByTarget(ctx context.Context, ownerFullName string) ([]repository.ReleaseRun, error) {
	seen := map[string]bool{}
	var out []repository.ReleaseRun
	for _, t := range f.r.state.ReleaseRunTargets {
		if t.OwnerFullName != ownerFullName || seen[t.ReleaseRunID] {
			continue
		}
		run, ok := f.r.state.ReleaseRuns[t.ReleaseRunID]
		if !ok {
			continue
		}
		seen[t.ReleaseRunID] = true
		out = append(out, run)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ReleaseRunID > out[j].ReleaseRunID
	})
	return out, nil
}
