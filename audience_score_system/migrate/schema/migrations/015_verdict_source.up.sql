-- viability_verdict gains a `source` column (M4.1 FR5 / NFR4, issue #1898):
-- an authorship marker distinguishing a verdict written by an agent via
-- MCP's save_viability_verdict from one written by a human via the web
-- save-verdict form (#1901), on the record itself.
--
-- `DEFAULT 'agent'` **is** NFR4's deterministic backfill: every
-- pre-existing row lands on `source = 'agent'` in the same ALTER TABLE
-- statement that adds the column (Postgres rewrites the existing rows in
-- place for a non-volatile DEFAULT), so no row is ever null or ambiguous
-- after this migration runs. The DEFAULT stays in place afterwards too --
-- not just for backfill -- so a bare INSERT from an unmigrated code path
-- remains valid and honest rather than failing; store.VerdictStore.Append
-- always sets the value explicitly regardless (store/verdict.go), so the
-- DEFAULT is a safety net, not the write path.
--
-- The CHECK set is deliberately closed here, unlike outcome_bar.metric_name
-- in migration 014 (whose open set was the point -- FR1 wanted a new bar
-- metric addable with no migration). `agent`/`human` enumerates the only
-- two surfaces that can author a verdict; that's a closed, known set,
-- matching viability_verdict.verdict's own CHECK precedent in migration
-- 002 rather than outcome_bar's open one.

ALTER TABLE viability_verdict
    ADD COLUMN source TEXT NOT NULL DEFAULT 'agent'
        CHECK (source IN ('agent', 'human'));

-- v_current_verdict (migration 002) has an explicit column list, not
-- SELECT * -- it does not pick up the new column automatically. CREATE OR
-- REPLACE VIEW cannot append/change a view's column list either way that
-- matters here without also touching existing columns' positions in a
-- one-shot rewrite, so DROP + CREATE, matching migration 012's precedent
-- (#1849) for this exact "view needs a column-list change" situation.
--
-- v_prediction_vs_outcome (migration 012) depends on v_current_verdict,
-- so it must be dropped first (DROP VIEW v_current_verdict alone fails
-- with SQLSTATE 2BP01) and recreated afterwards, verbatim -- it does not
-- need `source`, only v_current_verdict does.
DROP VIEW v_prediction_vs_outcome;
DROP VIEW v_current_verdict;

CREATE VIEW v_current_verdict AS
SELECT DISTINCT ON (idea_id)
    id,
    idea_id,
    version,
    verdict,
    reasoning,
    author_person_id,
    created_at,
    idempotency_key,
    source
FROM viability_verdict
ORDER BY idea_id, version DESC;

CREATE VIEW v_prediction_vs_outcome AS
SELECT
    i.id                             AS idea_id,
    i.channel_id                     AS channel_id,
    i.title                          AS idea_title,
    cv.id                            AS verdict_id,
    cv.version                       AS verdict_version,
    cv.verdict                       AS verdict,
    cv.reasoning                     AS verdict_reasoning,
    vs.id                            AS video_script_id,
    vs.title                         AS script_title,
    vs.status                        AS script_status,
    vs.target_publish_date           AS target_publish_date,
    vs.decided_at                    AS decided_at,
    vsm.id                           AS match_id,
    vsm.state                        AS match_state,
    vsm.confidence                   AS match_confidence,
    sv.id                            AS synced_video_id,
    sv.youtube_video_id              AS youtube_video_id,
    sv.title                         AS video_title,
    sv.published_at                  AS published_at,
    vm.views                         AS views,
    vm.average_view_duration_seconds AS average_view_duration_seconds,
    vm.average_view_percentage       AS average_view_percentage,
    vm.impressions                   AS impressions,
    vm.impression_ctr                AS impression_ctr,
    vm.measured_at                   AS metrics_measured_at
FROM idea i
JOIN v_current_verdict cv
    ON cv.idea_id = i.id
JOIN video_script vs
    ON vs.idea_id = i.id AND vs.verdict_id = cv.id AND vs.status IN ('greenlit', 'archived')
JOIN video_schedule_match vsm
    ON vsm.video_script_id = vs.id AND vsm.state IN ('auto', 'confirmed')
JOIN synced_video sv
    ON sv.id = vsm.synced_video_id
JOIN LATERAL (
    SELECT m.views, m.average_view_duration_seconds, m.average_view_percentage,
           m.impressions, m.impression_ctr, m.measured_at
    FROM video_metrics m
    WHERE m.synced_video_id = sv.id
    ORDER BY m.measured_at DESC
    LIMIT 1
) vm ON TRUE;
