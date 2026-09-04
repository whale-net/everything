-- Reverse migration 012: restore the migration-002 definition of
-- v_prediction_vs_outcome verbatim (schedule_entry-anchored).

CREATE OR REPLACE VIEW v_prediction_vs_outcome AS
SELECT
    i.id                             AS idea_id,
    i.channel_id                     AS channel_id,
    i.title                          AS idea_title,
    cv.id                            AS verdict_id,
    cv.version                       AS verdict_version,
    cv.verdict                       AS verdict,
    cv.reasoning                     AS verdict_reasoning,
    se.id                            AS schedule_entry_id,
    se.proposed_publish_at           AS proposed_publish_at,
    se.approved_at                   AS approved_at,
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
JOIN schedule_entry se
    ON se.idea_id = i.id AND se.verdict_id = cv.id AND se.state = 'committed'
JOIN video_schedule_match vsm
    ON vsm.schedule_entry_id = se.id AND vsm.state IN ('auto', 'confirmed')
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
