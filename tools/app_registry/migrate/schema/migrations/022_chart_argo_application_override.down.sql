-- Restore v_current_chart to its migration-008 definition before dropping
-- the column it would otherwise still reference.
CREATE OR REPLACE VIEW v_current_chart AS
SELECT
    c.chart_id,
    c.domain,
    c.name,
    ''::text     AS description,
    ''::text     AS chart_repository,
    'chart'::text AS deploy_unit,
    c.status,
    c.first_seen_at,
    c.last_seen_at
FROM chart c;

ALTER TABLE chart DROP COLUMN argo_application_name_template;
