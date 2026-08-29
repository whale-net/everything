# Version model

`AllocateVersion` is live and wired into every real release call site, for
every domain, unconditionally — there is no per-domain gate, and no domain
concept it could be gated on. `resolveVersion`
(`tools/release_helper_go/cmd/registry_version.go`) is the shared decision
every real version-resolution call site goes through: `plan.go`'s
`assignVersions`/`assignChartVersions`, `release_charts.go`'s
`releaseCharts` (the real production chart call site — `release.yml`'s
`release-helm-charts` job invokes it directly, not `plan`'s `chart-matrix`
output), and `build_helm.go`'s `build-helm-chart`. Once the registry
integration is opted in (`APP_REGISTRY_CICD_OPT_IN=true`), `resolveVersion`
always calls `AllocateVersion`; any error from it is fatal to the release.
The only fallback to git-tag scanning left is the opt-in itself being off
(no registry client dialed at all) — that is a global switch, not a
per-domain one.

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

**`AllocateVersion` writes directly to `artifact`, in state `allocated`.**
`AllocateVersionRequest` carries no digest or build id (allocation happens
*before* a build exists — see `protos/api_messages_artifact.proto`), which
is why `artifact.digest`/`build_id` are nullable (migration 007) rather than
requiring a separate reservation table. "Next version" is one query —
`SELECT ... FROM artifact WHERE owner_id = $1 AND kind = $2 ORDER BY
version_major DESC, version_minor DESC, version_patch DESC LIMIT 1` — that
covers both already-published versions and reserved-but-not-yet-published
ones in the same table, so a version reserved a moment ago by a concurrent
caller is never handed out twice. `UNIQUE (owner_id, kind, version)` is what
makes concurrent `AllocateVersion` calls for the same owner structurally
unable to collide — the constraint does the work, not application-level
locking. A unique-violation aborts the whole transaction (see "Idempotency"
and the transaction-abort hazard noted throughout this doc); the caller
(`handlers.ArtifactServer.AllocateVersion`) retries in a **fresh**
transaction, recomputing "next" against the now-committed state. (An
earlier design used a separate `version_allocation` reservation table; AR-7b
folded it into `artifact` and dropped it — see "Artifact lifecycle" in
"Release lifecycle (issue #558)".)

**Major bumps are first-class.** `incrementVersion` (backed by
`libs/go/semver`) handles `major`/`minor`/`patch` uniformly, and `plan.go`
exposes `--increment-major` alongside `--increment-minor`/`--increment-patch`
— matching chart bumping (`build_helm.go`), which accepts all three.
`AllocateVersionRequest.increment` accepts the same three values. This is a
strict superset of what git-tag scanning could ever produce, since the tag
path could not express a major bump at all.

**Prereleases and build metadata are rejected, not mis-sorted.**
`AllocateVersion` calls `semver.ParseRelease`, which rejects a
`-prerelease`/`+build` suffix outright — real semver orders a prerelease
*before* its release, which nothing here implements, so half-accepting one
would sort it wrongly. **Extension point:** a later `version_prerelease TEXT`
column can be added to `artifact` without changing the `(owner_id, kind,
version)` constraint or the integer triple; the ordering index would gain a
trailing term. Do not add it speculatively — it lands with the change that
actually needs prerelease ordering.

**A `failed` version is reused, not skipped past.** `(owner_id, kind,
version)` is unique across every artifact state (migration 007), so a
`failed` row (from `FailPublish` or the stale-row reaper) would otherwise
block that exact version forever while every retry silently allocated the
next one — permanently burning a version per failed attempt with no way to
actually retry the one that failed. `AllocateVersion` instead checks the
state of the highest existing version for the owner+kind: if it is
`failed`, that same version is handed back (no new row inserted) instead of
incrementing past it, so a release can be retried an arbitrary number of
times against the SAME version. The state-shape CHECK constraint still
guarantees at most one of those attempts ever carries a real digest (only
`published` may have one), so reuse is safe. This check is deliberately
scoped to `failed` only — not `allocated`/`publishing` — because those may
still be legitimately in flight from a concurrent caller; see
`TestAllocateVersion_ConcurrentCallsNeverCollide`, which depends on N
concurrent callers for the same owner each getting a distinct version. An
abandoned `allocated`/`publishing` row still becomes reusable once the
stale-row reaper (`ExpireStale`) sweeps it to `failed`, just bounded by the
reaper's timeout instead of being immediate. `explicitVersion` bypasses this
entirely — an explicit request always gets exactly what it asked for.

**As built.** `AllocateVersion` (`server/handlers/artifact.go`) is fully
implemented and tested (unit tests against the fake, real-Postgres
integration tests covering concurrent allocation, numeric ordering, and
idempotency-key replay — see `server/repository/postgres/postgres_integration_artifact_test.go`).
Full delivery history — including the two phases it originally shipped in
(AR-5a implemented it; AR-5b wired it into every call site) and the
now-removed per-domain adoption gate an earlier design used — is in
PLAN-HISTORY.md's "AR-5" section.
