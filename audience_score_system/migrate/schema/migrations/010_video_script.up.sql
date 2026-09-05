-- video_script: M2.1's replacement for schedule_entry as the record of a
-- proposed video (product brief #1562, milestone video-script-model,
-- issues #1823/#1824). Carries a title and script_text -- schedule_entry
-- never did -- and its own propose/greenlit/denied/archived lifecycle
-- (FR36-FR40) in place of schedule_entry's draft/committed pair.
--
-- verdict_id/idea_id mirror schedule_entry's LB3 identity link exactly:
-- verdict_id pins the specific viability_verdict *version* that judged
-- the idea viable (never "current"), idea_id is carried alongside it. Both
-- are NOT NULL, same as schedule_entry.
--
-- strategy_id is NOT NULL, unlike verdict_id/idea_id's schedule_entry
-- precedent -- FR36 and #1823's "Assumptions filled in during drafting"
-- read C18's "propose a video script *under* a Strategy" at face value: a
-- video_script cannot exist without a grounding Strategy.
--
-- decided_by_person_id/decided_at is a single mutable who+when pair
-- covering greenlight, deny, AND archive -- mirrors schedule_entry's
-- approved_by_person_id/approved_at (Approve/Unapprove/Update) exactly.
-- This is deliberately NOT a history table: AGENTS.md's SCD2 convention
-- only applies if a later task needs to reconstruct who-decided-what-when
-- across multiple transitions, and none does here.
--
-- No unique index enforcing "at most one greenlit video_script per
-- Channel" -- explicitly out of scope for this milestone (#1823).
CREATE TABLE video_script (
    id                      UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    channel_id              UUID        NOT NULL REFERENCES channel(id),
    idea_id                 UUID        NOT NULL REFERENCES idea(id),
    verdict_id              UUID        NOT NULL REFERENCES viability_verdict(id),
    strategy_id             UUID        NOT NULL REFERENCES strategy(id),
    title                   TEXT        NOT NULL,
    script_text             TEXT        NOT NULL,
    status                  TEXT        NOT NULL CHECK (status IN ('proposed', 'greenlit', 'denied', 'archived')),
    target_publish_date     TIMESTAMPTZ,
    decided_by_person_id    UUID        REFERENCES person(id),
    decided_at              TIMESTAMPTZ,
    created_by_person_id    UUID        NOT NULL REFERENCES person(id),
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    idempotency_key         TEXT
);

CREATE INDEX video_script_channel_id ON video_script(channel_id);
CREATE INDEX video_script_idea_id ON video_script(idea_id);
CREATE INDEX video_script_verdict_id ON video_script(verdict_id);
CREATE INDEX video_script_strategy_id ON video_script(strategy_id);

-- -- video_schedule_match.video_script_id re-anchor (FR45) -------------------
-- The eventual replacement outcome link. schedule_entry_id stays in place
-- for now -- it is dropped later, by the retirement task, once every
-- reader is retargeted onto video_script_id.

ALTER TABLE video_schedule_match ADD COLUMN video_script_id UUID REFERENCES video_script(id);
CREATE INDEX video_schedule_match_video_script_id ON video_schedule_match(video_script_id);

-- -- best-effort backfill (FR45) ----------------------------------------------
-- Not lossless, not zero-downtime (#1823's Out of scope). A
-- schedule_entry row with no derivable strategy_id is DROPPED, not
-- carried forward -- no placeholder or default Strategy is synthesized.
-- Any video_schedule_match row pointing at a dropped schedule_entry keeps
-- video_script_id NULL, including an already-confirmed one: that is the
-- accepted, explicitly-decided data loss, not a bug. FR39's publish
-- freeze governs post-migration transitions only and does not apply to
-- this migration's own writes.
--
-- strategy_id is derived by joining strategy_verdict on the schedule_entry
-- row's verdict_id to an active strategy on the same channel_id; when more
-- than one qualifies, the deterministic tie-break is lowest
-- strategy.created_at, then id.
--
-- migrated_from_schedule_entry_id is a temporary column, dropped at the
-- end of this migration, that exists only so the video_schedule_match
-- re-point step below can join a newly-inserted video_script row back to
-- the schedule_entry it came from without relying on any combination of
-- copied columns being coincidentally unique.

ALTER TABLE video_script ADD COLUMN migrated_from_schedule_entry_id UUID;

WITH derived_strategy AS (
    SELECT DISTINCT ON (se.id)
        se.id AS schedule_entry_id,
        st.id AS strategy_id
    FROM schedule_entry se
    JOIN strategy_verdict sv ON sv.verdict_id = se.verdict_id
    JOIN strategy st ON st.id = sv.strategy_id
        AND st.channel_id = se.channel_id
        AND st.active = TRUE
    ORDER BY se.id, st.created_at, st.id
)
INSERT INTO video_script (
    channel_id, idea_id, verdict_id, strategy_id, title, script_text, status,
    target_publish_date, decided_by_person_id, decided_at, created_by_person_id,
    created_at, updated_at, migrated_from_schedule_entry_id
)
SELECT
    se.channel_id,
    se.idea_id,
    se.verdict_id,
    ds.strategy_id,
    i.title,
    '',
    CASE se.state WHEN 'draft' THEN 'proposed' WHEN 'committed' THEN 'greenlit' END,
    se.proposed_publish_at,
    se.approved_by_person_id,
    se.approved_at,
    se.created_by_person_id,
    se.created_at,
    se.updated_at,
    se.id
FROM schedule_entry se
JOIN idea i ON i.id = se.idea_id
JOIN derived_strategy ds ON ds.schedule_entry_id = se.id;

-- Re-point already-confirmed/auto video_schedule_match rows at the
-- video_script row backfilled from their schedule_entry. A match whose
-- schedule_entry had no derivable strategy_id (so no video_script row was
-- inserted for it) keeps video_script_id NULL.
UPDATE video_schedule_match vsm
SET video_script_id = vs.id
FROM video_script vs
WHERE vs.migrated_from_schedule_entry_id = vsm.schedule_entry_id
  AND vsm.state IN ('confirmed', 'auto');

ALTER TABLE video_script DROP COLUMN migrated_from_schedule_entry_id;
