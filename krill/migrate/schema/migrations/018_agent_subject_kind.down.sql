-- Down-migrating restores migration 003's two-value CHECK. Rows already
-- written with kind='agent' violate it, so this fails loudly rather than
-- silently dropping or rewriting them -- that is the correct behaviour for
-- a reversible-schema boundary, and the operator's job is to decide what to
-- do with those rows first.
ALTER TABLE krill_session DROP CONSTRAINT krill_session_on_behalf_of_kind_check;
ALTER TABLE krill_session ADD CONSTRAINT krill_session_on_behalf_of_kind_check
    CHECK (on_behalf_of_kind IN ('human', 'service'));

ALTER TABLE krill_session DROP CONSTRAINT krill_session_acting_kind_check;
ALTER TABLE krill_session ADD CONSTRAINT krill_session_acting_kind_check
    CHECK (acting_kind IN ('human', 'service'));
