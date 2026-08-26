-- Migration 024: Elevation table (FR10)
--
-- Creates the append-only elevation table for time-boxed admin elevation
-- against a named target household. Elevation is entered deliberately with
-- a stated reason, is time-boxed (60 minutes by default, configurable),
-- expires automatically, and stamps both admin subject and target household
-- on every resulting write. Remaining time is readable while elevated (A22).
--
-- Elevation is a short-lived token-with-expiry shape, not SCD2 (NFR6.3):
-- renewal inserts a new row (optionally linked via renewed_from) rather than
-- closing and reopening a current-value row. This keeps every elevation
-- window — including superseded ones — permanently in the history for audit.

-- ── elevation table (append-only) ─────────────────────────────────────────────

CREATE TABLE elevation (
    elevation_id         BIGSERIAL PRIMARY KEY,
    admin_subject        VARCHAR(255) NOT NULL,   -- authenticated admin principal
    target_household_id  BIGINT NOT NULL REFERENCES household(household_id) ON DELETE RESTRICT,
    reason                TEXT NOT NULL,           -- stated reason; required, non-empty
    entered_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at            TIMESTAMPTZ NOT NULL,    -- entered_at + configured elevation duration
    -- renewed_from links a renewal to the elevation window it extends.
    -- NULL means this row is an initial EnterElevation, not a renewal.
    renewed_from          BIGINT REFERENCES elevation(elevation_id) ON DELETE SET NULL
);

-- Append-only trigger: reject UPDATE and DELETE operations, matching
-- audit_record's append-only enforcement (migration 022).
CREATE OR REPLACE FUNCTION elevation_append_only()
RETURNS TRIGGER AS $$
BEGIN
    IF TG_OP = 'UPDATE' OR TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'elevation is append-only: UPDATE and DELETE are not permitted';
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER elevation_no_modify
BEFORE UPDATE OR DELETE ON elevation
FOR EACH ROW EXECUTE FUNCTION elevation_append_only();

-- Lookup index: find the admin's most recent elevation window against a
-- target household (current active elevation is the latest row where
-- expires_at > NOW()).
CREATE INDEX idx_elevation_lookup
    ON elevation(admin_subject, target_household_id, expires_at DESC);

-- Index for finding all elevations against a household (for audit/history).
CREATE INDEX idx_elevation_target_household
    ON elevation(target_household_id, entered_at DESC);
