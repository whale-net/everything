-- Down migration 014: Remove household table and Unadopted seed

DROP FUNCTION IF EXISTS get_unadopted_household_id();
DROP TABLE IF EXISTS household;
