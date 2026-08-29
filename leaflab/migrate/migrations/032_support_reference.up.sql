-- Migration 032: support_reference (Phase 2 scaffold)
--
-- 032 is the next free number: the highest known migration number across
-- every disk worktree for this plan (v1 and v2 branches combined) was 031
-- (plan/1166-v2-1369's/1370's 031_device_config_removal) as of this write --
-- state this in the PR, since migration numbers have collided across
-- sibling branches on this plan multiple times before.
--
-- Discharges FR80's owner-initiated support reference: a household member
-- produces a short-lived, opaque, revocable code that resolves -- for an
-- admin, in the standing lane (FR10.2) -- to that household. Creation
-- discloses no household data and requires no problem description; only
-- the code and its expiry are returned to the caller (Implementation-phase
-- concern, not this migration's).
--
-- NFR6.3: a support reference is a short-lived token with expiry, NOT
-- SCD2 -- explicitly no valid_to column. Compare admin_elevation
-- (029_admin_elevation), which is the same shape for the same reason: a
-- single bounded episode with a hard end (expiry or explicit revocation),
-- not a current-value history to version with valid_from/valid_to.

CREATE TABLE support_reference (
    support_reference_id BIGSERIAL PRIMARY KEY,
    household_id          BIGINT NOT NULL REFERENCES household(household_id) ON DELETE RESTRICT,
    -- A hash of the opaque code, never the code itself (Implementation
    -- phase picks the hash function) -- the plaintext must not be
    -- recoverable from the database. UNIQUE also backs the admin resolve
    -- lookup: exactly one row (or none) per submitted code.
    code_hash              TEXT NOT NULL UNIQUE,
    created_by_subject     TEXT NOT NULL,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at             TIMESTAMPTZ NOT NULL,
    revoked_at             TIMESTAMPTZ NULL,
    -- last_resolved_at/resolve_count are use-tracking fields (FR80's
    -- "existence and use are visible to the owner in FR9's activity
    -- list" is served by the audit rows a resolve also writes -- these two
    -- columns are the cheap in-row counters ListSupportReferences reads
    -- directly, without joining audit_log).
    last_resolved_at       TIMESTAMPTZ NULL,
    resolve_count          INT NOT NULL DEFAULT 0
);

-- Owner-facing ListSupportReferences (FR80) lists a household's references;
-- household_id is the natural filter column for that query.
CREATE INDEX idx_support_reference_household_id ON support_reference(household_id);

-- NFR2's admin resolve must apply identical work (status, body, timing) to
-- unknown, expired and revoked references alike -- it looks up by
-- code_hash regardless of outcome, so no separate "active only" partial
-- index exists here; the UNIQUE constraint above already covers that one
-- lookup shape.
