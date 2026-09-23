-- Down-migrating drops both `display_number` columns -- `position` and
-- every other column on `feature`/`load_bearing_decision` are untouched,
-- so `krill/render` reverting to its old numberByOrder render-time
-- computation (LB2's original stance) is the only behavior this restores.
ALTER TABLE load_bearing_decision DROP COLUMN display_number;
ALTER TABLE feature DROP COLUMN display_number;
