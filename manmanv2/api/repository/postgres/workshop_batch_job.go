package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/whale-net/everything/manmanv2/models"
)

// WorkshopBatchJobRepository is the postgres-backed implementation of
// repository.WorkshopBatchJobRepository. See repository.go for the interface
// contract and migration 039 for the schema (NFR1/NFR2: no sgc_id or
// SGC-scoped uniqueness on either table).
type WorkshopBatchJobRepository struct {
	db *pgxpool.Pool
}

func NewWorkshopBatchJobRepository(db *pgxpool.Pool) *WorkshopBatchJobRepository {
	return &WorkshopBatchJobRepository{db: db}
}

const workshopBatchJobColumns = `
	batch_job_id, job_type, game_id, library_id, source_input, status,
	total_items, succeeded_items, failed_items, created_at, updated_at
`

func scanWorkshopBatchJob(row pgx.Row) (*manman.WorkshopBatchJob, error) {
	job := &manman.WorkshopBatchJob{}
	err := row.Scan(
		&job.BatchJobID,
		&job.JobType,
		&job.GameID,
		&job.LibraryID,
		&job.SourceInput,
		&job.Status,
		&job.TotalItems,
		&job.SucceededItems,
		&job.FailedItems,
		&job.CreatedAt,
		&job.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return job, nil
}

const workshopBatchJobItemColumns = `
	batch_job_item_id, batch_job_id, raw_input, workshop_id, addon_id, status,
	error_message, display_order, created_at, updated_at
`

func scanWorkshopBatchJobItem(row pgx.Row) (*manman.WorkshopBatchJobItem, error) {
	item := &manman.WorkshopBatchJobItem{}
	err := row.Scan(
		&item.BatchJobItemID,
		&item.BatchJobID,
		&item.RawInput,
		&item.WorkshopID,
		&item.AddonID,
		&item.Status,
		&item.ErrorMessage,
		&item.DisplayOrder,
		&item.CreatedAt,
		&item.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return item, nil
}

// CreateBatchJob inserts a new batch job header.
func (r *WorkshopBatchJobRepository) CreateBatchJob(ctx context.Context, job *manman.WorkshopBatchJob) (*manman.WorkshopBatchJob, error) {
	status := job.Status
	if status == "" {
		status = "pending"
	}

	query := `
		INSERT INTO workshop_batch_jobs (job_type, game_id, library_id, source_input, status, total_items)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING ` + workshopBatchJobColumns

	created, err := scanWorkshopBatchJob(r.db.QueryRow(
		ctx, query,
		job.JobType,
		job.GameID,
		job.LibraryID,
		job.SourceInput,
		status,
		job.TotalItems,
	))
	if err != nil {
		return nil, err
	}
	return created, nil
}

// CreateBatchJobItems inserts the per-item rows for a batch job, preserving
// caller-supplied display order. Uses a single batched round trip via
// pgx.Batch rather than one INSERT per item.
func (r *WorkshopBatchJobRepository) CreateBatchJobItems(ctx context.Context, batchJobID int64, items []*manman.WorkshopBatchJobItem) error {
	if len(items) == 0 {
		return nil
	}

	const query = `
		INSERT INTO workshop_batch_job_items (batch_job_id, raw_input, workshop_id, status, error_message, display_order)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING batch_job_item_id, created_at, updated_at
	`

	batch := &pgx.Batch{}
	for _, item := range items {
		status := item.Status
		if status == "" {
			status = "pending"
		}
		batch.Queue(query, batchJobID, item.RawInput, item.WorkshopID, status, item.ErrorMessage, item.DisplayOrder)
	}

	results := r.db.SendBatch(ctx, batch)
	defer results.Close()

	for _, item := range items {
		if err := results.QueryRow().Scan(&item.BatchJobItemID, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return err
		}
		item.BatchJobID = batchJobID
	}

	return results.Close()
}

// UpdateBatchJobItemResult records the outcome of parsing/creating a single
// batch job item.
func (r *WorkshopBatchJobRepository) UpdateBatchJobItemResult(ctx context.Context, batchJobItemID int64, status string, addonID *int64, errorMessage *string) error {
	query := `
		UPDATE workshop_batch_job_items
		SET status = $2, addon_id = $3, error_message = $4, updated_at = CURRENT_TIMESTAMP
		WHERE batch_job_item_id = $1
	`
	_, err := r.db.Exec(ctx, query, batchJobItemID, status, addonID, errorMessage)
	return err
}

// UpdateBatchJobStatus updates the aggregate status/counters on a batch job.
func (r *WorkshopBatchJobRepository) UpdateBatchJobStatus(ctx context.Context, batchJobID int64, status string, succeeded, failed int) error {
	query := `
		UPDATE workshop_batch_jobs
		SET status = $2, succeeded_items = $3, failed_items = $4, updated_at = CURRENT_TIMESTAMP
		WHERE batch_job_id = $1
	`
	_, err := r.db.Exec(ctx, query, batchJobID, status, succeeded, failed)
	return err
}

// GetBatchJob retrieves a batch job by ID.
func (r *WorkshopBatchJobRepository) GetBatchJob(ctx context.Context, batchJobID int64) (*manman.WorkshopBatchJob, error) {
	query := `
		SELECT ` + workshopBatchJobColumns + `
		FROM workshop_batch_jobs
		WHERE batch_job_id = $1
	`
	return scanWorkshopBatchJob(r.db.QueryRow(ctx, query, batchJobID))
}

// ListBatchJobItems retrieves all items for a batch job, ordered for display.
func (r *WorkshopBatchJobRepository) ListBatchJobItems(ctx context.Context, batchJobID int64) ([]*manman.WorkshopBatchJobItem, error) {
	query := `
		SELECT ` + workshopBatchJobItemColumns + `
		FROM workshop_batch_job_items
		WHERE batch_job_id = $1
		ORDER BY display_order, batch_job_item_id
	`

	rows, err := r.db.Query(ctx, query, batchJobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []*manman.WorkshopBatchJobItem
	for rows.Next() {
		item, err := scanWorkshopBatchJobItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// ListBatchJobs retrieves the most recent batch jobs for a game, newest
// first.
func (r *WorkshopBatchJobRepository) ListBatchJobs(ctx context.Context, gameID int64, limit int) ([]*manman.WorkshopBatchJob, error) {
	if limit <= 0 {
		limit = 50
	}

	query := `
		SELECT ` + workshopBatchJobColumns + `
		FROM workshop_batch_jobs
		WHERE game_id = $1
		ORDER BY batch_job_id DESC
		LIMIT $2
	`

	rows, err := r.db.Query(ctx, query, gameID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []*manman.WorkshopBatchJob
	for rows.Next() {
		job, err := scanWorkshopBatchJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}
