-- Research note threading & supersession, Stage 1 of 3 (root plan #1934,
-- this task #1936): research_thread (FR1), research_note.thread_id (FR2
-- Stage 1, additive + backfill only), research_note_relation (FR6), and
-- v_current_research_note (FR7).
--
-- This migration is deliberately additive-plus-backfill ONLY.
-- `research_note.idea_id` is untouched -- every currently-deployed reader
-- keeps working unchanged (NFR3). Nothing here retargets a reader onto
-- `thread_id` -- that is Stage 2 (#1939, #1940, #1946). The destructive
-- `SET NOT NULL` / `DROP COLUMN idea_id` step is Stage 3 (#1947),
-- deliberately deferred so a bad Stage 2 rollout can still fall back to
-- `idea_id` without a schema change.
--
-- Numbered 016: verified against the migrations/ directory immediately
-- before writing this file -- 015 (viability_verdict.source) is the
-- current head, so 016 is in fact the next free number.

-- -- research_thread (FR1) -----------------------------------------------
-- A thread groups research_note rows under one title. idea_id is nullable
-- with the exact same rule as research_note.idea_id today (migration
-- 002): a thread may predate an Idea. channel_id/idea_id are indexed to
-- back FR3's discovery reads (browse by Channel, or by Idea once one
-- exists).

CREATE TABLE research_thread (
    id                    UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    channel_id            UUID        NOT NULL REFERENCES channel(id),
    idea_id               UUID        REFERENCES idea(id),
    title                 TEXT        NOT NULL,
    created_by_person_id  UUID        NOT NULL REFERENCES person(id),
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX research_thread_channel_id ON research_thread(channel_id);
CREATE INDEX research_thread_idea_id ON research_thread(idea_id);

-- -- research_note.thread_id (FR2 Stage 1) ---------------------------------
-- Nullable in this migration -- NOT NULL is Stage 3's job, never this one.

ALTER TABLE research_note ADD COLUMN thread_id UUID REFERENCES research_thread(id);
CREATE INDEX research_note_thread_id ON research_note(thread_id);

-- -- Backfill (FR2 Stage 1) -------------------------------------------------
-- One synthetic default thread per distinct (channel_id, idea_id) bucket
-- of pre-existing research_note rows, including the (channel_id, NULL)
-- bucket for notes that predate an Idea. DISTINCT ON, like GROUP BY,
-- treats NULL as equal to NULL for grouping -- this is what gives the
-- (channel_id, NULL) bucket IS NOT DISTINCT FROM semantics on idea_id
-- without an explicit comparison operator. Title is the literal string
-- "Research" for every backfilled thread (no other title data exists to
-- derive from pre-migration).
--
-- created_by_person_id is deterministic, not arbitrary: it is the
-- author_person_id of the EARLIEST note in the bucket (ORDER BY
-- created_at ASC, then id ASC to break exact-timestamp ties
-- deterministically) -- the person who started that line of research is
-- the most defensible choice available in data that predates any notion
-- of "thread owner".
--
-- This INSERT does not touch research_note.text/source_url/
-- author_person_id/created_at at all, and research_note_relation is not
-- touched by this migration -- both per FR2 Stage 1 / root-plan Out of
-- scope.

INSERT INTO research_thread (channel_id, idea_id, title, created_by_person_id)
SELECT DISTINCT ON (rn.channel_id, rn.idea_id)
    rn.channel_id,
    rn.idea_id,
    'Research',
    rn.author_person_id
FROM research_note rn
ORDER BY rn.channel_id, rn.idea_id, rn.created_at ASC, rn.id ASC;

-- Point every pre-existing research_note at its bucket's thread. The join
-- condition uses IS NOT DISTINCT FROM (not `=`) on idea_id specifically
-- because `=` never matches two NULLs -- a bare `=` here would silently
-- leave every idea_id-less note's thread_id NULL instead of backfilled.
UPDATE research_note rn
SET thread_id = rt.id
FROM research_thread rt
WHERE rt.channel_id = rn.channel_id
  AND rt.idea_id IS NOT DISTINCT FROM rn.idea_id;

-- -- research_note_relation (FR6) -------------------------------------------
-- Append-only: no updated_at, no soft-delete column, nothing in this plan
-- ever UPDATEs or DELETEs a row here. The CHECK set is deliberately
-- closed, matching viability_verdict.verdict's (migration 002) and
-- migration 015 source's precedent for an enumerable, known set of
-- values -- NOT outcome_bar.metric_name's (migration 014) deliberately
-- open set, since a relation_type here is a fixed vocabulary the app
-- layer switches on, not a caller-defined label.
--
-- related_note_id is indexed because both FR7's v_current_research_note
-- view and FR11's incoming-direction lookup query that side of the
-- relation, not note_id.

CREATE TABLE research_note_relation (
    note_id          UUID NOT NULL REFERENCES research_note(id),
    related_note_id  UUID NOT NULL REFERENCES research_note(id),
    relation_type    TEXT NOT NULL CHECK (relation_type IN ('supersedes', 'excludes', 'caveats', 'follows_up', 'summarizes')),
    PRIMARY KEY (note_id, related_note_id, relation_type),
    CHECK (note_id <> related_note_id)
);

CREATE INDEX research_note_relation_related_note_id ON research_note_relation(related_note_id);

-- -- Same-thread enforcement (FR6, NFR4) -------------------------------------
-- A DB trigger, not an app-layer check: thread_id is still nullable
-- through Stages 1-2, and a NULL-vs-NULL comparison must never silently
-- pass as "same thread" for two unrelated pre-migration/pre-write notes.
-- IS NOT DISTINCT FROM would do exactly that (treat two NULLs as equal),
-- so this function deliberately uses plain `<>`/IS NULL checks instead:
-- either side missing a thread_id, or the two thread_ids differing,
-- is rejected. Self-reference is already blocked by the table's own
-- CHECK (note_id <> related_note_id) above, so this function does not
-- re-check it.

CREATE FUNCTION research_note_relation_enforce_same_thread() RETURNS TRIGGER AS $$
DECLARE
    note_thread_id UUID;
    related_thread_id UUID;
BEGIN
    SELECT thread_id INTO note_thread_id FROM research_note WHERE id = NEW.note_id;
    SELECT thread_id INTO related_thread_id FROM research_note WHERE id = NEW.related_note_id;

    IF note_thread_id IS NULL OR related_thread_id IS NULL OR note_thread_id <> related_thread_id THEN
        RAISE EXCEPTION 'research_note_relation requires note_id (%) and related_note_id (%) to share a non-null thread_id', NEW.note_id, NEW.related_note_id;
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER research_note_relation_same_thread
    BEFORE INSERT ON research_note_relation
    FOR EACH ROW EXECUTE FUNCTION research_note_relation_enforce_same_thread();

-- -- v_current_research_note (FR7) --------------------------------------------
-- Explicit column list, not `rn.*` -- migration 015's header documents
-- why: a SELECT * view doesn't pick up later column changes anyway, and
-- an explicit list makes Stage 3's eventual DROP COLUMN idea_id a visible,
-- deliberate view rewrite rather than a silent one.
--
-- A note is excluded iff it is the related_note_id target of at least one
-- 'supersedes' or 'excludes' relation; being the target of 'caveats',
-- 'follows_up', or 'summarizes' does not exclude it. No transitive-closure
-- logic is needed -- a chain of 'supersedes' edges each retires its own
-- direct target, which already propagates without extra recursion.

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
