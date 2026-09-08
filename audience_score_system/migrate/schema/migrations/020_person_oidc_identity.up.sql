-- person_oidc_identity: the (iss, sub) -> person mapping the whagent-net
-- authentication path resolves every verified whagent Claim against
-- (issue #2116, FR12(b)). Sits ALONGSIDE person.google_subject /
-- PersonStore.UpsertByGoogleSubject -- neither is touched by this
-- migration -- because the two identity keys answer different questions:
-- google_subject is ASS's own web-session sign-in identity (a Google `sub`
-- alone, no issuer column, because `web`'s OAuth only ever integrates one
-- issuer today), while person_oidc_identity is deliberately keyed on the
-- PAIR (iss, sub), never `sub` alone and never re-keyed off
-- google_subject, so a person can be linked to identities from more than
-- one issuer over time. The root plan's M1 design-time note is explicit
-- that keying on the pair is what makes a future issuer (e.g. Google
-- itself, for M3) one more row per person rather than a schema re-key.
--
-- Auto-provisioning (FR12(b)): the whagent Claim carries no email or
-- display name (FR10), so the Person an unseen (iss, sub) pair
-- provisions is identity-key-only -- person.email/display_name stay NULL,
-- exactly like any other Person row (both columns are already nullable,
-- migration 001). google_subject, however, was NOT NULL UNIQUE as of
-- migration 001 -- it was ASS's ONLY identity key until now, so nothing
-- ever had to construct a Person without one. An auto-provisioned
-- whagent-net Person has no Google identity at all (it may never sign
-- into `web`), so PersonIdentityStore.FindOrCreateByIssSub needs to be
-- able to insert a person row with a NULL google_subject. UNIQUE still
-- enforces "at most one Person per real google_subject" afterward --
-- Postgres does not treat NULL as equal to NULL for a UNIQUE constraint,
-- so any number of google_subject-less rows may coexist.
ALTER TABLE person ALTER COLUMN google_subject DROP NOT NULL;

CREATE TABLE person_oidc_identity (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    person_id  UUID        NOT NULL REFERENCES person(id),
    iss        TEXT        NOT NULL,
    sub        TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- The find-or-create key: PersonIdentityStore.FindOrCreateByIssSub's
-- ON CONFLICT (iss, sub) DO UPDATE ... RETURNING (xmax = 0) idiom (mirrors
-- person.google_subject's UNIQUE + UpsertByGoogleSubject idiom, migration
-- 001/store/person.go) needs this exact composite UNIQUE constraint --
-- keyed on the PAIR, never on `sub` alone, so the same external subject
-- string under two different issuers resolves to two distinct persons.
CREATE UNIQUE INDEX person_oidc_identity_iss_sub ON person_oidc_identity(iss, sub);

-- Supports "list this Person's linked (iss, sub) identities" without a
-- table scan -- no such affordance exists yet in M1, but every other
-- identity-adjacent table in this domain (e.g. mcp_credential's
-- person_id index, migration 006) carries this lookup direction too.
CREATE INDEX person_oidc_identity_person_id ON person_oidc_identity(person_id);
