-- App Registry — Reconcile watermark (issue #545)
--
-- ReconcileApps performs a FULL replace on every call: every app/chart is
-- upserted ACTIVE and anything absent is flagged MISSING, unconditionally.
-- As of #543 it runs from ci.yml on every push to main, with no ordering
-- guard beyond a same-group CI concurrency cancel (#546, a cheap mitigation
-- that is NOT sufficient on its own -- see that issue's "Caveats"). Without
-- a server-side guard, an OLDER commit's reconcile call landing AFTER a
-- newer one's (a manually re-run workflow, or an in-flight RPC that outran
-- a cancellation) silently reverts registry state -- e.g. re-marking ACTIVE
-- an app a newer commit correctly flagged MISSING -- with no error and no
-- way to detect it after the fact.
--
-- This migration adds a singleton watermark row recording the most
-- recently APPLIED reconcile call's ordering metadata. Reconcile() compares
-- every incoming call against it (inside the same transaction as the
-- upserts, via SELECT ... FOR UPDATE, so concurrent callers serialize
-- rather than both reading a stale watermark) and no-ops a call whose
-- ordering key is strictly older -- see ARCHITECTURE.md "Reconcile
-- watermark" for the full comparison/tie-break rules, which live in Go
-- (server/repository.ShouldApplyReconcile), not SQL.

-- Why source_committed_at, not discovered_at: AppManifestSet.discovered_at
-- (appmeta.proto) is the Unix time release_helper_go ran its `bazel query`
-- sweep, NOT the swept commit's position in history -- a manually re-run
-- older workflow sweeps at re-run time, so discovered_at is NOT monotonic
-- in commit order and is unsafe to use alone as an ordering key (the exact
-- "re-run an old workflow" case this table exists to guard against would
-- sail straight past a discovered_at-only watermark). source_committed_at
-- (appmeta.proto field 5, added alongside this migration) is the git
-- committer timestamp of git_sha instead, which IS monotonic-with-history
-- for any commit reachable from main. discovered_at is kept as a
-- comparison fallback for backward compatibility: an AppManifestSet
-- produced by a release_helper_go binary that predates this field (e.g.
-- still cached on a CI runner mid-rollout) carries source_committed_at = 0,
-- and the fallback lets that caller keep working, just without the
-- stronger ordering guarantee, rather than being rejected outright.

-- Why a singleton table, not a per-app/per-row watermark: ReconcileApps is
-- always a full-replace of the complete manifest set (ARCHITECTURE.md
-- "Design principles" #1 and this table's own migration 001 doc comments),
-- so there is exactly one meaningful "most recent complete sweep" to compare
-- against -- one global watermark suffices, and a per-row watermark would
-- have to answer an unasked question ("was THIS app's row from a newer
-- sweep than THAT app's row"), which cannot happen under a full-replace
-- write.
--
-- Why CHECK (id = 1) rather than a UNIQUE index on a constant expression:
-- both structurally forbid a second row, but a literal PRIMARY KEY column
-- gives every query (the FOR UPDATE read, the upsert) a normal, readable
-- `WHERE id = 1` instead of an expression index most engineers have never
-- seen. Rejected: no PK at all plus an application-level "SELECT ... LIMIT
-- 1 FOR UPDATE" -- that still races on the very first INSERT (see below)
-- and gives Postgres nothing to enforce if application code has a bug.
CREATE TABLE reconcile_watermark (
    id                    SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),

    -- The commit this watermark reflects. Compared directly against an
    -- incoming call's git_sha as the FIRST tie-break rule (see
    -- ShouldApplyReconcile): the identical commit reconciled twice --
    -- e.g. a manual re-run of the SAME workflow run, or the CLI pointed at
    -- the same manifest set twice with different idempotency keys -- always
    -- applies regardless of how its timestamps compare, since re-applying
    -- identical data is harmless and clock skew between two sweeps of the
    -- same commit must never produce a false-stale rejection.
    git_sha               TEXT NOT NULL,

    -- The ordering key. 0 alongside an empty git_sha (see below) means "no
    -- real watermark recorded yet."
    source_committed_at   BIGINT NOT NULL,

    -- Comparison fallback when a caller's source_committed_at is 0 -- see
    -- the rationale above. Stored (not just source_committed_at) so a
    -- future caller with source_committed_at = 0 can still be compared
    -- against whatever ordering key the CURRENT watermark was itself
    -- recorded with, including if that too falls back to discovered_at.
    discovered_at         BIGINT NOT NULL,

    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Seed exactly one sentinel row rather than leaving the table empty.
-- Postgres's SELECT ... FOR UPDATE locks only rows it MATCHES -- against a
-- genuinely empty table it matches (and locks) nothing, so two concurrent
-- "first ever reconcile" transactions would both read "no watermark," both
-- proceed to apply, and only serialize (accidentally, via the unique
-- constraint) at the final INSERT -- by which point both had already
-- written their app/chart upserts unserialized against each other. Seeding
-- one row up front means the FOR UPDATE read always has something to lock,
-- so every reconcile call -- including the very first one ever -- is
-- properly serialized against concurrent callers for the table's entire
-- lifetime.
--
-- This sentinel (empty git_sha, zero timestamps) is what "no watermark yet"
-- means operationally: ShouldApplyReconcile (server/repository) treats a
-- watermark row with git_sha = '' exactly like a wholly absent row --
-- always apply. The distinction between "table has zero rows" and "table
-- has one sentinel row" is a storage-layer implementation detail entirely
-- invisible above the SQL in this file; every caller of the repository
-- interface only ever sees "is there a watermark or not."
INSERT INTO reconcile_watermark (id, git_sha, source_committed_at, discovered_at)
VALUES (1, '', 0, 0);
