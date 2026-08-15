# Reconcile watermark (issue #545)

`Reconcile` performs a FULL replace on every call (see "Design principles"
#1 and `ReconcileAppsRequest`'s own doc comment): every app/chart is
upserted `ACTIVE`, anything absent is flagged `MISSING`, unconditionally. As
of #543 it runs from `ci.yml` on every push to `main`, with only a same-group
CI concurrency cancel (#546) as a mitigation — which does not cover a
manually re-run older workflow (fresh queue timing, not the original push's
chronological position) or an in-flight RPC that outran a cancellation. An
older call landing after a newer one would silently revert registry state
(e.g. re-marking `ACTIVE` an app a newer commit correctly flagged `MISSING`)
with no error and no way to detect it after the fact.

**The guard: a singleton watermark, checked and advanced inside the same
transaction as the write.** Migration 006 adds `reconcile_watermark`, one
row recording the most recently *applied* call's ordering metadata.
`Reconcile` reads it with `SELECT ... FOR UPDATE` (so two concurrent calls
serialize instead of both reading the same stale watermark), decides
apply-or-skip, and — only on apply — writes the app/chart diff and advances
the watermark, all in one transaction (the non-dry-run path already runs
inside `runIdempotent`'s `WithTx`). The comparison/tie-break logic itself
lives once, in Go (`repository.ShouldApplyReconcile`), shared by the
postgres and fake implementations exactly like `DerivePromotability` is —
not duplicated as SQL in one place and Go in the other.

**Ordering key: `source_committed_at`, not `discovered_at`.**
`AppManifestSet.discovered_at` (`appmeta.proto`) is the Unix time
`release_helper_go` ran its `bazel query` sweep — sweep time, not the swept
commit's position in history. A manually re-run older workflow sweeps at
re-run time, so `discovered_at` is **not monotonic in commit order** and is
unsafe to use alone: it is exactly the "re-run an old workflow" case this
watermark exists to guard against, and a naive `discovered_at`-only
watermark would sail straight past it. `source_committed_at` (added
alongside this migration) is the git committer timestamp of `git_sha`
instead — `release_helper_go`'s `manifest-set` command resolves it via `git
log -1 --format=%ct <git_sha>` — which *is* monotonic with history for any
commit reachable from `main`. `discovered_at` is kept as a comparison
fallback for exactly one reason: backward compatibility with an
`AppManifestSet` produced by a `release_helper_go` binary that predates this
field (e.g. still cached on a CI runner mid-rollout of this change), which
carries `source_committed_at = 0`. `git log` resolution failing (git
unavailable, sha unresolvable in a shallow clone) also leaves it `0` rather
than failing the `manifest-set` command — losing the stronger ordering
guarantee for one call beats breaking manifest discovery entirely.

**Tie-breaking rules** (`ShouldApplyReconcile`, evaluated in this order):

1. No watermark yet (table's sentinel row, `git_sha = ''`): apply. The very
   first reconcile is always accepted.
2. `incoming.git_sha == current.git_sha`: apply, unconditionally, regardless
   of how the timestamps compare. The identical commit reconciled twice
   (a manual re-run of the same workflow run, or the CLI pointed at the
   same manifest set again with a fresh idempotency key) is harmless to
   re-apply — idempotency already covers true RPC-level replays; this
   covers the same commit arriving via two *different* calls — and clock
   skew between two sweeps of the same commit must never produce a
   false-stale rejection.
3. Incoming's ordering key (`source_committed_at`, falling back to
   `discovered_at`) strictly older than current's: **skip** (stale).
4. Otherwise (incoming's key is ≥ current's, and `git_sha` differs):
   **apply**. The equal-timestamp case is deliberate: two different commits
   landing in the same wall-clock second (or two manifest sets both falling
   back to `discovered_at`) must not block a legitimate merge just because
   they tied.

**A skipped-stale call is a no-op SUCCESS, not an error.** A CI re-run of an
older commit is doing the *right* thing by declining to revert newer state;
turning that workflow red would be exactly backwards. But a silent no-op is
the failure mode this feature exists to eliminate, so
`ReconcileAppsResponse` carries `skipped_stale` and
`current_watermark_git_sha`, the handler logs at `Warn` when it happens (see
`server/handlers/app.go`'s `ReconcileApps`), and the CLI prints a stderr
banner (mirroring `promote.go`'s already-promoted/dry-run banners) so it is
visible in a CI log even if nobody inspects the JSON response.

**Idempotency-key interaction.** A skipped-stale response IS stored under
the request's `idempotency_key`, same as any other successful response —
deliberately, not an oversight: a retry with the *same* key must replay the
*same* answer. If the skip weren't stored and the watermark happened to
change before a retry (e.g. a legitimately newer commit's reconcile lands
between the two calls), a replay could flip from "skipped" to "applied" for
what the caller believes is the same request — silently reordering writes
relative to what actually happened on the wire. Storing it keeps
`runIdempotent`'s guarantee ("repeated calls with the same key are a no-op
returning the original result") true here too.

**Dry run never touches the watermark**, in either direction: it neither
reads it to decide skip-or-apply, nor advances it on completion. Dry run
already writes nothing (see `ReconcileAppsRequest.dry_run`'s doc comment);
consulting a mutation guard for a call that cannot mutate would just be
extra Postgres round trips answering a question nobody asked.

**Why a singleton table, not a per-app/per-row watermark.** `Reconcile` is
always a full-replace of the complete manifest set, so there is exactly one
meaningful "most recent complete sweep" to compare against — one global
watermark suffices. A per-row watermark would have to answer an unasked
question ("was THIS app's row from a newer sweep than THAT app's row"),
which cannot happen under a full-replace write model.

**Why the watermark table is seeded with a sentinel row, not left empty.**
Postgres's `SELECT ... FOR UPDATE` locks only rows it *matches* — against a
genuinely empty table it locks nothing, so two concurrent "first ever
reconcile" transactions would both read "no watermark," both proceed to
apply, and only accidentally serialize at the final write. Migration 006
seeds exactly one row (`git_sha = ''`, timestamps `0`) so the locking read
always has something to hold from the very first call onward. This sentinel
is what "no watermark yet" means operationally — see migration 006's
comments and `ShouldApplyReconcile`'s doc comment; it is invisible above the
repository interface, which only ever exposes "is there a watermark or
not."

