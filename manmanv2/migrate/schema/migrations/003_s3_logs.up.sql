-- Update log_references table to reflect S3 storage
-- The file_path column now stores S3 URLs (e.g., s3://bucket/logs/session_id/timestamp-batch_id.log)
-- No structural changes needed, just updating comments for clarity

COMMENT ON COLUMN log_references.file_path IS 'S3 URL for the log file (format: s3://bucket/key)';
