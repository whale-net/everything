-- Reverse migration 020: drop person_oidc_identity, then restore
-- person.google_subject's NOT NULL constraint the up migration dropped.
-- Exact inverse only holds if no auto-provisioned (NULL google_subject)
-- Person rows exist at rollback time -- true for any test/dev database
-- this migration's own reversibility check runs against (a fresh
-- up/down/up cycle with no whagent-net traffic in between), but an
-- operator rolling back a live database that has already auto-provisioned
-- whagent-net Persons must delete or backfill those rows first, or this
-- ALTER fails loudly (a Postgres NOT NULL violation) rather than silently
-- corrupting data.
DROP TABLE IF EXISTS person_oidc_identity;
ALTER TABLE person ALTER COLUMN google_subject SET NOT NULL;
