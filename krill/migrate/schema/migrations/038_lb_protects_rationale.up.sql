-- 038_lb_protects_rationale: optional free-text "why" on a protects edge, so
-- a roadmap's Later coverage can say why a decision protects a capability.
ALTER TABLE lb_protects_feature ADD COLUMN rationale TEXT;

-- Appending the column keeps CREATE OR REPLACE VIEW valid.
CREATE OR REPLACE VIEW lb_protects_feature_active AS
    SELECT id, scope_id, decision_id, feature_id, created_at, rationale
    FROM lb_protects_feature
    WHERE withdrawn_at IS NULL;
