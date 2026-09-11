package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/whale-net/everything/manmanv2/models"
	"github.com/whale-net/everything/manmanv2/api/repository"
)

type SessionRepository struct {
	db *pgxpool.Pool
}

func NewSessionRepository(db *pgxpool.Pool) *SessionRepository {
	return &SessionRepository{db: db}
}

func (r *SessionRepository) Create(ctx context.Context, session *manman.Session) (*manman.Session, error) {
	query := `
		INSERT INTO sessions (sgc_id, status)
		VALUES ($1, $2)
		RETURNING session_id
	`

	err := r.db.QueryRow(ctx, query,
		session.SGCID,
		session.Status,
	).Scan(&session.SessionID)
	if err != nil {
		return nil, err
	}

	return session, nil
}

func (r *SessionRepository) Get(ctx context.Context, sessionID int64) (*manman.Session, error) {
	session := &manman.Session{}

	query := `
		SELECT session_id, sgc_id, started_at, ended_at, exit_code, status, created_at, updated_at
		FROM sessions
		WHERE session_id = $1
	`

	err := r.db.QueryRow(ctx, query, sessionID).Scan(
		&session.SessionID,
		&session.SGCID,
		&session.StartedAt,
		&session.EndedAt,
		&session.ExitCode,
		&session.Status,
		&session.CreatedAt,
		&session.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	return session, nil
}

func (r *SessionRepository) List(ctx context.Context, sgcID *int64, limit, offset int) ([]*manman.Session, error) {
	if limit <= 0 {
		limit = 50
	}

	var query string
	var args []interface{}

	if sgcID != nil {
		query = `
			SELECT session_id, sgc_id, started_at, ended_at, exit_code, status, created_at, updated_at
			FROM sessions
			WHERE sgc_id = $1
			ORDER BY session_id DESC
			LIMIT $2 OFFSET $3
		`
		args = []interface{}{*sgcID, limit, offset}
	} else {
		query = `
			SELECT session_id, sgc_id, started_at, ended_at, exit_code, status, created_at, updated_at
			FROM sessions
			ORDER BY session_id DESC
			LIMIT $1 OFFSET $2
		`
		args = []interface{}{limit, offset}
	}

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []*manman.Session
	for rows.Next() {
		session := &manman.Session{}
		err := rows.Scan(
			&session.SessionID,
			&session.SGCID,
			&session.StartedAt,
			&session.EndedAt,
			&session.ExitCode,
			&session.Status,
			&session.CreatedAt,
			&session.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}

	return sessions, rows.Err()
}

func (r *SessionRepository) StopOtherSessionsForSGC(ctx context.Context, sessionID int64, sgcID int64) error {
	query := `
		UPDATE sessions
		SET status = 'stopped', ended_at = $3
		WHERE sgc_id = $1 AND session_id != $2 AND status IN ('pending', 'starting', 'running', 'stopping', 'crashed', 'lost')
	`
	_, err := r.db.Exec(ctx, query, sgcID, sessionID, time.Now())
	return err
}
func (r *SessionRepository) Update(ctx context.Context, session *manman.Session) error {
	query := `
		UPDATE sessions
		SET started_at = $2, ended_at = $3, exit_code = $4, status = $5
		WHERE session_id = $1
	`

	_, err := r.db.Exec(ctx, query,
		session.SessionID,
		session.StartedAt,
		session.EndedAt,
		session.ExitCode,
		session.Status,
	)
	return err
}

func (r *SessionRepository) ListWithFilters(ctx context.Context, filters *repository.SessionFilters, limit, offset int) ([]*manman.Session, error) {
	if limit <= 0 {
		limit = 50
	}

	baseQuery := `
		SELECT s.session_id, s.sgc_id, s.started_at, s.ended_at, s.exit_code, s.status, s.created_at, s.updated_at
		FROM sessions s
	`

	whereClauses := []string{}
	args := []interface{}{}
	argIdx := 1

	// Filter by SGCID
	if filters.SGCID != nil {
		whereClauses = append(whereClauses, fmt.Sprintf("s.sgc_id = $%d", argIdx))
		args = append(args, *filters.SGCID)
		argIdx++
	}

	// Filter by ServerID (requires join)
	if filters.ServerID != nil {
		baseQuery = `
			SELECT s.session_id, s.sgc_id, s.started_at, s.ended_at, s.exit_code, s.status, s.created_at, s.updated_at
			FROM sessions s
			JOIN server_game_configs sgc ON s.sgc_id = sgc.sgc_id
		`
		whereClauses = append(whereClauses, fmt.Sprintf("sgc.server_id = $%d", argIdx))
		args = append(args, *filters.ServerID)
		argIdx++
	}

	// Filter by status
	if len(filters.StatusFilter) > 0 {
		placeholders := make([]string, len(filters.StatusFilter))
		for i, status := range filters.StatusFilter {
			placeholders[i] = fmt.Sprintf("$%d", argIdx)
			args = append(args, status)
			argIdx++
		}
		whereClauses = append(whereClauses, fmt.Sprintf("s.status IN (%s)", strings.Join(placeholders, ",")))
	}

	// Filter by started_after
	if filters.StartedAfter != nil {
		whereClauses = append(whereClauses, fmt.Sprintf("s.started_at > $%d", argIdx))
		args = append(args, *filters.StartedAfter)
		argIdx++
	}

	// Filter by started_before
	if filters.StartedBefore != nil {
		whereClauses = append(whereClauses, fmt.Sprintf("s.started_at < $%d", argIdx))
		args = append(args, *filters.StartedBefore)
		argIdx++
	}

	// Filter for live_only
	if filters.LiveOnly {
		whereClauses = append(whereClauses, "s.status IN ('pending', 'starting', 'running', 'stopping')")
	}

	// Build WHERE clause
	if len(whereClauses) > 0 {
		baseQuery += " WHERE " + strings.Join(whereClauses, " AND ")
	}

	// Add ORDER BY and pagination
	baseQuery += fmt.Sprintf(" ORDER BY s.session_id DESC LIMIT $%d OFFSET $%d", argIdx, argIdx+1)
	args = append(args, limit, offset)

	rows, err := r.db.Query(ctx, baseQuery, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []*manman.Session
	for rows.Next() {
		session := &manman.Session{}
		err := rows.Scan(
			&session.SessionID,
			&session.SGCID,
			&session.StartedAt,
			&session.EndedAt,
			&session.ExitCode,
			&session.Status,
			&session.CreatedAt,
			&session.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}

	return sessions, rows.Err()
}

func (r *SessionRepository) UpdateStatus(ctx context.Context, sessionID int64, status string) error {
	query := `
		UPDATE sessions
		SET status = $2
		WHERE session_id = $1
		RETURNING session_id
	`

	var returnedID int64
	err := r.db.QueryRow(ctx, query, sessionID, status).Scan(&returnedID)
	return err
}

func (r *SessionRepository) UpdateSessionStart(ctx context.Context, sessionID int64, startedAt time.Time) error {
	query := `
		UPDATE sessions
		SET status = $2, started_at = $3
		WHERE session_id = $1
		RETURNING session_id
	`

	var returnedID int64
	err := r.db.QueryRow(ctx, query, sessionID, manman.SessionStatusRunning, startedAt).Scan(&returnedID)
	return err
}

func (r *SessionRepository) UpdateSessionEnd(ctx context.Context, sessionID int64, status string, endedAt time.Time, exitCode *int) error {
	query := `
		UPDATE sessions
		SET status = $2, ended_at = $3, exit_code = $4
		WHERE session_id = $1
		RETURNING session_id
	`

	var returnedID int64
	err := r.db.QueryRow(ctx, query, sessionID, status, endedAt, exitCode).Scan(&returnedID)
	return err
}

func (r *SessionRepository) UpdateSessionEndIfStatus(ctx context.Context, sessionID int64, expectedStatus, newStatus string, endedAt time.Time, exitCode *int) (bool, error) {
	query := `
		UPDATE sessions
		SET status = $3, ended_at = $4, exit_code = $5
		WHERE session_id = $1 AND status = $2
		RETURNING session_id
	`

	var returnedID int64
	err := r.db.QueryRow(ctx, query, sessionID, expectedStatus, newStatus, endedAt, exitCode).Scan(&returnedID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil // row changed underneath us -- not an error, the caller skips this session
	}
	return err == nil, err
}

// CountRunningDeploymentsByGame is the fleet-wide status summary aggregate
// (#2371, manmanv2 M6, FR5/NFR5) -- see the SessionRepository interface doc
// for the exact running/total definitions. One aggregate query: games LEFT
// JOIN game_configs LEFT JOIN server_game_configs (so a game with zero
// deployments still appears, as 0/0) with a LATERAL subselect resolving
// each deployment's most-recent session (by session_id, not started_at, so
// a session that was created but never started still counts as "most
// recent" over an older, already-ended one).
func (r *SessionRepository) CountRunningDeploymentsByGame(ctx context.Context) ([]*manman.FleetGameStatus, error) {
	query := `
		SELECT
			g.game_id,
			g.name,
			COUNT(sgc.sgc_id) AS total_count,
			COUNT(*) FILTER (WHERE latest.status = $1) AS running_count
		FROM games g
		LEFT JOIN game_configs gc ON gc.game_id = g.game_id
		LEFT JOIN server_game_configs sgc ON sgc.game_config_id = gc.config_id
		LEFT JOIN LATERAL (
			SELECT s.status
			FROM sessions s
			WHERE s.sgc_id = sgc.sgc_id
			ORDER BY s.session_id DESC
			LIMIT 1
		) latest ON true
		GROUP BY g.game_id, g.name
		ORDER BY g.name
	`

	rows, err := r.db.Query(ctx, query, manman.SessionStatusRunning)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []*manman.FleetGameStatus
	for rows.Next() {
		var totalCount, runningCount int64
		fgs := &manman.FleetGameStatus{}
		if err := rows.Scan(&fgs.GameID, &fgs.GameName, &totalCount, &runningCount); err != nil {
			return nil, err
		}
		fgs.TotalCount = int32(totalCount)
		fgs.RunningCount = int32(runningCount)
		results = append(results, fgs)
	}

	return results, rows.Err()
}

func (r *SessionRepository) GetStaleSessions(ctx context.Context, threshold time.Duration) ([]*manman.Session, error) {
	query := `
		SELECT session_id, sgc_id, started_at, ended_at, exit_code, status, created_at, updated_at
		FROM sessions
		WHERE status IN ('pending', 'starting', 'stopping')
		AND updated_at < $1
	`

	cutoff := time.Now().Add(-threshold)
	rows, err := r.db.Query(ctx, query, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []*manman.Session
	for rows.Next() {
		session := &manman.Session{}
		err := rows.Scan(
			&session.SessionID,
			&session.SGCID,
			&session.StartedAt,
			&session.EndedAt,
			&session.ExitCode,
			&session.Status,
			&session.CreatedAt,
			&session.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}

	return sessions, rows.Err()
}
