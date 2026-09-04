-- Reverse migration 009 in exact reverse order of its up-file's five
-- sections. No data-preservation attempted (matches this repo's other down
-- migrations' convention, e.g. 001_identity.down.sql).
--
-- Narrowing channel_person_role_check back to ('creator', 'analyst') will
-- fail loudly (a CHECK violation on the existing rows) if any row has
-- role = 'co_creator' at the time this runs. That failure is correct and
-- intentional -- down-migrating past a state that has real co_creator data
-- would silently discard which rows were which tier, so this migration
-- does not work around it by deleting or reassigning those rows.

-- -- 5. FR35 -- drop the role-audit view --------------------------------------
DROP VIEW v_channel_person_audit;

-- -- 4. NFR11 -- revert channel_invite to one live invite per Channel --------
DROP INDEX channel_invite_channel_id_role_live;
CREATE UNIQUE INDEX channel_invite_channel_id_live
    ON channel_invite(channel_id) WHERE consumed_at IS NULL AND invalidated_at IS NULL;

ALTER TABLE channel_invite DROP COLUMN role;

-- -- 3. NFR10 -- drop the Founder-uniqueness backstop -------------------------
DROP INDEX channel_person_channel_id_founder_current;

-- -- 2. FR34 -- drop grant/revoke attribution columns -------------------------
ALTER TABLE channel_person DROP COLUMN revoked_by_person_id;
ALTER TABLE channel_person DROP COLUMN granted_by_person_id;

-- -- 1. NFR6 -- narrow channel_person.role back to ('creator', 'analyst') ----
ALTER TABLE channel_person DROP CONSTRAINT channel_person_role_check;
ALTER TABLE channel_person ADD CONSTRAINT channel_person_role_check
    CHECK (role IN ('creator', 'analyst'));
