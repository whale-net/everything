-- Reverse migration 016: drop v_current_research_note,
-- research_note_relation (and its trigger/function), research_note.thread_id
-- (and its index), and research_thread. Structural reversibility only
-- (FR45's best-effort reversibility policy, same convention as migrations
-- 002/010/011/013's down migrations) -- the backfilled thread_id
-- assignments and any relation rows recorded since the up migration are
-- not recoverable once dropped, and idea_id is untouched throughout so no
-- data is actually lost by this reversal.
--
-- FK-safe order, no CASCADE anywhere -- a missed dependency must fail
-- loudly rather than silently drop past it, matching migration 013's
-- precedent:
--   1. the view (depends on research_note_relation and research_note)
--   2. research_note_relation (drops its own trigger with it, but not the
--      trigger function)
--   3. the trigger function itself
--   4. research_note.thread_id and its index
--   5. research_thread (its own indexes go with it)

DROP VIEW v_current_research_note;

DROP TABLE research_note_relation;

DROP FUNCTION research_note_relation_enforce_same_thread();

DROP INDEX research_note_thread_id;
ALTER TABLE research_note DROP COLUMN thread_id;

DROP TABLE research_thread;
