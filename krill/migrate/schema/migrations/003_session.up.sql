-- 003_session (issue #2489, FR3): the `init` session primitive and the
-- write gate every mutating call in this milestone passes through. `id` is
-- a krill-native, krill-minted session identifier -- distinct from
-- `whagent_session_id` below, never derived from it.
--
-- LB4 -- two subjects, both always populated. `acting_*` is who is making
-- this call; `on_behalf_of_*` is who the call is attributed to. Both are
-- `(iss, sub, kind)` triples with `iss` a real column (mirrors
-- `whagent_net`'s LB2 verbatim and `libs/go/whagent`'s `Claim` `sub`/
-- `sub_iss`/`act` shape) so a non-Keycloak issuer is a row, not a
-- migration. When a caller acts for itself, on_behalf_of_* is written
-- identical to acting_*; when they differ (e.g. an agent acting on behalf
-- of a human) both triples are populated distinctly. `kind` mirrors
-- whagent_net's CHECK verbatim ('human', 'service') -- widen this CHECK
-- (not the column shape) if a third kind is ever needed.
--
-- `whagent_session_id` is correlation only: populated from the inbound
-- `Claim.WhagentSessionID` (libs/go/whagent) when a call arrives through
-- the whagent-net verifier. It is nullable -- a human/OAuth2 caller has no
-- WhagentSessionID and still gets a krill session -- and it never
-- substitutes for `id`: every init mints its own krill-native session id
-- regardless of whether a whagent claim is present.
--
-- Boundary call (LB3): `krill_session` is append-only, NOT SCD2
-- (AGENTS.md "SCD2") -- M1 ships exactly one verb, `init`, with no update
-- path over a session row. There is therefore no valid_from/valid_to
-- pair and no updated_at; a row is written once and never changed.
CREATE TABLE krill_session (
    id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    scope_id            UUID        NOT NULL REFERENCES scope (id),
    acting_iss          TEXT        NOT NULL,
    acting_sub          TEXT        NOT NULL,
    acting_kind         TEXT        NOT NULL CHECK (acting_kind IN ('human', 'service')),
    on_behalf_of_iss    TEXT        NOT NULL,
    on_behalf_of_sub    TEXT        NOT NULL,
    on_behalf_of_kind   TEXT        NOT NULL CHECK (on_behalf_of_kind IN ('human', 'service')),
    whagent_session_id  TEXT        NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX ON krill_session (scope_id);
