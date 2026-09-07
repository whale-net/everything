-- FR2 Stage 3 (destructive), root plan #1934, this task #1947: the ONE
-- non-additive step in the whole research-note-threading plan.
-- research_note.thread_id (migration 016, Stage 1) gets its NOT NULL
-- (NFR4), and research_note.idea_id -- the column thread_id was
-- introduced to replace -- is dropped outright.
--
-- Numbered 018, not 017: this task's own scope note says to verify 017 is
-- the next free number at implementation time and use the next free one
-- if it's already taken -- it was (#1937's research_thread_natural_key
-- migration claimed 017 first), so this lands as 018.
--
-- Why this is safe now and was NOT safe before: every reader of
-- research_note.idea_id was retargeted onto the thread-derived value
-- (rt.idea_id via a JOIN to research_thread) before this migration was
-- written, across three merged PRs, each landing only after the one
-- before it:
--   1. #1939 (store/research.go)   -- researchNoteColumns/
--      researchNoteWithAuthorColumns/ListFiltered's ideaID filter all
--      switched from reading research_note.idea_id directly to a LEFT
--      JOIN research_thread rt ON rt.id = rn.thread_id, selecting
--      rt.idea_id instead. store/mywork.go's loadLatestNotes query made
--      the identical switch in the same task.
--   2. #1940 (mcp/tools) -- list_research_notes/save_research_note/
--      get_channel_overview/get_my_work's shared toResearchNoteOutput
--      (mcp/tools/research.go) consumes store.ResearchNote.IdeaID, which
--      was already thread-derived after #1939 -- #1940's own job was
--      exposing thread_id/thread_title alongside it, not touching the
--      idea_id derivation again.
--   3. #1946 (web/research) -- HandleSaveVerdict's cited-note same-Idea
--      check and renderChannelIndex's unattached-notes partition were
--      already reading the thread-derived store.ResearchNote.IdeaID from
--      #1939 too; #1946 restated their doc comments in thread terms and
--      added its own regression coverage (a tampered raw idea_id column
--      proven NOT to leak through the thread-derived read).
-- Stage 2 is confirmed merged and deployed on every surface (store, mcp,
-- web) -- this task's own pre-flight audit (posted as a comment on issue
-- #1947) re-ran each of the three greps above against the CURRENT trunk
-- and found zero remaining research_note.idea_id readers outside this
-- migration and researchStore.SaveNote's dual-write, which this same task
-- also removes in the same commit as this migration (never split across
-- two deploys -- an old binary between the two would still try to write
-- idea_id after it's gone).
--
-- Order matters (013's stated rule, restated here): SET NOT NULL first --
-- migration 016's backfill already guarantees every row has a thread_id,
-- so this cannot fail against real data, and doing it before the DROP
-- COLUMN means a failure here (were the backfill ever incomplete) aborts
-- before anything destructive happens. The view rewrite happens between
-- the two ALTERs so the DROP COLUMN below never runs while a view still
-- has an explicit reference to the column being dropped. No CASCADE
-- anywhere in this file -- if anything else still depends on idea_id or
-- thread_id's nullability, this migration must fail loudly (missing an
-- upstream retarget) rather than silently drop past it.

-- research_thread's own doc-comment invariant (migration 016) already
-- guarantees no row has a NULL thread_id post-backfill, so this cannot
-- fail against real data -- but SET NOT NULL, not a CHECK, is NFR4 itself:
-- an INSERT/UPDATE that tries to leave thread_id NULL is now rejected by
-- Postgres at the column level, with its own clear "null value in column
-- "thread_id" violates not-null constraint" error, rather than this
-- migration adding a second, redundant enforcement mechanism on top.
ALTER TABLE research_note ALTER COLUMN thread_id SET NOT NULL;

-- v_current_research_note (migration 016) has an explicit column list
-- including idea_id, so it must be dropped and recreated without that
-- column -- the same "view needs a column-list change" situation
-- migration 015 handled by DROP + CREATE (citing migration 012's
-- precedent), restated here since research_note has no other dependent
-- view to worry about (checked: no other CREATE VIEW in this schema
-- selects from research_note or v_current_research_note).
DROP VIEW v_current_research_note;

CREATE VIEW v_current_research_note AS
SELECT
    rn.id,
    rn.channel_id,
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

-- Drop the column's own index before the column itself -- Postgres drops
-- a column's index automatically on DROP COLUMN, but doing it explicitly
-- here (IF EXISTS, matching migration 013's DROP INDEX IF EXISTS
-- precedent for the analogous schedule_entry_id retirement) keeps this
-- migration's intent readable as three ordered steps rather than relying
-- on an implicit side effect.
DROP INDEX IF EXISTS research_note_idea_id;
ALTER TABLE research_note DROP COLUMN idea_id;
