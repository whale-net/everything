-- 001_scope: the `scope` table every other krill table hangs off (LB1,
-- issue #2487). `scope`, not any entity row, owns this repo's forge
-- coordinates -- see krill/PRODUCT.md's LB1 for why a surrogate scope_id
-- indirection exists instead of a bare repo_full_name column on every
-- table: a repo rename becomes a row edit here instead of a rewrite of
-- every entity row, and a future decision spanning more than one product
-- (C23, Later) has somewhere to live.
--
-- Boundary call (LB3): `scope` is a plain mutable config row -- NOT SCD2
-- (AGENTS.md "SCD2": valid_from/valid_to) and NOT the work axis's
-- append-only + claimed shape (tools/app_registry's writeback_outbox
-- precedent). M1 seeds exactly one row and never amends it in place
-- through any exposed surface; a future in-place edit (e.g. a repo
-- rename) is an ordinary UPDATE, not a new revision -- there is no
-- requirement to retain what a scope's coordinates used to be.
--
-- pointer_issue_number is nullable: NFR2/FR20's thin GitHub pointer issue
-- (the artifact cross-linking a krill Product to a PR/commit/conversation)
-- does not exist until a later M1 task creates it, so this milestone's
-- seed (krill/migrate/seed) leaves it unset.
CREATE TABLE scope (
    id                    UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    repo_full_name        TEXT        NOT NULL UNIQUE,
    default_branch        TEXT        NOT NULL,
    pointer_issue_number  INT         NULL,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
