package fake

import (
	"context"
	"errors"

	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// releaseRunFake implements repository.ReleaseRunRepository against
// Registry.state's ReleaseRuns/ReleaseRunTargets maps -- see fake.go's
// state doc comment and postgres/release_run.go's doc comment for the
// shape this mirrors (NOT SCD2; release_run_target mutated in place).
//
// Scaffold only -- every method below is a stub. Full implementation lands
// in the Implementation phase (issue #887's "Implementation scope").
type releaseRunFake struct{ r *Registry }

func (f releaseRunFake) CreateReleaseRun(ctx context.Context, run repository.ReleaseRun, targets []repository.ReleaseRunTarget) (*repository.ReleaseRun, []repository.ReleaseRunTarget, error) {
	return nil, nil, errors.New("not implemented")
}

func (f releaseRunFake) UpdateTargetState(ctx context.Context, releaseRunTargetID string, newState repository.ReleaseRunTargetState, buildID, errorDetail string) error {
	return errors.New("not implemented")
}

func (f releaseRunFake) GetReleaseRun(ctx context.Context, releaseRunID string) (*repository.ReleaseRun, []repository.ReleaseRunTarget, error) {
	return nil, nil, errors.New("not implemented")
}

func (f releaseRunFake) ListReleaseRunsByTarget(ctx context.Context, ownerFullName string) ([]repository.ReleaseRun, error) {
	return nil, errors.New("not implemented")
}
