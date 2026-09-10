-- grpcauth_delegated_grant and grpcauth_grant_index -- the schema
-- libs/go/grpcauth/pgstore and libs/go/grpcauth/grantindex need, owned and
-- versioned by whagent-net per those packages' own "Schema contract" doc
-- comments (neither package ships a migration of its own -- FR13, issue
-- #2426, plan #2421). This is purely additive: no request path reads or
-- writes either table yet (main.go's DelegatedGrantSource/Store/Index
-- construction is wiring only, not yet on the hot path) -- see #2426's
-- Scope for the tasks that swap onto them.
--
-- Column shapes below are copied verbatim from
-- libs/go/grpcauth/pgstore/pgstore.go's and
-- libs/go/grpcauth/grantindex/grantindex.go's package doc comments
-- ("# Schema contract") -- the shape, not just the table name, is the
-- contract those packages check.
--
-- Neither table is SCD2 (AGENTS.md "SCD2"): grpcauth_delegated_grant's
-- history is its status transitions (grpcauth.GrantStatus), not versioned
-- rows, and grpcauth_grant_index is a pure existence index with no status
-- column at all for valid_from/valid_to to apply to (see grantindex.go's
-- package doc, "Hard semantics").

CREATE TABLE grpcauth_delegated_grant (
    subject        TEXT        NOT NULL,
    grant_key      TEXT        NOT NULL,
    token_material BYTEA       NOT NULL,
    status         TEXT        NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (subject, grant_key)
);

CREATE TABLE grpcauth_grant_index (
    subject_iss        TEXT        NOT NULL,
    subject_sub        TEXT        NOT NULL,
    domain             TEXT        NOT NULL,
    preferred_username TEXT        NOT NULL,
    granted_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (subject_iss, subject_sub, domain)
);
