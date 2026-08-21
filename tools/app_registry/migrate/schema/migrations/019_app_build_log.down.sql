-- Rollback app_build_log (issue #923). Best-effort, matching migration
-- 010's own down.sql precedent for this schema: this table's history is not
-- depended on by anything the down path needs to reconstruct, so a plain
-- drop is sufficient -- its unique index is dropped implicitly with the
-- table.
DROP TABLE app_build_log;
