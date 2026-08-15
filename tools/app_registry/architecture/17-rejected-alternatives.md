# Rejected alternatives

| Decision | Chosen | Rejected | Why |
|---|---|---|---|
| Async durability | Temporal + outbox | River | River was a trial and is being retired repo-wide. |
| Async durability | Temporal + outbox | RabbitMQ | Cannot enlist in the promotion transaction; would need an outbox anyway, so the outbox is the real mechanism and Temporal is the better executor for a multi-step, retryable git push. |
| Transport | gRPC + thin Go CLI | grpc-gateway / connect-go | Every CI caller in this repo is already `bazel run` on a Go CLI. A gateway is pure complexity until a browser UI exists. |
| Environments | table rows | proto enum | Ephemeral and regional environments become an insert, not a release. |
| Bundling | chart pins image digests | registry-side "release bundle" | Charts already are the bundle. Inventing a parallel grouping would duplicate what `tools/helm` composes. |
| Missing apps | flag `MISSING` for triage | auto-archive | A rename would silently orphan promotion history. |
| Version source of truth | registry (AR-5), tags retained | tags only | A unique constraint beats `git tag --sort` plus a CI concurrency group for concurrent allocation. |
| Manifest schema | shared `//tools/appmeta/proto` | registry-local manifest messages | Two hand-written Go structs already decode the manifest JSON and had drifted; a third would compound it. |
| Manifest schema | proto + `protojson` | shared hand-written Go struct | `protojson` reads the existing snake_case JSON unchanged, and `DiscardUnknown: false` turns drift into a test failure. |
| App/chart registration | `ReconcileApps`, full manifest set, run on push to `main` | a scoped `RegisterApp`/upsert RPC tied to `release.yml`, sending only the apps in that run | Two write paths to the same rows with different invariants. `ReconcileApps` treats its input as the complete truth and flags anything absent as `MISSING` (`api.proto`'s own comment) — a scoped call would either need to skip that check (weakening triage) or would falsely flag every app not in the current release as `MISSING`. It's also unsafe to run at release time at all: `release.yml` can be dispatched against an arbitrary (possibly old) ref, and reconciling an old commit's manifest set would flag every app added since as `MISSING`. Running it on every push to `main` instead means it only ever sees the current, complete tree. See issue #539's follow-up (#542) and DEPLOY.md "CI wiring (AR-3d)". |
| Reconcile watermark ordering key | `source_committed_at` (git committer timestamp of `git_sha`), fallback to `discovered_at` | `discovered_at` alone | `discovered_at` is sweep time, not commit-history position — a manually re-run older workflow sweeps at re-run time, producing a *newer* `discovered_at` than the newer commit it's racing. That is precisely the headline case issue #545 exists to guard against, so a `discovered_at`-only watermark would not catch it. See "Reconcile watermark" above. |
| Stale reconcile call | no-op success (`skipped_stale = true`) | `FailedPrecondition` error | A CI re-run of an older commit is doing the *correct* thing by declining to overwrite newer state; failing that workflow run would punish correct behavior and train people to retry (or worse, force-apply) their way past a safety check. A visible response field plus a server-side `Warn` log gives the same "you should know this happened" property as an error without making the workflow red. |
| Reconcile watermark granularity | one singleton row for the whole registry | a watermark per app/chart row | `Reconcile` is always a full-replace of the complete manifest set (see "Design principles" #1) — there is exactly one meaningful "most recent complete sweep," so a per-row watermark would have to answer a question that cannot arise under this write model ("was this one row from a newer sweep than that one"). |
| Release-vs-reconcile gap (#547) | accept the gap; make the failure a distinct, actionable annotation instead of silent | a release-time provisional upsert, always superseded by the next real reconcile | Reintroduces a second write path to `app`/`chart`, the exact thing the "App/chart registration" row above already rejected. Unsafe against a `release.yml` ref that diverges from `main` (see "Release-vs-reconcile gap" above): it could write stale metadata that nothing corrects until the next `main` reconcile happens to run. |
| Release-vs-reconcile gap (#547) | accept the gap; make the failure a distinct, actionable annotation instead of silent | gate `release.yml` on `main`'s reconcile completing first | Avoids a second write path, but every release would block on `main` CI. The repo owner releases immediately after merging as normal practice; this would turn an occasional recording miss into a mandatory wait on every release. |

**Revised by AR-7 (issue #558).** The three rows above about registration and
the #547 gap rejected a release-time write path *because `app` carries mutable
metadata that a divergent ref could clobber*. AR-7 removes that premise by
moving the metadata to per-build manifest snapshots, at which point
`AssertApps` writes identity and facts only — the objection is answered, not
overruled. The full-replace/`MISSING` reasoning is untouched: absence still
belongs to the `main` sweep alone. Issue #558's own rejected alternatives are
tabled in "Release lifecycle (issue #558)" above.

