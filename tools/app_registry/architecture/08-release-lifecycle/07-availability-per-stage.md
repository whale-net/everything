# Availability, restated per adoption stage

**Superseded by the AR-5 cutover.** This section originally argued that
"the registry can be down for hours without blocking a release" and "the
registry is the source of truth for the pipeline" cannot both hold once
`publishing` must be written before a push — and resolved the tension with a
lever, `domain_adoption.stage`, that graduated a domain from best-effort
recording, to mandatory recording, to release-critical version allocation.
That lever is gone: `domain_adoption` is dropped, and every domain now sits
at what this table used to call `allocate` — recording, chart hermeticity,
and version allocation are all unconditional and release-critical for every
domain, all the time. There is no more per-domain axis to restate
availability against; there is exactly one axis left,
`APP_REGISTRY_CICD_OPT_IN` (see "Availability and bootstrap" below).

**What the registry is now authoritative for, once a domain calls into it at
all (i.e. once `APP_REGISTRY_CICD_OPT_IN=true`):**

| RPC | Registry outage during a release |
|---|---|
| Recording (`RecordBuild`/`BeginPublish`/`BeginPublishBatch`/`RecordArtifact`) | Individual steps stay `continue-on-error` at the GitHub Actions layer (a transient outage warns, it does not hard-fail the push), but each job's own **App Registry recording health** step turns the job red afterward — see OPERATIONS.md "Recording (automatic, best-effort)". An unrecorded image also makes every later chart release pinning it reject at `RecordArtifact` time (and, via `CheckChartHermeticity`, at compose time too — see below), so "skip it and carry on" is not actually safe to treat as best-effort in practice, even though the CI step itself does not hard-fail. |
| `CheckChartHermeticity` (AR-7f) | Not fatal to the chart build on a transport/auth error — logged as a warning, the build proceeds (see "Compose-time chart hermeticity" below). Only an actual `enforced = true` response naming violations fails the build. |
| `AllocateVersion` (AR-5) | Release-critical for every domain: the registry hands out the version number, and (per the AR-5 cutover) any `AllocateVersion` error is now fatal to the release rather than falling back to tag-scanning — see PLAN.md "AR-5 — cutover status" and `09-relationship-to-ar5.md`. |

The global rollback lever is `APP_REGISTRY_CICD_OPT_IN=false` — there is no
longer a per-domain rollback (moving a domain's stage back), since there is
no per-domain stage. This retracts, rather than merely restates, the promise
in "Availability and bootstrap" below for `AllocateVersion` and
`CheckChartHermeticity`: that section's "the registry can be down for hours
without blocking a release" no longer holds once the opt-in is on, for any
domain, not just a domain that has separately "earned" the stronger
guarantee.

**The `main`-push sweep is not on this scale — it fails red on any error**
(decided in review of PR #559, **built in AR-7a**, unaffected by the AR-5
cutover). `ci.yml`'s `reconcile-app-registry` job drops `continue-on-error`
entirely: a rejected sweep is our manifests being wrong, an unreachable
registry is worth knowing about immediately, and the job gates nothing
downstream, so a red costs attention and nothing else. The consequence is
deliberate and worth stating plainly: **once the opt-in is on, a registry
outage turns `main` CI red**, and `APP_REGISTRY_CICD_OPT_IN=false` is the
only lever that stops it. That is the same kill switch the rest of the
integration already hangs on, not a new one.
