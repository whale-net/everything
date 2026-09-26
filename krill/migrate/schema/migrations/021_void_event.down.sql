-- 021_void_event down: drop the void tombstone register and the two
-- non-partial parent indexes it added. Migration 002's partial
-- `(feature_set_id) WHERE valid_to IS NULL` indexes are left exactly as
-- they were -- they were never dropped, only supplemented.
--
-- Dropping void_event discards the audit record of every void applied
-- while this migration was live -- the tombstoned spec rows themselves are
-- untouched and stay closed, so the SCD2 history survives, but the actor
-- and reason do not. That asymmetry is the reason migration 021 is not
-- something to roll back casually.

DROP INDEX IF EXISTS load_bearing_decision_feature_set_all_idx;
DROP INDEX IF EXISTS feature_feature_set_all_idx;

DROP TABLE IF EXISTS void_event;
