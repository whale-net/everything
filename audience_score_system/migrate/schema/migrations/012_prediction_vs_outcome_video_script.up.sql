-- Re-anchor v_prediction_vs_outcome's outcome half from schedule_entry to
-- video_script (FR44, C9 re-anchor -- C9's capability text is unchanged).
-- Migration 010 already added video_schedule_match.video_script_id and
-- backfilled it for pre-existing confirmed/auto matches; this migration
-- only re-points the view. schedule_entry itself, and
-- video_schedule_match.schedule_entry_id, stay in place until the
-- retirement task (#1835) drops them.
--
-- Numbered 012, not 011: the plan originally assigned 011 to this task's
-- migration, but #1833's migration claimed 011 first, so this is the next
-- free number, preserving relative order after 011.
--
-- greenlit AND archived scripts are joined deliberately (not greenlit
-- alone) -- FR40's archive/match interaction note establishes that a video
-- can legitimately have published under a script the Channel later pulled
-- back, and dropping those rows would silently lose exactly the outcome
-- comparison C9 exists to provide. proposed and denied scripts are
-- excluded -- neither can carry a live match.

CREATE OR REPLACE VIEW v_prediction_vs_outcome AS
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
