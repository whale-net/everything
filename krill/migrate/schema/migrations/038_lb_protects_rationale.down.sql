DROP VIEW IF EXISTS lb_protects_feature_active;
ALTER TABLE lb_protects_feature DROP COLUMN IF EXISTS rationale;
CREATE VIEW lb_protects_feature_active AS
    SELECT id, scope_id, decision_id, feature_id, created_at
    FROM lb_protects_feature
    WHERE withdrawn_at IS NULL;
