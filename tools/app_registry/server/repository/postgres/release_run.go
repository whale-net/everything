package postgres

import (
	"context"
	"errors"

	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// releaseRunRepo implements repository.ReleaseRunRepository against
// `release_run`/`release_run_target` (migration 016, NFR4, issue #887). See
// that migration's doc comment and repository.ReleaseRunRepository's doc
// comment for the shape: NOT SCD2 -- release_run is written once,
// release_run_target is mutated in place per transition (matching
// artifactRepo's mutation style, not promotionRepo's SCD2 open/close
// style).
//
// Scaffold only -- every method below is a stub. Full implementation lands
// in the Implementation phase (issue #887's "Implementation scope").
type releaseRunRepo struct{ ex dbtx }

func (r *releaseRunRepo) CreateReleaseRun(ctx context.Context, run repository.ReleaseRun, targets []repository.ReleaseRunTarget) (*repository.ReleaseRun, []repository.ReleaseRunTarget, error) {
	return nil, nil, errors.New("not implemented")
}

func (r *releaseRunRepo) UpdateTargetState(ctx context.Context, releaseRunTargetID string, newState repository.ReleaseRunTargetState, buildID, errorDetail string) error {
	return errors.New("not implemented")
}

func (r *releaseRunRepo) GetReleaseRun(ctx context.Context, releaseRunID string) (*repository.ReleaseRun, []repository.ReleaseRunTarget, error) {
	return nil, nil, errors.New("not implemented")
}

func (r *releaseRunRepo) ListReleaseRunsByTarget(ctx context.Context, ownerFullName string) ([]repository.ReleaseRun, error) {
	return nil, errors.New("not implemented")
}
