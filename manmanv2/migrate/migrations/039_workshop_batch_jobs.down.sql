-- Remove workshop batch-job persistence

DROP INDEX IF EXISTS idx_workshop_batch_job_items_job_order;
DROP TABLE IF EXISTS workshop_batch_job_items;
DROP TABLE IF EXISTS workshop_batch_jobs;
