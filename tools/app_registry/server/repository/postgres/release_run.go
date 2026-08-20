package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// releaseRunRepo implements repository.ReleaseRunRepository against
// `release_run`/`release_run_target` (migration 016, NFR4, issue #887). See
// that migration's doc comment and repository.ReleaseRunRepository's doc
// comment for the shape: NOT SCD2 -- release_run is written once,
// release_run_target is mutated in place per transition (matching
// artifactRepo's mutation style, not promotionRepo's SCD2 open/close
// style).
type releaseRunRepo struct{ ex dbtx }

const releaseRunColumns = `release_run_id, triggered_by, requested_scope, digest_input, resolved_plan, temporal_workflow_id, temporal_run_id, created_at`

// releaseRunColumnsPrefixed is releaseRunColumns qualified with the `rr`
// alias ListReleaseRunsByTarget's join uses -- release_run_id exists on both
// joined tables, so an unqualified SELECT list would be ambiguous there.
const releaseRunColumnsPrefixed = `rr.release_run_id, rr.triggered_by, rr.requested_scope, rr.digest_input, rr.resolved_plan, rr.temporal_workflow_id, rr.temporal_run_id, rr.created_at`

const releaseRunTargetColumns = `release_run_target_id, release_run_id, owner_full_name, kind, state, state_changed_at, build_id, error_detail`

func scanReleaseRun(row pgx.Row) (repository.ReleaseRun, error) {
	var rr repository.ReleaseRun
	// digest_input is nullable (NULL means "build fresh", see migration
	// 016's doc comment) -- scan into a *[]byte so a NULL row leaves
	// rr.DigestInput nil rather than an empty non-nil slice.
	var digestInput *[]byte
	if err := row.Scan(
		&rr.ReleaseRunID, &rr.TriggeredBy, &rr.RequestedScope, &digestInput, &rr.ResolvedPlan,
		&rr.TemporalWorkflowID, &rr.TemporalRunID, &rr.CreatedAt,
	); err != nil {
		return repository.ReleaseRun{}, err
	}
	if digestInput != nil {
		rr.DigestInput = *digestInput
	}
	return rr, nil
}

func scanReleaseRunTarget(row pgx.Row) (repository.ReleaseRunTarget, error) {
	var t repository.ReleaseRunTarget
	var kind, state string
	var buildID *string
	if err := row.Scan(
		&t.ReleaseRunTargetID, &t.ReleaseRunID, &t.OwnerFullName, &kind, &state, &t.StateChangedAt, &buildID, &t.ErrorDetail,
	); err != nil {
		return repository.ReleaseRunTarget{}, err
	}
	t.Kind = repository.ArtifactKind(kind)
	t.State = repository.ReleaseRunTargetState(state)
	if buildID != nil {
		t.BuildID = *buildID
	}
	return t, nil
}

// legalReleaseRunTargetTransitions is the transition table
// ReleaseRunTargetState's doc comment (repository/models.go) describes:
// queued -> building -> publishing -> recording -> succeeded, with a
// transition to failed legal from any non-terminal state. succeeded/failed
// have no entry, i.e. no legal transition out of either -- both terminal.
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

// CreateReleaseRun implements repository.ReleaseRunRepository.CreateReleaseRun
// -- one INSERT into release_run plus one INSERT per target into
// release_run_target, all starting 'queued'. Callers MUST invoke this inside
// Registry.WithTx (see that method's doc comment) so a failure partway
// through target inserts rolls the whole batch back rather than leaving a
// release_run row with a partial target set.
func (r *releaseRunRepo) CreateReleaseRun(ctx context.Context, run repository.ReleaseRun, targets []repository.ReleaseRunTarget) (*repository.ReleaseRun, []repository.ReleaseRunTarget, error) {
	if len(targets) == 0 {
		return nil, nil, fmt.Errorf("%w: release run must cover at least one target", repository.ErrInvalidArgument)
	}

	var digestInput any
	if run.DigestInput != nil {
		digestInput = string(run.DigestInput)
	}

	row := r.ex.QueryRow(ctx, `
		INSERT INTO release_run (triggered_by, requested_scope, digest_input, resolved_plan, temporal_workflow_id)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING `+releaseRunColumns,
		run.TriggeredBy, run.RequestedScope, digestInput, string(run.ResolvedPlan), run.TemporalWorkflowID)
	created, err := scanReleaseRun(row)
	if err != nil {
		if de, ok := translatePgError(err, fmt.Sprintf("release run for workflow %s already exists", run.TemporalWorkflowID)); ok {
			return nil, nil, de
		}
		return nil, nil, fmt.Errorf("create release run for workflow %s: %w", run.TemporalWorkflowID, err)
	}

	outTargets := make([]repository.ReleaseRunTarget, 0, len(targets))
	for _, t := range targets {
		row := r.ex.QueryRow(ctx, `
			INSERT INTO release_run_target (release_run_id, owner_full_name, kind)
			VALUES ($1, $2, $3)
			RETURNING `+releaseRunTargetColumns,
			created.ReleaseRunID, t.OwnerFullName, string(t.Kind))
		out, err := scanReleaseRunTarget(row)
		if err != nil {
			if de, ok := translatePgError(err, fmt.Sprintf("release run target %s for run %s", t.OwnerFullName, created.ReleaseRunID)); ok {
				return nil, nil, de
			}
			return nil, nil, fmt.Errorf("create release run target %s for run %s: %w", t.OwnerFullName, created.ReleaseRunID, err)
		}
		outTargets = append(outTargets, out)
	}

	return &created, outTargets, nil
}

// UpdateTargetState implements
// repository.ReleaseRunRepository.UpdateTargetState -- transitions
// releaseRunTargetID to newState in place, validating the transition against
// legalReleaseRunTargetTransitions first. buildID/errorDetail, when
// non-empty, overwrite the existing column; when empty, the existing value
// is preserved (so a building -> publishing transition that doesn't pass
// buildID again doesn't null out the build_id 'building' already recorded).
func (r *releaseRunRepo) UpdateTargetState(ctx context.Context, releaseRunTargetID string, newState repository.ReleaseRunTargetState, buildID, errorDetail string) error {
	row := r.ex.QueryRow(ctx, `SELECT `+releaseRunTargetColumns+` FROM release_run_target WHERE release_run_target_id = $1`, releaseRunTargetID)
	existing, err := scanReleaseRunTarget(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: release run target %s", repository.ErrNotFound, releaseRunTargetID)
		}
		return err
	}

	if !legalReleaseRunTargetTransitions[existing.State][newState] {
		return fmt.Errorf("%w: release run target %s is %q, cannot transition to %q",
			repository.ErrFailedPrecondition, releaseRunTargetID, existing.State, newState)
	}

	effectiveBuildID := existing.BuildID
	if buildID != "" {
		effectiveBuildID = buildID
	}
	var buildIDArg any
	if effectiveBuildID != "" {
		buildIDArg = effectiveBuildID
	}

	tag, err := r.ex.Exec(ctx, `
		UPDATE release_run_target
		SET state = $1, state_changed_at = NOW(), build_id = $2, error_detail = $3
		WHERE release_run_target_id = $4`,
		string(newState), buildIDArg, errorDetail, releaseRunTargetID)
	if err != nil {
		if de, ok := translatePgError(err, fmt.Sprintf("release run target %s build reference %s", releaseRunTargetID, effectiveBuildID)); ok {
			return de
		}
		return fmt.Errorf("update release run target %s state: %w", releaseRunTargetID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: release run target %s", repository.ErrNotFound, releaseRunTargetID)
	}
	return nil
}

// GetReleaseRun implements repository.ReleaseRunRepository.GetReleaseRun --
// the release_run row plus every one of its release_run_target rows.
func (r *releaseRunRepo) GetReleaseRun(ctx context.Context, releaseRunID string) (*repository.ReleaseRun, []repository.ReleaseRunTarget, error) {
	row := r.ex.QueryRow(ctx, `SELECT `+releaseRunColumns+` FROM release_run WHERE release_run_id = $1`, releaseRunID)
	run, err := scanReleaseRun(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, repository.ErrNotFound
		}
		return nil, nil, err
	}

	rows, err := r.ex.Query(ctx, `SELECT `+releaseRunTargetColumns+` FROM release_run_target WHERE release_run_id = $1 ORDER BY release_run_target_id`, releaseRunID)
	if err != nil {
		return nil, nil, fmt.Errorf("list targets for release run %s: %w", releaseRunID, err)
	}
	defer rows.Close()

	var targets []repository.ReleaseRunTarget
	for rows.Next() {
		t, err := scanReleaseRunTarget(rows)
		if err != nil {
			return nil, nil, err
		}
		targets = append(targets, t)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return &run, targets, nil
}

// ListReleaseRunsByTarget implements
// repository.ReleaseRunRepository.ListReleaseRunsByTarget -- every
// release_run that covers ownerFullName in at least one of its
// release_run_target rows, most-recent-first. Backed by
// release_run_target_owner_state_idx (migration 016) for the owner_full_name
// filter; DISTINCT collapses the join back to one row per release_run since
// CreateReleaseRun never inserts more than one release_run_target row per
// owner within the same release_run.
func (r *releaseRunRepo) ListReleaseRunsByTarget(ctx context.Context, ownerFullName string) ([]repository.ReleaseRun, error) {
	rows, err := r.ex.Query(ctx, `
		SELECT DISTINCT `+releaseRunColumnsPrefixed+`
		FROM release_run rr
		JOIN release_run_target rrt ON rrt.release_run_id = rr.release_run_id
		WHERE rrt.owner_full_name = $1
		ORDER BY rr.created_at DESC, rr.release_run_id DESC`,
		ownerFullName)
	if err != nil {
		return nil, fmt.Errorf("list release runs for target %s: %w", ownerFullName, err)
	}
	defer rows.Close()

	var out []repository.ReleaseRun
	for rows.Next() {
		run, err := scanReleaseRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}
