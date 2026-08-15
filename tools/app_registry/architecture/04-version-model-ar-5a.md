# Version model (AR-5a)

Full rationale lives in PLAN.md's AR-5 "Addendum — semver semantics
(decided)" — this section is the as-built summary. **AR-5a ships this
schema and RPC fully working but wired to nothing**: no domain is at
adoption stage `allocate`, and `tools/release_helper_go/cmd/plan.go`'s
git-tag path (`autoIncrementVersion`) is untouched. See "AR-5" in PLAN.md
for what remains before any domain can be cut over for real.

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

**Per-domain cutover gate.** `AllocateVersion` rejects any domain not at
`domain_adoption.stage = 'allocate'` (see "Resolved questions" #3) with
`FailedPrecondition`. `domain_adoption` ships from AR-1's migration with a
row-per-domain-on-cutover shape (no row = implicit `observe`); AR-5a adds no
mechanism to move a domain to `allocate` — that is deliberately still a
direct `UPDATE domain_adoption` (or a future admin RPC/CLI command, not yet
built), so the first real cutover is a reviewable, explicit action.

