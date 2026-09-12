-- FR1 (issue #2424, plan #2421): agent_definition gains a required
-- `domain` column -- the *only* input whagent_net/grantkey.ForDomain (FR4)
-- may derive a delegated-grant key from. domain is declared once per
-- agent_definition (one row = one domain), not per tool_set entry -- see
-- ARCHITECTURE.md/PRODUCT.md's NFR2 for why the single-domain-per-
-- definition guarantee is structural rather than a CHECK constraint here.
--
-- Not SCD2 (AGENTS.md "SCD2" applies to entity state that changes over
-- time and needs as-of queries) -- agent_definition already isn't SCD2
-- either (001_initial_schema.up.sql: version is the append-only axis, not
-- valid_from/valid_to), and this column doesn't change that.
--
-- Backfill-then-NOT-NULL in the same migration (rather than a separate
-- follow-up) so there is never a window where agent_definition can hold a
-- NULL domain. Backfills every pre-existing row, not just the one
-- config/agents.yaml currently seeds (agent_id = 'audience-score-system-
-- research') -- agent_definition is a real table, not a config mirror, so
-- a long-lived environment's database can hold rows config never
-- described (a stale manual-test row, an experiment never cleaned up,
-- etc.); filtering the UPDATE by agent_id leaves any such row NULL and
-- fails the ALTER COLUMN below with a dirty migration. audience_score_system
-- is a safe default for every row here because it is the only domain that
-- has ever existed prior to this migration.
ALTER TABLE agent_definition
    ADD COLUMN domain TEXT;

UPDATE agent_definition
    SET domain = 'audience_score_system'
    WHERE domain IS NULL;

ALTER TABLE agent_definition
    ALTER COLUMN domain SET NOT NULL;
