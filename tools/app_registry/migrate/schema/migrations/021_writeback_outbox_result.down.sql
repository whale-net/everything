ALTER TABLE writeback_outbox
    DROP COLUMN location,
    DROP COLUMN commit_sha;
