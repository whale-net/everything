-- Reverse migration 020: drop the never-zero-members trigger and function.

DROP TRIGGER IF EXISTS trg_household_membership_never_zero ON household_membership;
DROP FUNCTION IF EXISTS enforce_household_never_zero_members();
