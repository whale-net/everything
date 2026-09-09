-- 002_transcript_archive (issue #2240, FR8/LB1): the cold-tier index for
-- transcript archival -- one row per session that has been archived to S3,
-- pointing at the object plus enough bookkeeping (event_count/min_seq/
-- max_seq) for a reader to sanity-check what it downloads without opening
-- it first. Append-only-ish, like transcript_event -- explicitly NOT SCD2
-- (AGENTS.md's SCD2 convention is for versioned current-value rows; this
-- table has exactly one row per session, written once by the archiver and
-- never revised except hot_trimmed_at).
--
-- hot_trimmed_at is NULL until whagent_net/archiver (FR7, a later task)
-- trims the session's transcript_event rows -- this task's TranscriptStore
-- reads never consult it; a row existing here is itself the "hydrate from
-- S3" signal (see whagent_net/session/archive.go and
-- whagent_net/ARCHITECTURE.md "Transcript storage tiers").
CREATE TABLE transcript_archive (
    session_id     UUID PRIMARY KEY REFERENCES sessions (session_id),
    s3_bucket      TEXT NOT NULL,
    s3_key         TEXT NOT NULL,
    event_count    BIGINT NOT NULL,
    min_seq        BIGINT NOT NULL,
    max_seq        BIGINT NOT NULL,
    archived_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    hot_trimmed_at TIMESTAMPTZ NULL
);
