-- Three-tier role schema for M2 (issue #1713, part of #1709): introduces
-- the co_creator tier alongside M1's creator/analyst, grant/revoke
-- attribution on channel_person, a Founder-uniqueness backstop, per-tier
-- channel_invite live-invite scoping, and a role-audit view.
--
-- Additive only, per NFR6: no existing channel_person or channel_invite row
-- is UPDATEd or DELETEd here, `creator` keeps its exact M1 meaning
-- (Founder) unchanged, and no row is backfilled to co_creator.

-- -- 1. NFR6 -- widen channel_person.role to accept 'co_creator' -------------
-- Postgres has no ALTER ... ALTER CONSTRAINT for CHECKs, so the existing
-- auto-generated constraint (named channel_person_role_check, confirmed
-- against a freshly migrated DB -- Postgres names an unnamed inline CHECK
-- constraint `<table>_<column>_check`) is dropped and replaced with the
-- widened list. `creator` keeps its M1 meaning (Founder); `co_creator` is
-- new; `analyst` is unchanged.
ALTER TABLE channel_person DROP CONSTRAINT channel_person_role_check;
ALTER TABLE channel_person ADD CONSTRAINT channel_person_role_check
    CHECK (role IN ('creator', 'co_creator', 'analyst'));

-- -- 2. FR34 -- grant/revoke attribution -------------------------------------
-- Both nullable: existing M1 rows recorded no actor and none is invented
-- here. Per AGENTS.md's SCD2 close-and-open convention,
-- granted_by_person_id is written once at the row's INSERT (the person who
-- performed the invite-accept or promotion) and revoked_by_person_id is
-- written once, together with valid_to, at the closing UPDATE (the person
-- who performed the removal).
ALTER TABLE channel_person ADD COLUMN granted_by_person_id UUID REFERENCES person(id);
ALTER TABLE channel_person ADD COLUMN revoked_by_person_id UUID REFERENCES person(id);

-- -- 3. NFR10 -- Founder-uniqueness backstop ---------------------------------
-- Database-level backstop to FR29's "exactly one Founder per Channel"
-- invariant, belt-and-suspenders alongside the fact that no FR path other
-- than Channel-connect (FR25) ever grants the creator role.
CREATE UNIQUE INDEX channel_person_channel_id_founder_current
    ON channel_person(channel_id) WHERE role = 'creator' AND valid_to IS NULL;

-- -- 4. NFR11 -- per-tier channel_invite --------------------------------------
-- `creator` is deliberately not a valid invite role -- no invite path ever
-- grants Founder (FR25/FR29); only 'co_creator' and 'analyst' invites
-- exist. The DEFAULT 'analyst' backfills M1's existing rows as Analyst
-- invites, which is exactly what they are (M1 had no other invite kind).
-- The default is kept (not dropped) because store/invite.go's Generate is
-- not yet tier-aware -- it still inserts a channel_invite row without
-- naming a role at all (#1715 will update it to set role explicitly per
-- tier). Dropping the default here would NOT-NULL-violate every call to
-- today's Generate; keeping it means every untouched invite keeps being an
-- Analyst invite, exactly matching M1's actual (implicit) behavior, until
-- #1715 lands.
ALTER TABLE channel_invite ADD COLUMN role TEXT NOT NULL DEFAULT 'analyst'
    CHECK (role IN ('co_creator', 'analyst'));

-- Rescope the live-invite uniqueness index from one-live-invite-per-Channel
-- to one-live-invite-per-(Channel, tier), so a live Analyst invite and a
-- live Co-Creator invite can coexist on the same Channel (FR30).
DROP INDEX channel_invite_channel_id_live;
CREATE UNIQUE INDEX channel_invite_channel_id_role_live
    ON channel_invite(channel_id, role) WHERE consumed_at IS NULL AND invalidated_at IS NULL;

-- -- 5. FR35 -- role-audit view ------------------------------------------------
-- One row per grant/revoke *event*, per AGENTS.md's SCD2 "Views" convention
-- (the join lives here once, not re-derived per call site): every
-- channel_person row contributes a 'granted' event at valid_from, and,
-- once closed (valid_to IS NOT NULL), a 'revoked' event at valid_to.
-- Ordering (most-recent-first, per FR35) is the caller's -- e.g.
-- `SELECT * FROM v_channel_person_audit WHERE channel_id = $1 ORDER BY
-- occurred_at DESC`.
CREATE VIEW v_channel_person_audit AS
    SELECT
        cp.channel_id                AS channel_id,
        'granted'                    AS event,
        cp.valid_from                AS occurred_at,
        cp.person_id                 AS subject_person_id,
        subject.display_name         AS subject_display_name,
        subject.email                AS subject_email,
        cp.role                      AS role,
        cp.granted_by_person_id      AS actor_person_id,
        granter.display_name         AS actor_display_name
    FROM channel_person cp
    JOIN person subject ON subject.id = cp.person_id
    LEFT JOIN person granter ON granter.id = cp.granted_by_person_id
    UNION ALL
    SELECT
        cp.channel_id                AS channel_id,
        'revoked'                    AS event,
        cp.valid_to                  AS occurred_at,
        cp.person_id                 AS subject_person_id,
        subject.display_name         AS subject_display_name,
        subject.email                AS subject_email,
        cp.role                      AS role,
        cp.revoked_by_person_id      AS actor_person_id,
        revoker.display_name         AS actor_display_name
    FROM channel_person cp
    JOIN person subject ON subject.id = cp.person_id
    LEFT JOIN person revoker ON revoker.id = cp.revoked_by_person_id
    WHERE cp.valid_to IS NOT NULL;
