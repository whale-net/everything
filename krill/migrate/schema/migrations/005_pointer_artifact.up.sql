-- 005_pointer_artifact: the thin GitHub pointer issue (FR20, C9, this
-- task -- issue #2496). A krill Product's spec lives in krill's own
-- entity model, not a file in this repository, so there is no longer a
-- markdown file for a PR/commit/conversation to reference the way this
-- very repo's `project-manager` pipeline references a root `Plan:` issue
-- (e.g. "Part of #2485"). This table records the one thin GitHub issue
-- krill creates per Product to stand in for that file, so the exact same
-- cross-linking convention keeps working unchanged (C9: "as they do
-- today").
--
-- ============================================================================
-- Boundary call (LB3): plain, not SCD2
-- ============================================================================
-- A pointer_artifact row is a fact ("this Product has a pointer issue,
-- created by this session's two subjects") with no history worth
-- versioning -- there is no "amend a pointer artifact" operation, mirroring
-- `milestone_ref`/`entity_milestone` (004_milestone_assoc.up.sql)'s own
-- LB3 call for the same reason. No `valid_from`/`valid_to` pair.
--
-- ============================================================================
-- What "kind" discriminates, and what this table deliberately does NOT store
-- ============================================================================
-- `kind` discriminates the artifact's OWN shape (today: exactly one value,
-- "github_issue"), the same one-column-not-two-tables discriminator
-- precedent as `requirement.kind`/`non_goal.kind` -- not what later
-- references the issue. This table does not store a ledger of which PRs,
-- commits, or conversations have since referenced the created issue: C20
-- ("krill does not own branch or PR lifecycle... stores references only")
-- means krill's job stops at minting the issue and recording that it
-- exists -- after that, ordinary GitHub mechanics (a PR body, a commit
-- message, a discussion linking "#<n>") do the cross-linking with no
-- further krill involvement, exactly as they do for this repo's own
-- `Plan:`/task issues today. There is therefore no branch name, PR number,
-- commit SHA, or conversation URL column here (C20's one recorded
-- condition: no branch-name-derived state anywhere in this feature).
--
-- ============================================================================
-- product_id -- LB2 parentage, not a DB-enforced REFERENCES
-- ============================================================================
-- product_id holds the parent's immutable `id` (migration 002's LB2
-- parentage note) -- not a REFERENCES, for the same reason no other
-- spec-entity parent column is one: `product.id` is not unique table-wide.
-- `krill/store`'s `currentRowExists` enforces it at write time, same as
-- every other child table.
--
-- ============================================================================
-- LB1 -- scope_id, and where the forge coordinates actually live
-- ============================================================================
-- scope_id is a real, DB-enforced FK (scope.id IS unique table-wide,
-- `scope` is not SCD2) -- LB1's own precedent. The created issue's number
-- is also mirrored onto `scope.pointer_issue_number` (001_scope.up.sql) in
-- the same store-layer transaction that inserts this row: `scope`, not
-- this table, is the forge-coordinate system of record (LB1), and this
-- table is the audit trail of *when* and *by whom* that coordinate was
-- populated. The UNIQUE index on product_id below keeps the two in lock
-- step for M1's one-Product-per-scope shape: at most one pointer issue is
-- ever minted per Product, so `scope.pointer_issue_number` is written
-- exactly once by this feature.
--
-- ============================================================================
-- created_by_* -- LB4, both subjects always recorded
-- ============================================================================
-- Mirrors `krill_session`'s acting/on_behalf_of triples verbatim
-- (003_session.up.sql) -- FR20's issue-create is a write path (FR3's write
-- gate covers it), and unlike every other M1 create endpoint, this one is
-- worth auditing per-row: a pointer issue is a durable, human-visible GitHub
-- artifact, not just an internal spec entity.
CREATE TABLE pointer_artifact (
    id                           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    scope_id                     UUID        NOT NULL REFERENCES scope(id),
    product_id                   UUID        NOT NULL, -- parent's immutable `id`; see this migration's LB2 parentage note above
    kind                         TEXT        NOT NULL DEFAULT 'github_issue' CHECK (kind = 'github_issue'),
    issue_number                 INT         NOT NULL,
    issue_url                    TEXT        NOT NULL,
    created_by_acting_iss        TEXT        NOT NULL,
    created_by_acting_sub        TEXT        NOT NULL,
    created_by_acting_kind       TEXT        NOT NULL,
    created_by_on_behalf_of_iss  TEXT        NOT NULL,
    created_by_on_behalf_of_sub  TEXT        NOT NULL,
    created_by_on_behalf_of_kind TEXT        NOT NULL,
    created_at                   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- At most one pointer artifact per Product -- see this migration's LB1
-- note above for why: re-creating one would either orphan
-- `scope.pointer_issue_number`'s meaning or silently mint a second GitHub
-- issue for the same Product, neither of which "as they do today" (C9)
-- calls for.
CREATE UNIQUE INDEX pointer_artifact_product_idx ON pointer_artifact(product_id);

CREATE INDEX pointer_artifact_scope_idx ON pointer_artifact(scope_id);
