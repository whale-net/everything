-- Reverse migration 018: re-add research_note.idea_id, backfill it from
-- each row's thread (the exact inverse of "the note's effective idea_id
-- is its resolved thread's IdeaID", the up migration's own invariant, and
-- migration 016's backfill invariant before it), restore
-- v_current_research_note's pre-018 (migration 016) column list, and drop
-- thread_id's NOT NULL -- leaving a database an old (pre-#1947) binary
-- could read and write exactly as it did before this migration, unlike
-- the up migration this is not merely structural: the backfill below
-- restores real per-row idea_id values, not just an empty nullable
-- column, because #1947's own down-migration contract (see this task's
-- Scope) requires a pre-Stage-2 read of research_note.idea_id to return
-- the correct Idea for every row after this runs.
--
-- Order is the up migration's, reversed: the view needs idea_id to exist
-- and be backfilled before it can be recreated selecting it, and
-- thread_id's NOT NULL is dropped last since nothing above depends on
-- thread_id being nullable to do its own job.

-- research_thread.idea_id (migration 016) is the join target for the
-- backfill -- since Stage 3 (#1947's own INSERT change) always resolves
-- research_note.thread_id to the SAME thread whose idea_id was the note's
-- effective idea_id, thread_id -> research_thread.idea_id recovers
-- exactly the idea_id value this migration dropped, for every row,
-- without exception.
ALTER TABLE research_note ADD COLUMN idea_id UUID REFERENCES idea(id);

UPDATE research_note rn
SET idea_id = rt.idea_id
FROM research_thread rt
WHERE rt.id = rn.thread_id;

CREATE INDEX research_note_idea_id ON research_note(idea_id);

-- Restore v_current_research_note to its pre-018 (migration 016)
-- definition, verbatim -- no other view depends on it (same check as the
-- up migration), so a single DROP + CREATE suffices.
DROP VIEW v_current_research_note;

CREATE VIEW v_current_research_note AS
SELECT
    rn.id,
    rn.channel_id,
    rn.idea_id,
    rn.thread_id,
    rn.text,
    rn.source_url,
    rn.author_person_id,
    rn.created_at,
    rn.idempotency_key
FROM research_note rn
WHERE NOT EXISTS (
    SELECT 1 FROM research_note_relation r
    WHERE r.related_note_id = rn.id
      AND r.relation_type IN ('supersedes', 'excludes')
);

ALTER TABLE research_note ALTER COLUMN thread_id DROP NOT NULL;
