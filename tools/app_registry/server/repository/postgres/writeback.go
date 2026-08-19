package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

type writebackRepo struct{ ex dbtx }

const writebackColumns = `outbox_id, promotion_id, environment_id, environment_key, domain, event_id, state_hash,
	status, claimed_by, claimed_at, workflow_id, completed_at, last_error, attempts, created_at`

func scanWriteback(row pgx.Row) (repository.WritebackOutbox, error) {
	var o repository.WritebackOutbox
	var status string
	// claimed_by is NULL until a worker first claims the row -- unlike
	// EnvironmentKey/WorkflowID/LastError (all NOT NULL DEFAULT '' columns,
	// see 004_writeback_outbox.up.sql), so it needs a nullable scan target.
	var claimedBy *string
	if err := row.Scan(
		&o.OutboxID, &o.PromotionID, &o.EnvironmentID, &o.EnvironmentKey, &o.Domain, &o.EventID, &o.StateHash,
		&status, &claimedBy, &o.ClaimedAt, &o.WorkflowID, &o.CompletedAt, &o.LastError, &o.Attempts, &o.CreatedAt,
	); err != nil {
		return repository.WritebackOutbox{}, err
	}
	o.Status = repository.WritebackOutboxStatus(status)
	if claimedBy != nil {
		o.ClaimedBy = *claimedBy
	}
	return o, nil
}

// Enqueue inserts one outbox row. Callers MUST invoke this inside
// Registry.WithTx, in the same transaction as the Promote/RecordEvent call
// it follows -- see server/handlers/promotion.go's enqueueWriteback and
// ARCHITECTURE.md "Writeback: outbox -> Temporal".
func (r *writebackRepo) Enqueue(ctx context.Context, o repository.WritebackOutbox) (*repository.WritebackOutbox, error) {
	row := r.ex.QueryRow(ctx, `
		INSERT INTO writeback_outbox (promotion_id, environment_id, environment_key, domain, event_id, state_hash)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING `+writebackColumns,
		o.PromotionID, o.EnvironmentID, o.EnvironmentKey, o.Domain, o.EventID, o.StateHash)
	out, err := scanWriteback(row)
	if err != nil {
		return nil, fmt.Errorf("enqueue writeback for promotion %s: %w", o.PromotionID, err)
	}
	return &out, nil
}

// ClaimBatch is one atomic statement: an UPDATE whose WHERE clause selects
// from a subquery using `FOR UPDATE SKIP LOCKED`, so concurrent workers
// racing this call never claim the same row -- Postgres holds the row locks
// from the subquery through the UPDATE within the same statement. Selects
// every 'pending' row plus every 'claimed' row whose claimed_at predates
// staleAfter, so a worker killed mid-run (see AR-4b's exit criterion) does
// not strand its claims forever.
func (r *writebackRepo) ClaimBatch(ctx context.Context, workerID string, limit int, staleAfter time.Duration) ([]repository.WritebackOutbox, error) {
	staleBefore := time.Now().UTC().Add(-staleAfter)
	rows, err := r.ex.Query(ctx, `
		UPDATE writeback_outbox
		SET status = 'claimed', claimed_by = $1, claimed_at = NOW(), attempts = attempts + 1
		WHERE outbox_id IN (
			SELECT outbox_id FROM writeback_outbox
			WHERE status = 'pending' OR (status = 'claimed' AND claimed_at < $2)
			ORDER BY created_at
			LIMIT $3
			FOR UPDATE SKIP LOCKED
		)
		RETURNING `+writebackColumns,
		workerID, staleBefore, limit)
	if err != nil {
		return nil, fmt.Errorf("claim writeback outbox batch: %w", err)
	}
	defer rows.Close()

	var out []repository.WritebackOutbox
	for rows.Next() {
		o, err := scanWriteback(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// MarkDone marks outboxID done. Terminal: ClaimBatch never selects a done
// row again.
func (r *writebackRepo) MarkDone(ctx context.Context, outboxID, workflowID, runID string) error {
	tag, err := r.ex.Exec(ctx, `
		UPDATE writeback_outbox
		SET status = 'done', workflow_id = $2, completed_at = NOW()
		WHERE outbox_id = $1`,
		outboxID, workflowID)
	if err != nil {
		return fmt.Errorf("mark writeback outbox %s done: %w", outboxID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("mark writeback outbox %s done: %w", outboxID, repository.ErrNotFound)
	}
	// runID is recorded on promotion_event by the caller (worker/outbox),
	// not on this table -- writeback_outbox only stores workflow_id,
	// matching the "workflow id, not run id" convention on
	// promotion_event.temporal_workflow_id in 003_promotion.up.sql.
	_ = runID
	return nil
}

// MarkFailed records an error and returns outboxID to 'pending' so the next
// ClaimBatch reclaims it immediately rather than waiting out the staleness
// window.
func (r *writebackRepo) MarkFailed(ctx context.Context, outboxID, errMsg string) error {
	tag, err := r.ex.Exec(ctx, `
		UPDATE writeback_outbox
		SET status = 'pending', last_error = $2, claimed_by = NULL, claimed_at = NULL
		WHERE outbox_id = $1`,
		outboxID, errMsg)
	if err != nil {
		return fmt.Errorf("mark writeback outbox %s failed: %w", outboxID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("mark writeback outbox %s failed: %w", outboxID, repository.ErrNotFound)
	}
	return nil
}

func (r *writebackRepo) Get(ctx context.Context, outboxID string) (*repository.WritebackOutbox, error) {
	row := r.ex.QueryRow(ctx, `SELECT `+writebackColumns+` FROM writeback_outbox WHERE outbox_id = $1`, outboxID)
	o, err := scanWriteback(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	return &o, nil
}
