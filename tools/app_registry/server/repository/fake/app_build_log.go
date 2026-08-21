package fake

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// appBuildLogFake implements repository.AppBuildLogRepository against
// Registry.state.AppBuildLogs -- see fake.go's state doc comment and
// postgres/app_build_log.go's doc comment for the shape this mirrors:
// SCD2 (valid_from/valid_to), written UNCONDITIONALLY on every
// RecordBuildLog call (no same-content-skip branch, unlike
// app_manifest_history's write path).
type appBuildLogFake struct{ r *Registry }

// RecordBuildLog mirrors postgres's appBuildLogRepo.RecordBuildLog: closes
// any currently-open row for ownerID, then opens a new one unconditionally.
func (f appBuildLogFake) RecordBuildLog(ctx context.Context, ownerID string, kind repository.ArtifactKind, gitSHA, buildID string) (*repository.AppBuildLog, error) {
	if ownerID == "" {
		return nil, fmt.Errorf("%w: owner id is required", repository.ErrInvalidArgument)
	}
	if kind != repository.ArtifactKindImage && kind != repository.ArtifactKindChart {
		return nil, fmt.Errorf("%w: kind must be image or chart, got %q", repository.ErrInvalidArgument, kind)
	}
	if gitSHA == "" {
		return nil, fmt.Errorf("%w: git sha is required", repository.ErrInvalidArgument)
	}
	if buildID == "" {
		return nil, fmt.Errorf("%w: build id is required", repository.ErrInvalidArgument)
	}

	now := timeNow()
	for id, existing := range f.r.state.AppBuildLogs {
		if existing.OwnerID == ownerID && existing.ValidTo == nil {
			closedAt := now
			existing.ValidTo = &closedAt
			f.r.state.AppBuildLogs[id] = existing
		}
	}

	created := repository.AppBuildLog{
		AppBuildLogID: uuid.NewString(),
		OwnerID:       ownerID,
		Kind:          kind,
		GitSHA:        gitSHA,
		BuildID:       buildID,
		ValidFrom:     now,
	}
	f.r.state.AppBuildLogs[created.AppBuildLogID] = created
	return &created, nil
}

// GetCurrentBuildLog mirrors postgres's
// appBuildLogRepo.GetCurrentBuildLog -- the single open (ValidTo == nil)
// row for ownerID, or ErrNotFound.
func (f appBuildLogFake) GetCurrentBuildLog(ctx context.Context, ownerID string) (*repository.AppBuildLog, error) {
	for _, l := range f.r.state.AppBuildLogs {
		if l.OwnerID == ownerID && l.ValidTo == nil {
			out := l
			return &out, nil
		}
	}
	return nil, repository.ErrNotFound
}
