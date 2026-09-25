-- Workshop batch-job persistence: the shared foundation for Workshop
-- collection bulk-add (FR1) and mixed-format batch create (FR2/FR3), plus
-- per-item outcome tracking that makes per-item atomicity observable (NFR5).
-- See #2175/#2176.
--
-- NFR1/NFR2 (load-bearing): neither table carries an sgc_id column or any
-- SGC/host/deployment-scoped uniqueness constraint. Addon writes produced by
-- these jobs land in the existing library-scoped workshop_library_addons
-- junction, which has no SGC dimension -- batch jobs must stay at the same
-- library/game scope, not narrower.

CREATE TABLE IF NOT EXISTS workshop_batch_jobs (
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

CREATE TABLE IF NOT EXISTS workshop_batch_job_items (
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

CREATE INDEX IF NOT EXISTS idx_workshop_batch_job_items_job_order
    ON workshop_batch_job_items(batch_job_id, display_order);
