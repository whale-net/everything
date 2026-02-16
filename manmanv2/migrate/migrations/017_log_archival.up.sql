-- Add archival columns to log_references table for S3-based historical log storage

-- Add new columns
ALTER TABLE log_references ADD COLUMN sgc_id BIGINT;
ALTER TABLE log_references ADD COLUMN minute_timestamp TIMESTAMP;
ALTER TABLE log_references ADD COLUMN state VARCHAR(20) DEFAULT 'complete';
ALTER TABLE log_references ADD COLUMN appended_at TIMESTAMP;

-- Backfill sgc_id from sessions table for existing records
UPDATE log_references lr
SET sgc_id = s.sgc_id
FROM sessions s
WHERE lr.session_id = s.session_id
  AND lr.sgc_id IS NULL;

-- Add foreign key constraint
ALTER TABLE log_references
  ADD CONSTRAINT fk_log_references_sgc
  FOREIGN KEY (sgc_id)
  REFERENCES server_game_configs(sgc_id)
  ON DELETE CASCADE;

-- Create indexes for efficient querying
CREATE INDEX idx_log_refs_sgc_minute ON log_references(sgc_id, minute_timestamp DESC);
CREATE INDEX idx_log_refs_state ON log_references(state) WHERE state = 'pending';

-- Add comments
COMMENT ON COLUMN log_references.sgc_id IS 'Server Game Config ID for partitioning and efficient cleanup';
COMMENT ON COLUMN log_references.minute_timestamp IS 'Truncated timestamp for minute window (used for batching)';
COMMENT ON COLUMN log_references.state IS 'Archival state: pending | complete';
COMMENT ON COLUMN log_references.appended_at IS 'Timestamp when blob was appended (rare case when logs span minute boundaries)';
