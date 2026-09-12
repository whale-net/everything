-- Reverse migration 021.

DROP INDEX IF EXISTS link_assertion_consumption_expires_at;
DROP TABLE IF EXISTS link_assertion_consumption;
