package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/whale-net/everything/manmanv2/models"
)

// RestartScheduleRepository implements repository.RestartScheduleRepository
type RestartScheduleRepository struct {
	db *pgxpool.Pool
}

func NewRestartScheduleRepository(db *pgxpool.Pool) *RestartScheduleRepository {
	return &RestartScheduleRepository{db: db}
}

func (r *RestartScheduleRepository) Create(ctx context.Context, s *manman.RestartSchedule) (*manman.RestartSchedule, error) {
	err := r.db.QueryRow(ctx, `
		INSERT INTO restart_schedules (sgc_id, cadence_minutes, enabled, created_at, updated_at)
		VALUES ($1, $2, $3, NOW(), NOW())
		RETURNING restart_schedule_id, created_at, updated_at
	`, s.SGCID, s.CadenceMinutes, s.Enabled,
	).Scan(&s.RestartScheduleID, &s.CreatedAt, &s.UpdatedAt)
	return s, err
}

func (r *RestartScheduleRepository) Get(ctx context.Context, id int64) (*manman.RestartSchedule, error) {
	s := &manman.RestartSchedule{}
	err := r.db.QueryRow(ctx, `
		SELECT restart_schedule_id, sgc_id, cadence_minutes, enabled, last_restart_at, created_at, updated_at
		FROM restart_schedules WHERE restart_schedule_id = $1 AND deleted_at IS NULL
	`, id).Scan(
		&s.RestartScheduleID, &s.SGCID, &s.CadenceMinutes, &s.Enabled,
		&s.LastRestartAt, &s.CreatedAt, &s.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return s, nil
}

func (r *RestartScheduleRepository) ListBySGC(ctx context.Context, sgcID int64) ([]*manman.RestartSchedule, error) {
	rows, err := r.db.Query(ctx, `
		SELECT restart_schedule_id, sgc_id, cadence_minutes, enabled, last_restart_at, created_at, updated_at
		FROM restart_schedules WHERE sgc_id = $1 AND deleted_at IS NULL ORDER BY restart_schedule_id
	`, sgcID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var schedules []*manman.RestartSchedule
	for rows.Next() {
		s := &manman.RestartSchedule{}
		if err := rows.Scan(
			&s.RestartScheduleID, &s.SGCID, &s.CadenceMinutes, &s.Enabled,
			&s.LastRestartAt, &s.CreatedAt, &s.UpdatedAt,
		); err != nil {
			return nil, err
		}
		schedules = append(schedules, s)
	}
	return schedules, rows.Err()
}

// ListDue returns enabled restart schedules whose cadence has elapsed since last_restart_at (or never run).
func (r *RestartScheduleRepository) ListDue(ctx context.Context, now time.Time) ([]*manman.RestartSchedule, error) {
	rows, err := r.db.Query(ctx, `
		SELECT restart_schedule_id, sgc_id, cadence_minutes, enabled, last_restart_at, created_at, updated_at
		FROM restart_schedules
		WHERE enabled = true
		  AND deleted_at IS NULL
		  AND (
		      last_restart_at IS NULL
		      OR last_restart_at + (cadence_minutes * INTERVAL '1 minute') <= $1
		  )
	`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var schedules []*manman.RestartSchedule
	for rows.Next() {
		s := &manman.RestartSchedule{}
		if err := rows.Scan(
			&s.RestartScheduleID, &s.SGCID, &s.CadenceMinutes, &s.Enabled,
			&s.LastRestartAt, &s.CreatedAt, &s.UpdatedAt,
		); err != nil {
			return nil, err
		}
		schedules = append(schedules, s)
	}
	return schedules, rows.Err()
}

func (r *RestartScheduleRepository) Update(ctx context.Context, s *manman.RestartSchedule) error {
	_, err := r.db.Exec(ctx, `
		UPDATE restart_schedules
		SET cadence_minutes = $2, enabled = $3, updated_at = NOW()
		WHERE restart_schedule_id = $1 AND deleted_at IS NULL
	`, s.RestartScheduleID, s.CadenceMinutes, s.Enabled)
	return err
}

func (r *RestartScheduleRepository) SetLastRestartAt(ctx context.Context, id int64, t time.Time) error {
	_, err := r.db.Exec(ctx, `
		UPDATE restart_schedules SET last_restart_at = $2, updated_at = NOW()
		WHERE restart_schedule_id = $1
	`, id, t)
	return err
}

func (r *RestartScheduleRepository) Delete(ctx context.Context, id int64) error {
	_, err := r.db.Exec(ctx, `
		UPDATE restart_schedules SET deleted_at = NOW() WHERE restart_schedule_id = $1
	`, id)
	return err
}
