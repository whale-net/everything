-- Drop Strategy's cadence field (FR47, issue #1833). M2.1 removes
-- pacing/calendar mechanics outright -- this is not a placeholder for a
-- successor field. strategy.preferred_weekday is untouched: no FR removes
-- it. Dropping the column drops the
-- CHECK (cadence IN ('weekly', 'biweekly', 'monthly')) constraint with it.

ALTER TABLE strategy DROP COLUMN cadence;
