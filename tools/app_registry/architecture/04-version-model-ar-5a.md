# Version model (AR-5a)

Full rationale lives in PLAN.md's AR-5 "Addendum — semver semantics
(decided)" — this section is the as-built summary of the version-allocation
mechanics. **Current state: `AllocateVersion` is live and wired into every
real release call site, for every domain, unconditionally** — see "Per-domain
cutover gate — removed" below and PLAN.md's "AR-5 — cutover status" for the
full history of how it got there. AR-5a itself shipped this schema and RPC
fully working but wired to nothing (no call site invoked it yet, and it was
additionally gated per domain); AR-5b (issue #829) wired it into every real
version-resolution call site; the AR-5 cutover then removed the per-domain
gate entirely.

**Parsing is shared, not duplicated.** `libs/go/semver` is the one semver
parser/incrementer/comparator this repo's release tooling uses —
`release_helper_go`'s `incrementVersion` and this package's `AllocateVersion`
both call it, rather than each keeping its own regex. It lives in `libs/go`
(not under either caller) for the same reason `//tools/appmeta/proto` lives
outside the registry: neither side should depend on the other to get parsing
right.

**Numeric ordering, not lexical.** `artifact.version` is `TEXT`; lexical
`ORDER BY` on it is wrong — `"v1.9.0" > "v1.10.0"` as strings. Migration 004
adds `version_major`/`version_minor`/`version_patch` (`INT NOT NULL`,
populated from the same parse at record/allocate time) plus an index
ordering by that triple. `UNIQUE (owner_id, kind, version)` stays on the TEXT
column as the collision guard; the integer columns are for ordering only. A
version that fails to parse (a real, if rare, historical condition — see
migration 004's comments) backfills to the `0/0/0` sentinel, which sorts
before every real release rather than blocking the migration or winning a
"latest" query it shouldn't.

**`AllocateVersion` writes to `version_allocation`, not `artifact`.**
`AllocateVersionRequest` carries no digest or build id (allocation happens
*before* a build exists — see `protos/api_messages_artifact.proto`), and
`artifact` requires both `NOT NULL`. `version_allocation` is a lightweight
table with the same `(owner_id, kind, version)` unique-index shape, so a
transactional `INSERT` against it is what makes concurrent `AllocateVersion`
calls for the same owner structurally unable to collide — the unique
constraint does the work, not application-level locking. "Next version" is
computed as the max across **both** `artifact` (already-published) and
`version_allocation` (reserved but not yet recorded), so a version reserved
a moment ago by a concurrent caller is never handed out twice even though
`RecordArtifact` hasn't run for it yet. A unique-violation aborts the whole
transaction (see "Idempotency" and the transaction-abort hazard noted
throughout this doc); the caller (`handlers.ArtifactServer.AllocateVersion`)
retries in a **fresh** transaction, recomputing "next" against the
now-committed state.

**Prereleases and build metadata are rejected, not mis-sorted.**
`AllocateVersion` calls `semver.ParseRelease`, which rejects a
`-prerelease`/`+build` suffix outright — real semver orders a prerelease
*before* its release, which nothing here implements, so half-accepting one
would sort it wrongly. **Extension point:** a later `version_prerelease TEXT`
column can be added to `artifact` without changing the `(owner_id, kind,
version)` constraint or the integer triple; the ordering index would gain a
trailing term. Do not add it speculatively — it lands with the change that
actually needs prerelease ordering.

**Per-domain cutover gate — removed by the AR-5 cutover.** As originally
built (AR-5a), `AllocateVersion` rejected any domain not at
`domain_adoption.stage = 'allocate'` (see "Resolved questions" #3) with
`FailedPrecondition`. `domain_adoption` shipped from AR-1's migration with a
row-per-domain-on-cutover shape (no row = implicit `observe`); AR-5a added no
mechanism to move a domain to `allocate` — that was deliberately still a
direct `UPDATE domain_adoption` (or a future admin RPC/CLI command), so the
first real cutover would be a reviewable, explicit action. Two domains
(`app-registry`, `tools`) were in fact cut over this way, by hand, ahead of
AR-5b wiring any real caller to `AllocateVersion` — see PLAN.md's "AR-5 —
cutover status" for that history (issue #829).

**The AR-5 cutover removed the gate entirely** (migration
`022_drop_domain_adoption`): `domain_adoption` is dropped, and
`AllocateVersion` now serves every domain unconditionally — there is no more
per-domain stage to be at, no more `FailedPrecondition` for adoption reasons,
and consequently no more need for an admin RPC/CLI to move a domain between
stages, since there are no stages left to move between. The only remaining
gate on whether `AllocateVersion` gets called at all is the global
`APP_REGISTRY_CICD_OPT_IN` opt-in (registry-integration-wide, not
per-domain) — see PLAN.md's "AR-5 — cutover status" and
`08-release-lifecycle/09-relationship-to-ar5.md` for the full as-built
account.

