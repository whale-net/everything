package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// appBuildLogRepo implements repository.AppBuildLogRepository against
// `app_build_log` (migration 019, issue #923, FR8-FR12/FR14). See that
// migration's doc comment and repository.AppBuildLogRepository's doc
// comment for the shape: SCD2 (valid_from/valid_to) but written
// UNCONDITIONALLY -- there is no content-gating branch here, unlike
// appRepo.recordAppManifestSweep (app.go) against app_manifest_history.
type appBuildLogRepo struct{ ex dbtx }

const appBuildLogColumns = `app_build_log_id, owner_id, kind, git_sha, build_id, valid_from, valid_to`

func scanAppBuildLog(row pgx.Row) (repository.AppBuildLog, error) {
	var l repository.AppBuildLog
	var kind string
	if err := row.Scan(&l.AppBuildLogID, &l.OwnerID, &kind, &l.GitSHA, &l.BuildID, &l.ValidFrom, &l.ValidTo); err != nil {
		return repository.AppBuildLog{}, err
	}
	l.Kind = repository.ArtifactKind(kind)
	return l, nil
}

// RecordBuildLog implements
// repository.AppBuildLogRepository.RecordBuildLog -- the AGENTS.md SCD2
// "close and open" write path, unconditionally: unlike
// appRepo.recordAppManifestSweep, there is no "same content, just advance
// last_git_sha" branch, because this table has no last_git_sha column to
// advance and no content-equality check to skip on -- FR8 requires a new
// row on every call. Callers MUST invoke this inside Registry.WithTx (see
// AppBuildLogRepository.RecordBuildLog's doc comment) so the close+open
// pair is atomic.
func (r *appBuildLogRepo) RecordBuildLog(ctx context.Context, ownerID string, kind repository.ArtifactKind, gitSHA, buildID string) (*repository.AppBuildLog, error) {
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

	if _, err := r.ex.Exec(ctx, `UPDATE app_build_log SET valid_to = NOW() WHERE owner_id = $1 AND valid_to IS NULL`, ownerID); err != nil {
		return nil, fmt.Errorf("close current app build log for %s: %w", ownerID, err)
	}

	row := r.ex.QueryRow(ctx, `
		INSERT INTO app_build_log (owner_id, kind, git_sha, build_id)
		VALUES ($1, $2, $3, $4)
		RETURNING `+appBuildLogColumns,
		ownerID, string(kind), gitSHA, buildID)
	created, err := scanAppBuildLog(row)
	if err != nil {
		if de, ok := translatePgError(err, fmt.Sprintf("app build log for %s referencing build %s", ownerID, buildID)); ok {
			return nil, de
		}
		return nil, fmt.Errorf("open app build log for %s: %w", ownerID, err)
	}
	return &created, nil
}

// GetCurrentBuildLog implements
// repository.AppBuildLogRepository.GetCurrentBuildLog -- a point lookup
// against app_build_log_current_idx (migration 019).
func (r *appBuildLogRepo) GetCurrentBuildLog(ctx context.Context, ownerID string) (*repository.AppBuildLog, error) {
	row := r.ex.QueryRow(ctx, `SELECT `+appBuildLogColumns+` FROM app_build_log WHERE owner_id = $1 AND valid_to IS NULL`, ownerID)
	l, err := scanAppBuildLog(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, repository.ErrNotFound
		}
		return nil, fmt.Errorf("get current app build log for %s: %w", ownerID, err)
	}
	return &l, nil
}
