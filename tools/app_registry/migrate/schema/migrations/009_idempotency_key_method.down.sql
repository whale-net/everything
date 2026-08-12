-- Rollback App Registry idempotency (idempotency_key, method) scoping
-- (issue #575)
--
-- Matches this repo's other down migrations' no-data-preservation
-- convention (see 007's and 008's down comments): once the up migration is
-- live, two rows CAN legitimately share an idempotency_key with different
-- methods (that is the whole point of the fix), which the pre-migration
-- single-column PRIMARY KEY cannot represent. Rather than pick a winner
-- silently, keep only the most recently created row per idempotency_key
-- and drop the rest before restoring the narrower constraint.
DELETE FROM idempotency_key a
USING idempotency_key b
WHERE a.idempotency_key = b.idempotency_key
  AND a.created_at < b.created_at;

ALTER TABLE idempotency_key DROP CONSTRAINT idempotency_key_pkey;
ALTER TABLE idempotency_key ADD PRIMARY KEY (idempotency_key);
