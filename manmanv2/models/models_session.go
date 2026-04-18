package manman

import "time"

// Session represents an execution of a ServerGameConfig
type Session struct {
	SessionID            int64      `db:"session_id"`
	SGCID                int64      `db:"sgc_id"`
	StartedAt            *time.Time `db:"started_at"`
	EndedAt              *time.Time `db:"ended_at"`
	ExitCode             *int       `db:"exit_code"`
	Status               string     `db:"status"`
	RestoredFromBackupID *int64     `db:"restored_from_backup_id"`
	CreatedAt            time.Time  `db:"created_at"`
	UpdatedAt            time.Time  `db:"updated_at"`
}

// LogReference represents a reference to a log file for a session
type LogReference struct {
	LogID           int64      `db:"log_id"`
	SessionID       int64      `db:"session_id"`
	SGCID           *int64     `db:"sgc_id"`
	FilePath        string     `db:"file_path"`
	StartTime       time.Time  `db:"start_time"`
	EndTime         time.Time  `db:"end_time"`
	LineCount       int32      `db:"line_count"`
	Source          string     `db:"source"`
	MinuteTimestamp *time.Time `db:"minute_timestamp"`
	State           string     `db:"state"`
	AppendedAt      *time.Time `db:"appended_at"`
	CreatedAt       time.Time  `db:"created_at"`
}

// IsActive returns true if the session is in an active state (not completed or stopped)
// Note: crashed and lost are still considered active for management purposes
func (s Session) IsActive() bool {
	switch s.Status {
	case SessionStatusPending, SessionStatusStarting, SessionStatusRunning,
		SessionStatusStopping, SessionStatusCrashed, SessionStatusLost:
		return true
	default:
		return false
	}
}

// IsAvailable returns true if the session is running and ready for connections
func (s Session) IsAvailable() bool {
	return s.Status == SessionStatusRunning
}
