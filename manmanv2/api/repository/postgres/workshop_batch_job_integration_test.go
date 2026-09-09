//go:build integration

// This file only builds under the "integration" build tag, same as this
// package's server_port_range/action integration precedent. It exercises
// WorkshopBatchJobRepository (task #2176) against a real Postgres instance:
// aggregate-header CRUD, per-item creation/update, the display_order
// ordering ListBatchJobItems relies on, and the NFR1/NFR2 shape guarantee
// (no sgc_id column or SGC-scoped uniqueness on either new table) that only
// information_schema can actually prove.
//
// Schema here is hand-written, self-contained DDL mirroring exactly the
// pieces of manmanv2/migrate/migrations/039_workshop_batch_jobs.up.sql and
// its games/workshop_libraries/workshop_addons FK targets from
// 001_initial_schema.up.sql / 021_workshop_addons.up.sql /
// 023_workshop_libraries.up.sql -- per dbtest's README ("Options.Schema
// should be self-contained DDL").
package postgres

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/libs/go/dbtest"
	manman "github.com/whale-net/everything/manmanv2/models"
)

const workshopBatchJobSchema = `
	CREATE TABLE games (
		game_id BIGSERIAL PRIMARY KEY,
		name VARCHAR(255) NOT NULL UNIQUE
	);

	CREATE TABLE workshop_libraries (
		library_id BIGSERIAL PRIMARY KEY,
		game_id BIGINT NOT NULL REFERENCES games(game_id) ON DELETE CASCADE,
		name VARCHAR(255) NOT NULL
	);

	CREATE TABLE workshop_addons (
		addon_id BIGSERIAL PRIMARY KEY,
		game_id BIGINT NOT NULL REFERENCES games(game_id) ON DELETE CASCADE,
		workshop_id VARCHAR(255) NOT NULL,
		name VARCHAR(500) NOT NULL
	);

	CREATE TABLE workshop_batch_jobs (
		batch_job_id BIGSERIAL PRIMARY KEY,
		job_type TEXT NOT NULL,
		game_id BIGINT NOT NULL REFERENCES games(game_id),
		library_id BIGINT NULL REFERENCES workshop_libraries(library_id),
		source_input TEXT,
		status TEXT NOT NULL DEFAULT 'pending',
		total_items INT NOT NULL DEFAULT 0,
		succeeded_items INT NOT NULL DEFAULT 0,
		failed_items INT NOT NULL DEFAULT 0,
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		CONSTRAINT workshop_batch_jobs_job_type CHECK (job_type IN ('collection_add', 'batch_create')),
		CONSTRAINT workshop_batch_jobs_status CHECK (status IN ('pending', 'running', 'completed', 'completed_with_errors', 'failed'))
	);

	CREATE TABLE workshop_batch_job_items (
		batch_job_item_id BIGSERIAL PRIMARY KEY,
		batch_job_id BIGINT NOT NULL REFERENCES workshop_batch_jobs(batch_job_id) ON DELETE CASCADE,
		raw_input TEXT NOT NULL,
		workshop_id TEXT NULL,
		addon_id BIGINT NULL REFERENCES workshop_addons(addon_id),
		status TEXT NOT NULL DEFAULT 'pending',
		error_message TEXT NULL,
		display_order INT NOT NULL,
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		CONSTRAINT workshop_batch_job_items_status CHECK (status IN ('pending', 'succeeded', 'failed'))
	);

	CREATE INDEX idx_workshop_batch_job_items_job_order
		ON workshop_batch_job_items(batch_job_id, display_order);
`

func newWorkshopBatchJobHarness(t *testing.T) (*pgxpool.Pool, *WorkshopBatchJobRepository) {
	t.Helper()
	db := dbtest.NewPostgres(context.Background(), t, dbtest.Options{Schema: workshopBatchJobSchema})
	pool := db.Pool

	if _, err := pool.Exec(context.Background(), `INSERT INTO games (game_id, name) VALUES (7, 'game-7')`); err != nil {
		t.Fatalf("seed game: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `INSERT INTO workshop_libraries (library_id, game_id, name) VALUES (9, 7, 'lib-9')`); err != nil {
		t.Fatalf("seed library: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `INSERT INTO workshop_addons (addon_id, game_id, workshop_id, name) VALUES (11, 7, '450814997', 'addon-11')`); err != nil {
		t.Fatalf("seed addon: %v", err)
	}
	return pool, NewWorkshopBatchJobRepository(pool)
}

func TestWorkshopBatchJob_CreateAndGetRoundTrip(t *testing.T) {
	_, repo := newWorkshopBatchJobHarness(t)
	ctx := context.Background()

	libraryID := int64(9)
	source := "https://steamcommunity.com/sharedfiles/filedetails/?id=123"
	created, err := repo.CreateBatchJob(ctx, &manman.WorkshopBatchJob{
		JobType:     "collection_add",
		GameID:      7,
		LibraryID:   &libraryID,
		SourceInput: &source,
		TotalItems:  3,
	})
	if err != nil {
		t.Fatalf("CreateBatchJob: %v", err)
	}
	if created.BatchJobID == 0 {
		t.Fatal("expected a non-zero BatchJobID")
	}
	if created.Status != "pending" {
		t.Fatalf("Status = %q, want default %q", created.Status, "pending")
	}
	if created.TotalItems != 3 {
		t.Fatalf("TotalItems = %d, want 3", created.TotalItems)
	}

	fetched, err := repo.GetBatchJob(ctx, created.BatchJobID)
	if err != nil {
		t.Fatalf("GetBatchJob: %v", err)
	}
	if fetched.JobType != "collection_add" || fetched.GameID != 7 {
		t.Fatalf("fetched job = %+v, want JobType=collection_add GameID=7", fetched)
	}
	if fetched.LibraryID == nil || *fetched.LibraryID != 9 {
		t.Fatalf("fetched LibraryID = %v, want *9", fetched.LibraryID)
	}
	if fetched.SourceInput == nil || *fetched.SourceInput != source {
		t.Fatalf("fetched SourceInput = %v, want %q", fetched.SourceInput, source)
	}
}

func TestWorkshopBatchJob_CreateBatchJobItemsPreservesDisplayOrder(t *testing.T) {
	_, repo := newWorkshopBatchJobHarness(t)
	ctx := context.Background()

	job, err := repo.CreateBatchJob(ctx, &manman.WorkshopBatchJob{JobType: "batch_create", GameID: 7, TotalItems: 3})
	if err != nil {
		t.Fatalf("CreateBatchJob: %v", err)
	}

	items := []*manman.WorkshopBatchJobItem{
		{RawInput: "111111111", DisplayOrder: 0},
		{RawInput: "222222222", DisplayOrder: 1},
		{RawInput: "not-a-thing", DisplayOrder: 2, Status: "failed"},
	}
	if err := repo.CreateBatchJobItems(ctx, job.BatchJobID, items); err != nil {
		t.Fatalf("CreateBatchJobItems: %v", err)
	}
	for i, item := range items {
		if item.BatchJobItemID == 0 {
			t.Fatalf("item %d: expected a populated BatchJobItemID after insert", i)
		}
		if item.BatchJobID != job.BatchJobID {
			t.Fatalf("item %d: BatchJobID = %d, want %d", i, item.BatchJobID, job.BatchJobID)
		}
	}
	// Default status ("pending") must be applied when the caller leaves it
	// unset, matching CreateBatchJob's default-fill behavior.
	if items[0].Status != "" && items[0].Status != "pending" {
		t.Fatalf("item 0 Status field unexpectedly mutated to %q", items[0].Status)
	}

	listed, err := repo.ListBatchJobItems(ctx, job.BatchJobID)
	if err != nil {
		t.Fatalf("ListBatchJobItems: %v", err)
	}
	if len(listed) != 3 {
		t.Fatalf("listed = %d items, want 3", len(listed))
	}
	wantRaw := []string{"111111111", "222222222", "not-a-thing"}
	for i, item := range listed {
		if item.RawInput != wantRaw[i] {
			t.Fatalf("listed[%d].RawInput = %q, want %q (display_order must be preserved)", i, item.RawInput, wantRaw[i])
		}
		if item.DisplayOrder != i {
			t.Fatalf("listed[%d].DisplayOrder = %d, want %d", i, item.DisplayOrder, i)
		}
	}
	if listed[0].Status != "pending" {
		t.Fatalf("listed[0].Status = %q, want default pending", listed[0].Status)
	}
	if listed[2].Status != "failed" {
		t.Fatalf("listed[2].Status = %q, want failed (explicit status must not be overridden)", listed[2].Status)
	}
}

func TestWorkshopBatchJob_UpdateItemResultAndJobStatus(t *testing.T) {
	_, repo := newWorkshopBatchJobHarness(t)
	ctx := context.Background()

	job, err := repo.CreateBatchJob(ctx, &manman.WorkshopBatchJob{JobType: "batch_create", GameID: 7, TotalItems: 2})
	if err != nil {
		t.Fatalf("CreateBatchJob: %v", err)
	}
	items := []*manman.WorkshopBatchJobItem{
		{RawInput: "450814997", DisplayOrder: 0},
		{RawInput: "bad-line", DisplayOrder: 1},
	}
	if err := repo.CreateBatchJobItems(ctx, job.BatchJobID, items); err != nil {
		t.Fatalf("CreateBatchJobItems: %v", err)
	}

	addonID := int64(11)
	if err := repo.UpdateBatchJobItemResult(ctx, items[0].BatchJobItemID, "succeeded", &addonID, nil); err != nil {
		t.Fatalf("UpdateBatchJobItemResult (success): %v", err)
	}
	errMsg := "not a numeric Workshop ID or recognized Workshop URL"
	if err := repo.UpdateBatchJobItemResult(ctx, items[1].BatchJobItemID, "failed", nil, &errMsg); err != nil {
		t.Fatalf("UpdateBatchJobItemResult (failure): %v", err)
	}

	listed, err := repo.ListBatchJobItems(ctx, job.BatchJobID)
	if err != nil {
		t.Fatalf("ListBatchJobItems: %v", err)
	}
	if listed[0].Status != "succeeded" || listed[0].AddonID == nil || *listed[0].AddonID != addonID {
		t.Fatalf("listed[0] = %+v, want succeeded with AddonID=%d", listed[0], addonID)
	}
	if listed[1].Status != "failed" || listed[1].ErrorMessage == nil || *listed[1].ErrorMessage != errMsg {
		t.Fatalf("listed[1] = %+v, want failed with ErrorMessage=%q", listed[1], errMsg)
	}

	if err := repo.UpdateBatchJobStatus(ctx, job.BatchJobID, "completed_with_errors", 1, 1); err != nil {
		t.Fatalf("UpdateBatchJobStatus: %v", err)
	}
	updated, err := repo.GetBatchJob(ctx, job.BatchJobID)
	if err != nil {
		t.Fatalf("GetBatchJob: %v", err)
	}
	if updated.Status != "completed_with_errors" || updated.SucceededItems != 1 || updated.FailedItems != 1 {
		t.Fatalf("updated job = %+v, want completed_with_errors/1/1", updated)
	}
}

func TestWorkshopBatchJob_ListBatchJobsNewestFirstScopedToGame(t *testing.T) {
	pool, repo := newWorkshopBatchJobHarness(t)
	ctx := context.Background()

	if _, err := pool.Exec(ctx, `INSERT INTO games (game_id, name) VALUES (8, 'game-8')`); err != nil {
		t.Fatalf("seed second game: %v", err)
	}

	for i := 0; i < 3; i++ {
		if _, err := repo.CreateBatchJob(ctx, &manman.WorkshopBatchJob{JobType: "batch_create", GameID: 7}); err != nil {
			t.Fatalf("seed job %d for game 7: %v", i, err)
		}
	}
	if _, err := repo.CreateBatchJob(ctx, &manman.WorkshopBatchJob{JobType: "batch_create", GameID: 8}); err != nil {
		t.Fatalf("seed job for game 8: %v", err)
	}

	listed, err := repo.ListBatchJobs(ctx, 7, 50)
	if err != nil {
		t.Fatalf("ListBatchJobs: %v", err)
	}
	if len(listed) != 3 {
		t.Fatalf("listed = %d jobs, want 3 (scoped to game 7 only)", len(listed))
	}
	for i := 0; i+1 < len(listed); i++ {
		if listed[i].BatchJobID < listed[i+1].BatchJobID {
			t.Fatalf("ListBatchJobs not newest-first: %+v", listed)
		}
	}

	limited, err := repo.ListBatchJobs(ctx, 7, 2)
	if err != nil {
		t.Fatalf("ListBatchJobs with limit: %v", err)
	}
	if len(limited) != 2 {
		t.Fatalf("limited = %d jobs, want 2", len(limited))
	}
}

// TestWorkshopBatchJob_TablesHaveNoSGCColumn proves NFR1/NFR2 (load-bearing
// per the migration's own header comment) at the schema level: neither new
// table may carry an sgc_id column. Asserting against information_schema is
// what makes this enforceable rather than aspirational -- a hand read of the
// migration file can't catch a later, careless ALTER TABLE the way this
// test does.
func TestWorkshopBatchJob_TablesHaveNoSGCColumn(t *testing.T) {
	pool, _ := newWorkshopBatchJobHarness(t)
	ctx := context.Background()

	for _, table := range []string{"workshop_batch_jobs", "workshop_batch_job_items"} {
		var exists bool
		if err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_name = $1 AND column_name = 'sgc_id'
			)`, table,
		).Scan(&exists); err != nil {
			t.Fatalf("query information_schema.columns for %s: %v", table, err)
		}
		if exists {
			t.Fatalf("table %s must not carry an sgc_id column (NFR1/NFR2)", table)
		}
	}
}
