-- Rollback App Registry Artifact Lifecycle States (AR-7b, issue #558)
--
-- Matches the style of 005_version_allocation.down.sql: simple, and does
-- not attempt to preserve data across the schema change it is undoing --
-- the same tradeoff 003's down (DROP TABLE promotion, losing promotion
-- history) already makes for this repo's migrations.
--
-- Rows in 'allocated', 'publishing', or 'failed' state have no meaning
-- once state/digest-nullability go away (the world this down migration
-- returns to required digest/build_id NOT NULL on every artifact row), so
-- they are deleted rather than coerced into a shape the old schema can't
-- represent. Only 'published' rows -- the only state the pre-AR-7b schema
-- ever had -- survive the rollback.
DELETE FROM artifact WHERE state != 'published';

DROP INDEX IF EXISTS artifact_state_idx;

ALTER TABLE artifact DROP CONSTRAINT IF EXISTS artifact_state_shape;

-- Restore the pre-AR-7b column shape. Safe now: every surviving row is
-- 'published' and therefore already carries a real digest/build_id/
-- published_at (see the up migration's backfill and the state_shape CHECK
-- constraint it enforced while this schema was live).
ALTER TABLE artifact ALTER COLUMN digest SET NOT NULL;
ALTER TABLE artifact ALTER COLUMN build_id SET NOT NULL;
ALTER TABLE artifact ALTER COLUMN published_at SET NOT NULL;
ALTER TABLE artifact ALTER COLUMN published_at SET DEFAULT NOW();

DROP INDEX IF EXISTS artifact_digest_idx;
CREATE UNIQUE INDEX artifact_digest_idx ON artifact (digest);

ALTER TABLE artifact DROP COLUMN IF EXISTS state;
ALTER TABLE artifact DROP COLUMN IF EXISTS provenance;
ALTER TABLE artifact DROP COLUMN IF EXISTS version_source;
ALTER TABLE artifact DROP COLUMN IF EXISTS state_changed_at;
ALTER TABLE artifact DROP COLUMN IF EXISTS fail_reason;

-- Recreate version_allocation exactly as 005_version_allocation.up.sql
-- defined it. Empty on recreation -- any 'allocated' rows this migration's
-- up direction folded into `artifact` were deleted above, matching this
-- repo's other down migrations' no-data-preservation convention.
CREATE TABLE version_allocation (
    version_allocation_id  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id                UUID NOT NULL,
    kind                    TEXT NOT NULL CHECK (kind IN ('image', 'chart')),
    version                 TEXT NOT NULL,
    version_major           INTEGER NOT NULL,
    version_minor           INTEGER NOT NULL,
    version_patch           INTEGER NOT NULL,
    allocated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX version_allocation_idx ON version_allocation (owner_id, kind, version);
CREATE INDEX version_allocation_order_idx
    ON version_allocation (owner_id, kind, version_major DESC, version_minor DESC, version_patch DESC);
