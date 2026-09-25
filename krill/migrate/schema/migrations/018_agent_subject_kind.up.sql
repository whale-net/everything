-- 018_agent_subject_kind: widen `session`'s two `kind` CHECK constraints
-- to admit a third value, 'agent' (issue #2962).
--
-- Migration 003 created both columns as
-- `CHECK (acting_kind IN ('human', 'service'))` and anticipated exactly
-- this change: "`kind` mirrors whagent_net's CHECK verbatim ('human',
-- 'service') -- widen this CHECK (not the column shape) if a third kind is
-- ever needed." This is that widening, and nothing else: the columns stay
-- TEXT, stay NOT NULL, and keep the same three-column (iss, sub, kind)
-- shape.
--
-- Why a third kind is needed now. LB4 requires every mutating call to
-- record an acting subject and an on-behalf-of subject *distinctly*, and
-- 003's own comment names the case this serves: "an agent acting on
-- behalf of a human." Until now that case had no honest encoding -- the
-- nearest fit was to record the agent as a 'service', which asserts
-- something different (a machine account acting for the system) and so
-- makes the two-subject distinction LB4 is built on unreadable at the gate.
--
-- This is LB4's shape becoming usable, not C24 arriving. C24 ("attribute
-- an action to a specific agent identity") stays Later and undecided: it
-- governs how an agent identity is minted and verified (krill's own
-- capability vs. whagent_net's JWKS). This migration only makes a caller
-- able to *record* that the subject was an agent; it decides nothing about
-- how that fact is established.
--
-- Widening a CHECK is strictly permissive: every value already accepted
-- is still accepted, so this cannot invalidate existing rows. The Go-side
-- enum (store.SubjectKind) and the HTTP-side validator
-- (api/handlers.ParseSubject) are widened in the same change, so the
-- database, the Go constant, and the request gate agree.
ALTER TABLE krill_session DROP CONSTRAINT krill_session_acting_kind_check;
ALTER TABLE krill_session ADD CONSTRAINT krill_session_acting_kind_check
    CHECK (acting_kind IN ('human', 'service', 'agent'));

ALTER TABLE krill_session DROP CONSTRAINT krill_session_on_behalf_of_kind_check;
ALTER TABLE krill_session ADD CONSTRAINT krill_session_on_behalf_of_kind_check
    CHECK (on_behalf_of_kind IN ('human', 'service', 'agent'));
