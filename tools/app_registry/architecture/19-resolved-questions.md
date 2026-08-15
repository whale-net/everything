# Resolved questions

**1. Chart identity source — already solved, reuse it.**
`ListAllHelmCharts` in `tools/release_helper_go/cmd/plan_helm.go` mirrors
`ListAllApps` exactly: a loading-phase `bazel query` lists the metadata target
labels, then a `bazel cquery --output=starlark` scoped to those labels reads
the `HelmChartMetadataInfo` provider. Chart identity comes from that existing
path. No new discovery mechanism.

Note this changed in #444: discovery reads Starlark **providers**
(`AppMetadataInfo` / `HelmChartMetadataInfo`) rather than building each
target's `*_metadata.json`, so no actions run. The provider carries the same
dict the JSON file does — the cquery expression is literally
`json.encode(...metadata)` — so `//tools/appmeta/proto` remains the correct
schema for both delivery mechanisms, and the contract test gets cheaper
(analysis-only).

**2. Writeback is interface-only for now.**
Wiring the gitops repo requires changes in another repository that are out of
scope. AR-4 therefore delivers the *contract* — outbox, workflow, and a
`Writeback` activity interface — with a stub implementation that renders state
and writes it locally without publishing anywhere. The real gitops and S3
activities plug in behind that interface later with no schema or API change.

Consequence: nothing before AR-4 needs Temporal, so the `libs/go/temporal`
work moves out of AR-1 and into AR-4. That removes the largest unknown from
the foundations phase.

Consequence: until the real writeback lands, `GetEnvironmentState` is the only
way to consume promotion state. That is acceptable because no deploy tooling
depends on it yet — but it means the "registry can be down without blocking a
deploy" property is untested until AR-4 completes for real.

**3. No backfill; adopt by per-domain cutover.**
Historical artifacts are not backfilled from git tags or GHCR — history
accumulates from AR-2 forward.

Adoption is **per domain**, not global. A domain can publish through the
registry while every other domain stays on the existing tag-based path, which
allows a fast, low-blast-radius rollout instead of one repo-wide switch.

This needs a `domain_adoption` table keyed by domain, recording which
capabilities the registry is authoritative for:

| Stage | Meaning |
|---|---|
| `observe` | Registry records builds and artifacts. Git tags remain authoritative. Default for every domain from AR-2. |
| `promote` | Promotion state for this domain is tracked and consumed. |
| `allocate` | Registry allocates versions for this domain; tag scanning is bypassed. |

Recording (AR-2) is deliberately **not** gated — recording every domain is
harmless and builds the parity evidence AR-5 depends on. The gate matters from
AR-3 onward, and most of all at AR-5, where the source of truth actually
changes hands.

`AllocateVersion` must reject a domain not yet at `allocate`, so a
misconfigured CI job fails loudly rather than silently allocating from the
wrong source of truth.

**4. `chart`/`chart_app` are not SCD2, and don't need to be — issue #544.**

*Is `chart` SCD2?* No, and it shouldn't be. `chart` is a **mutable,
reconciled dimension row** — the same shape as `app`: identity
(`domain`/`name`) is stable, and `Reconcile` overwrites descriptive fields
and `status` in place (`appRepo.Reconcile`, the `UPDATE chart SET
status='active', last_seen_at=$2 ...` path). Per AGENTS.md's SCD2 section,
SCD2 exists to answer "what was the value at time T," backed by a
`valid_from`/`valid_to` pair and a partial-unique "current" index. Nothing in
this system ever asks "what did `chart.description` say on Tuesday" — the
one question anyone actually asks about a chart's history ("what was
*running* at time T") is already answered by `promotion` (genuinely SCD2) and
`build`/`artifact` (append-only, timestamped), not by historizing the chart
row itself. Applying SCD2 here would just be tracking the history of a
reconciled cache with no reader.

*Is `chart_app` SCD2, or should it be?* This is the one that "kind of seems
like it could be" — it changes over time and, naively, "what apps did this
chart compose when it was promoted" sounds like a temporal question. It
isn't SCD2 today: `appRepo.setChartApps` (`server/repository/postgres/app.go`)
does a full `DELETE FROM chart_app WHERE chart_id = $1` then re-`INSERT` on
every `Reconcile` — a current-state-only join, not append-only and not
soft-deleted, which AGENTS.md's SCD2 section would normally flag as a
candidate. But it should **not** become SCD2 either, because nothing on the
deploy-time render path reads `chart_app` at all (traced in full below) — the
thing that actually needs to be temporally stable already is, via a
different, simpler mechanism than SCD2.

*Is the deploy-time idempotency worry real?* **No — traced and verified with
a real-Postgres regression test
(`TestChartArtifact_CompositionPinnedAtRecordTime_SurvivesLaterReconcile`,
`server/repository/postgres/postgres_integration_artifact_test.go`).** The chart
→ image lockfile mechanism built for AR-2c (see "Chart → image lockfile"
above) already solves this, incidentally, for the exact case the issue
raises:

- A chart artifact's app list is `Artifact.Contains` (`[]ArtifactLink`),
  populated by `artifactRepo.loadContains`
  (`server/repository/postgres/artifact.go:233`), which reads **only**
  `artifact_link WHERE chart_artifact_id = $1` — keyed to one specific,
  already-published chart artifact, never to the live `chart` row.
- `artifact_link` rows are written exactly once, inside `RecordArtifact`
  (`artifact.go:169-201`), from the CI-supplied `contains` list — itself a
  hermetic function of the manifest set at chart-build time
  (`tools/helm/composer.go`, no registry or DB access). No code path ever
  updates or deletes an `artifact_link` row after that insert.
- `PromotionServer.GetEnvironmentState` (`server/handlers/promotion.go:350-359`)
  — the exact RPC the writeback worker calls to render what a promoted chart
  deploys — builds its `Images`/drift computation from
  `artifact.Contains`, i.e. `artifact_link`. It never joins `chart_app`.
  `chart_app` only surfaces through `AppIds` on `GetChart`/`ListCharts`
  responses (`server/handlers/convert.go:118`), a purely informational
  "what does this chart declare today" read, never consulted by
  `Promote`/`Rollback`/`GetEnvironmentState`/the writeback workflow.

So: promote chart `v1.2.3` (digest D, pinning images {A, B}), then run a
`Reconcile` that changes the chart's *declared* composition to {A, C} — the
already-promoted artifact D still renders {A, B}, because rendering never
looks at the row `Reconcile` just rewrote. Confirmed against real Postgres,
not just the fakes: see "Verification" in the PR that introduced this
section.

**Where this leaves `chart_app`:** it stays as-is, current-state-only, for
the informational "what does chart X compose right now" question
(`app-registry chart show`-style reads). It is not wired into anything
render-critical, so there is nothing to harden. Nothing precludes adding it
later if a *human-facing* "what did chart X compose at time T" question ever
needs answering (an SCD2 history table would be the right tool then, per
AGENTS.md) — but that is a different, lower-stakes question than deploy-time
correctness, and is not needed today.

