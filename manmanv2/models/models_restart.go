package manman

import "time"

// RestartSchedule defines a scheduled restart for a ServerGameConfig
type RestartSchedule struct {
	RestartScheduleID int64      `db:"restart_schedule_id"`
	SGCID             int64      `db:"sgc_id"`
	CadenceMinutes    int        `db:"cadence_minutes"`
	Enabled           bool       `db:"enabled"`
	LastRestartAt     *time.Time `db:"last_restart_at"`
	CreatedAt         time.Time  `db:"created_at"`
	UpdatedAt         time.Time  `db:"updated_at"`
}
