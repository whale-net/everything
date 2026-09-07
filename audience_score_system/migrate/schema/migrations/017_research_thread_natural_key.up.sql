-- Follow-up to migration 016 (root plan #1934, this task #1937): a
-- partial unique index backing ThreadStore.FindOrCreate's natural-key
-- convergence on (channel_id, idea_id, lower(btrim(title))).
--
-- Numbered 017 rather than folded into 016 -- 016 (#1936) had already
-- landed (issue closed) by the time this task started, so per this
-- task's own scope note ("add it as a follow-up migration if #1936's 016
-- has already landed"), the index goes here instead of editing an
-- already-shipped migration file.
--
-- idea_id is nullable (a thread may predate an Idea, same rule as
-- research_note.idea_id) but a UNIQUE index treats NULL as distinct from
-- every other NULL -- two NULL-idea_id threads with the same
-- (channel_id, title) would NOT collide under a bare
-- UNIQUE(channel_id, idea_id, ...) index, defeating convergence for
-- exactly the "note predates an Idea" bucket FindOrCreate most needs to
-- protect (see FindOrCreate's own doc comment on IS NOT DISTINCT FROM for
-- the read-path half of this same hazard). COALESCE(idea_id, the nil
-- UUID) collapses every NULL to one common sentinel value so the index
-- treats "no Idea yet" as one convergent bucket per (channel_id, title),
-- exactly matching the IS NOT DISTINCT FROM semantics the lookup query
-- uses. The nil UUID is used as the sentinel, not some other constant,
-- because it can never collide with a real idea.id (gen_random_uuid()
-- output is never all-zero in practice, and idea.id is a real primary
-- key besides).
--
-- lower(btrim(title)) mirrors FindOrCreate's own lookup predicate
-- exactly, so a racing double-create is caught by this index rather than
-- slipping through on a title comparison the index doesn't actually
-- enforce.

CREATE UNIQUE INDEX research_thread_natural_key
    ON research_thread (
        channel_id,
        COALESCE(idea_id, '00000000-0000-0000-0000-000000000000'::uuid),
        lower(btrim(title))
    );
