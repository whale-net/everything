-- 031_deferral_capability_id: a deferral may cite a capability (Feature)
-- by its immutable id. Nullable and unconstrained (Feature is SCD2, so
-- its id is not a table-wide key); the Cn label is resolved at read time
-- and never stored.
ALTER TABLE milestone_deferral
    ADD COLUMN capability_id UUID NULL;
