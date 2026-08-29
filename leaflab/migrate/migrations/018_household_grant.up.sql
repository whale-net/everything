-- Migration 018: household_grant (Phase 2 scaffold, FR7)
--
-- Originally scaffolded as 017 (next free after 016/audit_log in this
-- branch's own migrations/ directory), then renumbered to 018 by the
-- orchestrator: 017 was independently claimed by plant_region_history on
-- a sibling branch (#1357) not merged into this branch's ancestry. Sibling
-- v2 branches on plan/1166 have collided on migration numbers before
-- (013 twice, then 016/017); state this in the PR.
--
-- FR7: a household member grants a named principal time-boxed write
-- access to their household. household_grant is deliberately NOT SCD2
-- (NFR6.3): it is a short-lived, expiring record, not a history-bearing
-- current-value table -- revocation sets revoked_at, it does not close a
-- valid_from/valid_to interval, and there is no valid_to column here at
-- all. Expiry is evaluated at request time against NOW() (Implementation
-- phase's scope resolution) -- no background job marks a grant expired.

CREATE TABLE household_grant (
    grant_id           BIGSERIAL PRIMARY KEY,
    household_id       BIGINT NOT NULL REFERENCES household(household_id) ON DELETE RESTRICT,
    grantee_subject    TEXT NOT NULL,
    granted_by_subject TEXT NOT NULL,
    granted_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at         TIMESTAMPTZ NOT NULL,
    revoked_at         TIMESTAMPTZ NULL,
    reason             TEXT NULL
    -- No valid_to column. Not SCD2 (NFR6.3) -- see this migration's header.
);

-- Scope resolution's "unexpired, unrevoked grants" lookup (Implementation
-- phase) and FR7's "grant disappears... on expiry" / one-action revocation
-- semantics both key off grantee_subject with revoked_at IS NULL; expiry
-- itself (expires_at > NOW()) is evaluated at request time, not indexed
-- separately, per this migration's header.
CREATE INDEX idx_household_grant_grantee_subject_active
    ON household_grant(grantee_subject) WHERE revoked_at IS NULL;

-- FR7's "visible while active" listing: a household member sees who
-- currently has access to their household without an admin
-- (ListHouseholdGrants).
CREATE INDEX idx_household_grant_household_id_active
    ON household_grant(household_id) WHERE revoked_at IS NULL;
