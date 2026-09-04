-- M2 schema groundwork for FR3 (board rename), FR10 (leaflab-local role
-- grants), and NFR4 (corrective-push attempt counter). Schema only -- no Go
-- behavior lands in this migration.

-- -- board.name --------------------------------------------------------------
-- Plain current-value column, per FR3. Deliberately NOT a history table --
-- board name is not an attribution dimension for any reading, matching
-- region.name's precedent under LB6 (see migration 013). Contrast with
-- sensor rename (FR4), which extends the existing sensor_name_history SCD2
-- table -- do not generalize that shape onto board.
--
-- Nullable, no default, no UNIQUE, no length/format constraint. Non-empty
-- is enforced in the API layer (FR3), not by a check constraint -- the
-- milestone explicitly rules out name uniqueness/format validation beyond
-- non-empty. Existing boards get NULL; NULL means "no name set, display
-- device_id".

ALTER TABLE board ADD COLUMN name TEXT;

-- -- leaflab_user_role ---------------------------------------------------------
-- SCD2 role grants, per FR10. leaflab owns its own roles (see
-- leaflab/PRODUCT.md section Non-goals) -- this is NOT read from OIDC
-- realm_access.roles. Mirrors board_owner_history's shape from
-- 013_ownership.up.sql, per AGENTS.md section SCD2: valid_from/valid_to
-- naming (no assigned_at/revoked_at synonyms), and a partial unique index
-- over the open interval only, so "grant, revoke, re-grant" works while
-- forbidding two concurrent open grants of the same role to the same user.
--
-- Roles are free-text; 'admin' is the only role this milestone uses.

CREATE TABLE leaflab_user_role (
    leaflab_user_role_id BIGSERIAL   PRIMARY KEY,
    leaflab_user_id      BIGINT      NOT NULL REFERENCES leaflab_user(leaflab_user_id) ON DELETE CASCADE,
    role                 TEXT        NOT NULL,
    valid_from            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    valid_to              TIMESTAMPTZ
);

CREATE INDEX idx_leaflab_user_role_user_id ON leaflab_user_role(leaflab_user_id);
CREATE UNIQUE INDEX idx_leaflab_user_role_current
    ON leaflab_user_role(leaflab_user_id, role) WHERE valid_to IS NULL;

-- -- seeded first admin ------------------------------------------------------
-- FR10 requires that after this milestone's migrations run, at least one
-- user holds the admin role, with no role-grant UI action needed. This
-- covers the case where leaflab_user already has rows at migration time.
-- Selection rule: earliest leaflab_user_id (lowest id = the longest-lived
-- account, a reasonable default admin absent any other signal). This is a
-- SELECT-driven insert, not a literal id, so it is a no-op (not an error)
-- on an empty leaflab_user table -- the zero-users case is covered
-- separately by the first-sign-in bootstrap in the admin-role task.

INSERT INTO leaflab_user_role (leaflab_user_id, role)
SELECT leaflab_user_id, 'admin'
FROM leaflab_user
ORDER BY leaflab_user_id
LIMIT 1
ON CONFLICT DO NOTHING;

-- -- sensor corrective-push attempt counter -----------------------------------
-- NFR4 requires this counter to live in Postgres, not in
-- processor/cache.go's in-memory SensorCache: leaflab-processor is
-- release_app(..., replicas = 1) and restart-prone by routine redeploys, so
-- an in-memory counter would hand a device that never persists to NVS a
-- fresh 3-attempt budget on every deploy.

ALTER TABLE sensor ADD COLUMN corrective_push_attempts INT NOT NULL DEFAULT 0;

-- Concurrent-guard state (NFR4, first bullet): NULL = no corrective push
-- outstanding for this sensor; non-NULL = the device_config.version of a
-- corrective push issued but not yet acked.

ALTER TABLE sensor ADD COLUMN corrective_push_outstanding_version BIGINT;
