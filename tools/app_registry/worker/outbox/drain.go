// Package outbox drains tools/app_registry's writeback_outbox table,
// starting one WritebackWorkflow per claimed row -- see ARCHITECTURE.md
// "Writeback: outbox -> Temporal" and PLAN.md's AR-4b scope. This package
// owns no business logic about *what* gets written; it only turns an
// outbox row into a Temporal workflow execution, reliably and exactly
// once.
package outbox

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"

	"github.com/whale-net/everything/tools/app_registry/server/repository"
	"github.com/whale-net/everything/tools/app_registry/worker/writeback"
)

// Drainer polls repository.WritebackRepository and starts a
// WritebackWorkflow for each row it claims.
type Drainer struct {
	// Store is the outbox repository -- backed by the same Postgres
	// database the App Registry API writes to (see
	// server/repository/postgres), connected directly rather than through
	// the gRPC API: draining the outbox is not a promotion-transaction
	// concern, and a direct connection means the worker keeps functioning
	// even if the API process itself is down.
	Store repository.WritebackRepository
	// Temporal starts workflows. See libs/go/temporal.NewClient.
	Temporal client.Client
	// TaskQueue is the queue WritebackWorkflow executions are dispatched
	// on -- must match what the worker.Worker in main.go is listening on.
	// Defaults to writeback.TaskQueue if empty.
	TaskQueue string
	// WorkerID identifies this drain loop instance in claimed_by, for
	// operator visibility into which process holds a claim.
	WorkerID string
	// BatchSize caps how many rows one drain pass claims.
	BatchSize int
	// StaleAfter is how long a 'claimed' row is left alone before the next
	// pass reclaims it -- see repository.WritebackRepository.ClaimBatch's
	// doc comment. Must be comfortably longer than a WritebackWorkflow's
	// expected StartToCloseTimeout (see writeback.WritebackWorkflow) or a
	// still-healthy claim gets needlessly reclaimed.
	StaleAfter time.Duration
	// PollInterval is the delay between drain passes.
	PollInterval time.Duration
	// Logger receives per-row and per-error events. If nil, slog.Default()
	// is used.
	Logger *slog.Logger
}

func (d *Drainer) taskQueue() string {
	if d.TaskQueue != "" {
		return d.TaskQueue
	}
	return writeback.TaskQueue
}

func (d *Drainer) logger() *slog.Logger {
	if d.Logger != nil {
		return d.Logger
	}
	return slog.Default()
}

// Run polls forever (one drainOnce per PollInterval) until ctx is
// cancelled. A drainOnce error is logged, not fatal -- the next tick tries
// again, matching ARCHITECTURE.md's "the registry can be down without
// blocking a deploy" posture applied to the worker's own dependencies.
func (d *Drainer) Run(ctx context.Context) error {
	ticker := time.NewTicker(d.PollInterval)
	defer ticker.Stop()

	// Drain once immediately on startup rather than waiting a full
	// PollInterval -- important for the worker being killed mid-run and
	// restarted: a reclaimable row should not sit idle for a whole tick.
	if err := d.drainOnce(ctx); err != nil {
		d.logger().Error("outbox drain pass failed", "error", err)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := d.drainOnce(ctx); err != nil {
				d.logger().Error("outbox drain pass failed", "error", err)
			}
		}
	}
}

// drainOnce claims up to BatchSize rows and starts a workflow for each.
// Each row's own failure is handled (marked failed, logged) without
// aborting the rest of the batch.
func (d *Drainer) drainOnce(ctx context.Context) error {
	rows, err := d.Store.ClaimBatch(ctx, d.WorkerID, d.BatchSize, d.StaleAfter)
	if err != nil {
		return err
	}
	for _, row := range rows {
		d.startWorkflow(ctx, row)
	}
	return nil
}

// startWorkflow starts WritebackWorkflow with workflow id = row.PromotionID
// (see ARCHITECTURE.md) and marks the outbox row done on success. Two
// outcomes both count as success:
//
//  1. ExecuteWorkflow returns a run: this claim genuinely started (or
//     re-started, after the prior run completed) the workflow.
//  2. ExecuteWorkflow returns serviceerror.WorkflowExecutionAlreadyStarted:
//     a workflow with this id is already running -- almost always because
//     this same row was claimed, started, and the worker died before
//     marking it done, and a later pass reclaimed it. Temporal's own
//     workflow-id collision handling is what makes this branch safe to
//     treat as success without starting a duplicate concurrent execution --
//     see WritebackWorkflow's doc comment.
//
// Any other error marks the row failed (returned to 'pending' for the next
// pass to retry -- see MarkFailed's doc comment), not stuck 'claimed'.
func (d *Drainer) startWorkflow(ctx context.Context, row repository.WritebackOutbox) {
	log := d.logger().With("outbox_id", row.OutboxID, "promotion_id", row.PromotionID, "environment_key", row.EnvironmentKey)

	run, err := d.Temporal.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:        row.PromotionID,
		TaskQueue: d.taskQueue(),
	}, writeback.WritebackWorkflow, writeback.WritebackInput{
		PromotionID:    row.PromotionID,
		EnvironmentKey: row.EnvironmentKey,
		Domain:         row.Domain,
		StateHash:      row.StateHash,
	})

	var alreadyStarted *serviceerror.WorkflowExecutionAlreadyStarted
	switch {
	case err == nil:
		log.Info("started writeback workflow", "run_id", run.GetRunID())
		if merr := d.Store.MarkDone(ctx, row.OutboxID, row.PromotionID, run.GetRunID()); merr != nil {
			log.Error("mark outbox row done", "error", merr)
		}
	case errors.As(err, &alreadyStarted):
		// Redelivery of an outbox row whose workflow is already running --
		// exactly the case Temporal's dedup exists to make harmless. Not
		// an error condition for this row.
		log.Info("writeback workflow already running for this promotion, treating claim as done")
		if merr := d.Store.MarkDone(ctx, row.OutboxID, row.PromotionID, ""); merr != nil {
			log.Error("mark outbox row done (already-started)", "error", merr)
		}
	default:
		log.Error("start writeback workflow", "error", err)
		if merr := d.Store.MarkFailed(ctx, row.OutboxID, err.Error()); merr != nil {
			log.Error("mark outbox row failed", "error", merr)
		}
	}
}
