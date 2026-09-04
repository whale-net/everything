-- Reverse migration 011. Not losslessly reversible: the dropped cadence
-- values cannot be recovered, so every row is backfilled with the
-- documented default 'weekly' rather than a NULL or a guess. This
-- deliberately does not restore the original per-row data, matching
-- migration 002's/010's down convention.

ALTER TABLE strategy ADD COLUMN cadence TEXT NOT NULL DEFAULT 'weekly'
    CHECK (cadence IN ('weekly', 'biweekly', 'monthly'));
ALTER TABLE strategy ALTER COLUMN cadence DROP DEFAULT;
