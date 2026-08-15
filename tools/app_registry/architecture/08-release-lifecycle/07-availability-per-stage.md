# Availability, restated per adoption stage

"The registry can be down for hours without blocking a release" and "the
registry is the source of truth for the pipeline" cannot both hold once
`publishing` must be written before a push. Rather than add a lever, the
existing one carries it: `domain_adoption.stage` already means "what is the
registry authoritative for," so it also means "how critical is it."

| Stage | Recording | Registry outage during a release |
|---|---|---|
| `observe` | best-effort, `continue-on-error` | release proceeds; a record may be missing (today's behavior) |
| `promote` | required — the recording step fails the job | release fails rather than silently skipping a record that later chart releases depend on |
| `allocate` | release-critical | release cannot proceed; the registry hands out the version number |

Recording becomes mandatory at `promote`, not only at `allocate` (decided in
review of PR #559): under the artifact lifecycle an unrecorded image is no
longer merely a missing row — it makes every later chart release pinning it
reject, so "skip it and carry on" is the expensive option, not the safe one.

At `allocate` the registry is already release-critical whether or not this is
written down — `AllocateVersion` is in the version path. Per-domain rollback is
moving the stage back; the global rollback is still
`APP_REGISTRY_CICD_OPT_IN=false`. This restates, and partially retracts, the
promise in "Availability and bootstrap" below; that section stays accurate for
`observe`, which is where every domain is today.

**The `main`-push sweep is not on this scale — it fails red on any error**
(decided in review of PR #559, **built in AR-7a**). `ci.yml`'s
`reconcile-app-registry` job drops `continue-on-error` entirely: a rejected
sweep is our manifests being wrong,
an unreachable registry is worth knowing about immediately, and the job gates
nothing downstream, so a red costs attention and nothing else. The consequence
is deliberate and worth stating plainly: **once the opt-in is on, a registry
outage turns `main` CI red**, and `APP_REGISTRY_CICD_OPT_IN=false` is the only
lever that stops it. That is the same kill switch the rest of the integration
already hangs on, not a new one.

