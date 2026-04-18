package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/whale-net/everything/manmanv2/api/repository"
	pb "github.com/whale-net/everything/manmanv2/protos"
)

// ============================================================================
// Scan job: runs every minute, enqueues one scheduledRestartJob per due schedule
// ============================================================================

type restartScanArgs struct{}

func (restartScanArgs) Kind() string { return "restart_scan" }

type restartScanWorker struct {
	river.WorkerDefaults[restartScanArgs]
	repo        *repository.Repository
	riverClient *river.Client[pgx.Tx]
	logger      *slog.Logger
}

func (w *restartScanWorker) Work(ctx context.Context, _ *river.Job[restartScanArgs]) error {
	due, err := w.repo.RestartSchedules.ListDue(ctx, time.Now())
	if err != nil {
		return fmt.Errorf("failed to list due restart schedules: %w", err)
	}
	for _, s := range due {
		_, err := w.riverClient.Insert(ctx, scheduledRestartArgs{RestartScheduleID: s.RestartScheduleID}, &river.InsertOpts{
			UniqueOpts: river.UniqueOpts{
				ByArgs:   true,
				ByPeriod: time.Duration(s.CadenceMinutes) * time.Minute,
			},
		})
		if err != nil {
			w.logger.Error("failed to enqueue restart job",
				"restart_schedule_id", s.RestartScheduleID,
				"sgc_id", s.SGCID,
				"error", err,
			)
		}
	}
	return nil
}

// ============================================================================
// Restart job: stops the active session for an SGC and starts a new one
// ============================================================================

type scheduledRestartArgs struct {
	RestartScheduleID int64 `json:"restart_schedule_id"`
}

func (scheduledRestartArgs) Kind() string { return "scheduled_restart" }

type scheduledRestartWorker struct {
	river.WorkerDefaults[scheduledRestartArgs]
	repo               *repository.Repository
	apiClient          pb.ManManAPIClient
	stopTimeoutSeconds int
	logger             *slog.Logger
}

func (w *scheduledRestartWorker) Work(ctx context.Context, job *river.Job[scheduledRestartArgs]) error {
	scheduleID := job.Args.RestartScheduleID

	// Load the restart schedule
	schedule, err := w.repo.RestartSchedules.Get(ctx, scheduleID)
	if err != nil {
		return fmt.Errorf("restart schedule %d not found: %w", scheduleID, err)
	}
	if !schedule.Enabled {
		w.logger.Info("restart schedule disabled, skipping", "restart_schedule_id", scheduleID)
		return nil
	}

	sgcID := schedule.SGCID

	// Find active session for this SGC
	activeStatuses := []string{"pending", "starting", "running"}
	sessions, err := w.repo.Sessions.ListWithFilters(ctx, &repository.SessionFilters{
		SGCID:        &sgcID,
		StatusFilter: activeStatuses,
		LiveOnly:     true,
	}, 1, 0)
	if err != nil {
		return fmt.Errorf("failed to list sessions for SGC %d: %w", sgcID, err)
	}

	if len(sessions) == 0 {
		// No active session — update cadence marker so we don't re-trigger immediately
		w.logger.Info("no active session found, skipping restart and updating last_restart_at",
			"restart_schedule_id", scheduleID,
			"sgc_id", sgcID,
		)
		return w.repo.RestartSchedules.SetLastRestartAt(ctx, scheduleID, time.Now())
	}

	sessionID := sessions[0].SessionID
	w.logger.Info("stopping session for scheduled restart",
		"restart_schedule_id", scheduleID,
		"sgc_id", sgcID,
		"session_id", sessionID,
	)

	// Stop the session via control-api
	if _, err := w.apiClient.StopSession(ctx, &pb.StopSessionRequest{SessionId: sessionID}); err != nil {
		return fmt.Errorf("failed to stop session %d: %w", sessionID, err)
	}

	// Poll until session reaches stopped/crashed/completed or timeout
	stopTimeout := time.Duration(w.stopTimeoutSeconds) * time.Second
	deadline := time.Now().Add(stopTimeout)
	const pollInterval = 3 * time.Second

	for time.Now().Before(deadline) {
		sess, err := w.repo.Sessions.Get(ctx, sessionID)
		if err != nil {
			return fmt.Errorf("failed to poll session %d status: %w", sessionID, err)
		}
		switch sess.Status {
		case "stopped", "crashed", "completed":
			goto sessionStopped
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
	return fmt.Errorf("session %d did not stop within %s", sessionID, stopTimeout)

sessionStopped:
	w.logger.Info("session stopped, starting new session",
		"restart_schedule_id", scheduleID,
		"sgc_id", sgcID,
		"stopped_session_id", sessionID,
	)

	// Start a new session for the same SGC
	if _, err := w.apiClient.StartSession(ctx, &pb.StartSessionRequest{
		ServerGameConfigId: sgcID,
	}); err != nil {
		return fmt.Errorf("failed to start new session for SGC %d: %w", sgcID, err)
	}

	// Update last_restart_at
	if err := w.repo.RestartSchedules.SetLastRestartAt(ctx, scheduleID, time.Now()); err != nil {
		w.logger.Error("failed to update last_restart_at",
			"restart_schedule_id", scheduleID,
			"error", err,
		)
		// Non-fatal — the restart succeeded; cadence drift is acceptable.
	}

	w.logger.Info("scheduled restart complete",
		"restart_schedule_id", scheduleID,
		"sgc_id", sgcID,
	)
	return nil
}
