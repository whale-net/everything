-- Migration 025: Support reference table (FR80)
--
-- A household member can produce a support reference: a short-lived, opaque,
-- revocable code that resolves — for an admin, in the standing lane (FR10)
-- — to that household. Producing one requires no description of the problem
-- and discloses no household data in itself.
--
-- Short-lived token with expiry, NOT SCD2 (NFR6.3): a reference is a one-shot
-- artifact with a lifetime bound. Revocation sets revoked_at; a code is never
-- deleted so its use remains visible in the household's activity list (FR9).
--
-- code is stored hashed (SHA-256, hex) rather than in plaintext so that a
-- database read alone cannot recover a valid, resolvable code. The plaintext
-- code is disclosed to the household member exactly once, at creation.

-- ── support_reference table ────────────────────────────────────────────────────

CREATE TABLE support_reference (
    support_reference_id BIGSERIAL PRIMARY KEY,
    household_id          BIGINT NOT NULL REFERENCES household(household_id) ON DELETE CASCADE,
    code_hash             VARCHAR(64) NOT NULL,   -- SHA-256 hex digest of the opaque code
    created_by            VARCHAR(255) NOT NULL,  -- household member who produced it
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at            TIMESTAMPTZ NOT NULL,   -- short lifetime bound
    revoked_at            TIMESTAMPTZ             -- NULL = not revoked
);

-- Lookup by hash is the only way a reference is ever resolved; this index
-- backs both the household member's own view and the admin standing-lane
-- resolve path. Not unique: a hash collision or reused code across expired
-- rows must not throw at insert time, resolution logic picks the most
-- recent unrevoked, unexpired match.
CREATE INDEX idx_support_reference_code_hash ON support_reference(code_hash);

-- Listing references for a household (owner's own view, FR9 activity trace).
CREATE INDEX idx_support_reference_household ON support_reference(household_id, created_at DESC);
