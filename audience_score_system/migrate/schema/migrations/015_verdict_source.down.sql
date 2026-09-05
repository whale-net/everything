-- Reverse migration 015: drop viability_verdict.source. Structural
-- reversibility only (FR45's best-effort reversibility policy) -- the
-- per-row authorship values are gone once dropped, same convention as
-- migrations 002/010/011/013's down migrations.

-- Restore v_current_verdict to its pre-015 (migration 002) definition
-- before dropping the column it would otherwise still reference.
-- v_prediction_vs_outcome (migration 012) depends on v_current_verdict,
-- same DROP-order constraint as the up migration -- drop it first, then
-- recreate both, verbatim (its own definition never referenced `source`).
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
    idempotency_key
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

ALTER TABLE viability_verdict DROP COLUMN source;
