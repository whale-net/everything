-- Migration 017: household never-zero-members database-level guard (Phase 2)
--
-- 017 is the next free number after 016 (audit_log); state in the PR since
-- sibling v2 branches on plan/1166 have collided on migration numbers
-- before.
--
-- FR75's "a household never reaches zero members" backstop at the database
-- layer, alongside leaflab/api/households.go's Repository.RemoveMember
-- application-level check (which already SELECT ... FOR UPDATEs the
-- household row and counts current members inside the same transaction as
-- the close -- see its doc comment). This trigger is the second, independent
-- layer the issue calls for: it fires on every UPDATE that closes a
-- household_membership row (valid_to transitioning NULL -> non-NULL),
-- regardless of which code path performed it, and refuses if doing so would
-- leave zero current members for that household.
--
-- The trigger locks the owning household row (SELECT ... FOR UPDATE) itself
-- before counting, so two concurrent closes racing on the same household
-- serialize through this trigger even if RemoveMember's own lock were ever
-- bypassed or buggy -- this guarantee does not depend on the application
-- taking any lock of its own.

CREATE FUNCTION enforce_household_never_zero_members() RETURNS TRIGGER AS $$
DECLARE
    remaining INT;
BEGIN
    -- Lock the household row so a concurrent close on the same household
    -- blocks here until this transaction commits or rolls back --
    -- otherwise two closes, each closing a different member, could each
    -- observe the other's row as still open under READ COMMITTED and both
    -- proceed, leaving zero members.
    PERFORM 1 FROM household WHERE household_id = OLD.household_id FOR UPDATE;

    SELECT COUNT(*) INTO remaining
    FROM household_membership
    WHERE household_id = OLD.household_id
      AND valid_to IS NULL
      AND household_membership_id <> OLD.household_membership_id;

    IF remaining = 0 THEN
        RAISE EXCEPTION 'household % would have zero members; refused (FR75)', OLD.household_id
            USING ERRCODE = 'LL001';
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_household_membership_never_zero
    BEFORE UPDATE OF valid_to ON household_membership
    FOR EACH ROW
    WHEN (OLD.valid_to IS NULL AND NEW.valid_to IS NOT NULL)
    EXECUTE FUNCTION enforce_household_never_zero_members();
