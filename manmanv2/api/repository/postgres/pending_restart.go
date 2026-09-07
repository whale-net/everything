package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/whale-net/everything/manmanv2/api/repository"
	"github.com/whale-net/everything/manmanv2/models"
)

// Postgres SQLSTATE code for a unique-constraint violation. See
// https://www.postgresql.org/docs/current/errcodes-appendix.html.
const sqlStateUniqueViolation = "23505"

// pendingRestartVisibilityWindow bounds how long a terminal ('failed' or
// 'expired') pending_restarts record stays visible to GetLatestBySGCIDs
// (FR12, #1735): without a bound, a week-old "Restart failed" badge would
// sit on a deployment that has since been happily restarted by hand. Chosen
// as 5 minutes -- long enough for an operator glancing at the page shortly
// after a failure to still see it, short enough that it isn't mistaken for
// current state. Only applies to resolved records; an unresolved ('pending'
// or 'started') record is always visible regardless of age.
const pendingRestartVisibilityWindow = 5 * time.Minute

// pendingRestartsOnePendingPerSGCIndex is the unique partial index name from
// migration 036_pending_restarts. Create checks the violating index by name
// (not just the SQLSTATE) so it never mistranslates an unrelated future
// unique constraint on this table into ErrPendingRestartExists.
const pendingRestartsOnePendingPerSGCIndex = "pending_restarts_one_pending_per_sgc"

// PendingRestartRepository is the postgres-backed implementation of
// repository.PendingRestartRepository. See repository.go for the interface
// contract; the atomicity requirements on ClaimForSession and ExpireStalled
// (single UPDATE ... RETURNING, never SELECT-then-UPDATE) are load-bearing
// for NFR10 and are implemented here as single statements.
type PendingRestartRepository struct {
	db *pgxpool.Pool
}

func NewPendingRestartRepository(db *pgxpool.Pool) *PendingRestartRepository {
	return &PendingRestartRepository{db: db}
}

func scanPendingRestart(row pgx.Row) (*manman.PendingRestart, error) {
	pr := &manman.PendingRestart{}
	err := row.Scan(
		&pr.PendingRestartID,
		&pr.ServerGameConfigID,
		&pr.GatingSessionID,
		&pr.Status,
		&pr.StallDeadline,
		&pr.StartedSessionID,
		&pr.FailureReason,
		&pr.CreatedAt,
		&pr.ResolvedAt,
	)
	if err != nil {
		return nil, err
	}
	return pr, nil
}

const pendingRestartColumns = `
	pending_restart_id, server_game_config_id, gating_session_id, status,
	stall_deadline, started_session_id, failure_reason, created_at, resolved_at
`

func (r *PendingRestartRepository) Create(ctx context.Context, sgcID, gatingSessionID int64, stallDeadline time.Time) (*manman.PendingRestart, error) {
	query := `
		INSERT INTO pending_restarts (server_game_config_id, gating_session_id, status, stall_deadline)
		VALUES ($1, $2, 'pending', $3)
		RETURNING ` + pendingRestartColumns

	pr, err := scanPendingRestart(r.db.QueryRow(ctx, query, sgcID, gatingSessionID, stallDeadline))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == sqlStateUniqueViolation && pgErr.ConstraintName == pendingRestartsOnePendingPerSGCIndex {
			return nil, repository.ErrPendingRestartExists
		}
		return nil, err
	}
	return pr, nil
}

func (r *PendingRestartRepository) ClaimForSession(ctx context.Context, gatingSessionID int64) (*manman.PendingRestart, error) {
	query := `
		UPDATE pending_restarts
		SET status = 'started', resolved_at = NOW()
		WHERE gating_session_id = $1 AND status = 'pending'
		RETURNING ` + pendingRestartColumns

	pr, err := scanPendingRestart(r.db.QueryRow(ctx, query, gatingSessionID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return pr, nil
}

func (r *PendingRestartRepository) MarkStarted(ctx context.Context, pendingRestartID, startedSessionID int64) error {
	query := `
		UPDATE pending_restarts
		SET started_session_id = $2
		WHERE pending_restart_id = $1 AND status = 'started'
	`
	_, err := r.db.Exec(ctx, query, pendingRestartID, startedSessionID)
	return err
}

func (r *PendingRestartRepository) MarkFailed(ctx context.Context, pendingRestartID int64, reason string) error {
	// Accepts both 'pending' and 'started': a record can fail either before
	// it's ever claimed (RestartDeployment's own Stop dispatch fails right
	// after Create, #1730) or after it's claimed (the deferred Start itself
	// fails, #1731) -- both are "this restart intent will never resolve
	// successfully" and share the same terminal transition. resolved_at is
	// only set here if it wasn't already (ClaimForSession sets it at the
	// pending->started transition), so it keeps meaning "when this record
	// stopped being 'pending'" in both cases.
	query := `
		UPDATE pending_restarts
		SET status = 'failed', failure_reason = $2, resolved_at = COALESCE(resolved_at, NOW())
		WHERE pending_restart_id = $1 AND status IN ('pending', 'started')
	`
	_, err := r.db.Exec(ctx, query, pendingRestartID, reason)
	return err
}

func (r *PendingRestartRepository) ExpireStalled(ctx context.Context, now time.Time) ([]*manman.PendingRestart, error) {
	query := `
		UPDATE pending_restarts
		SET status = 'expired', resolved_at = NOW(), failure_reason = 'stall deadline exceeded'
		WHERE status = 'pending' AND stall_deadline <= $1
		RETURNING ` + pendingRestartColumns

	rows, err := r.db.Query(ctx, query, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var expired []*manman.PendingRestart
	for rows.Next() {
		pr, err := scanPendingRestart(rows)
		if err != nil {
			return nil, err
		}
		expired = append(expired, pr)
	}
	return expired, rows.Err()
}

// GetLatestBySGCIDs returns the latest pending_restarts record per SGC,
// excluding records resolved more than pendingRestartVisibilityWindow ago
// (FR12, #1735 -- see that constant's doc comment). Unresolved records
// (resolved_at IS NULL) are never excluded by the window.
func (r *PendingRestartRepository) GetLatestBySGCIDs(ctx context.Context, sgcIDs []int64) (map[int64]*manman.PendingRestart, error) {
	result := make(map[int64]*manman.PendingRestart)
	if len(sgcIDs) == 0 {
		return result, nil
	}

	query := `
		SELECT DISTINCT ON (server_game_config_id) ` + pendingRestartColumns + `
		FROM pending_restarts
		WHERE server_game_config_id = ANY($1)
			AND (resolved_at IS NULL OR resolved_at > $2)
		ORDER BY server_game_config_id, pending_restart_id DESC
	`

	rows, err := r.db.Query(ctx, query, sgcIDs, time.Now().Add(-pendingRestartVisibilityWindow))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		pr, err := scanPendingRestart(rows)
		if err != nil {
			return nil, err
		}
		result[pr.ServerGameConfigID] = pr
	}
	return result, rows.Err()
}
