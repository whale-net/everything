# Availability and bootstrap

The registry is itself a `release_app` in this monorepo, so it deploys itself.
That circularity is only safe because **nothing in the deploy path calls the
API synchronously**:

- ArgoCD reads the gitops repo, which the worker writes.
- The S3 snapshot is an auth-free read path for tooling that has no gRPC client.
- CI recording is best-effort: a registry outage warns, it does not fail a
  release.

The registry can be down for hours without blocking a release or a deploy. The
only thing lost is the ability to *make new promotions* during the outage.

> **Superseded by the AR-5 cutover.** The claim above ("can be down for
> hours without blocking a release") originally held only while a domain's
> recording was best-effort — issue #558 scoped that to adoption stage
> `observe`, with recording becoming required at `promote` and the registry
> entering the version path (an outage blocking that domain's releases) at
> `allocate`. The AR-5 cutover removed the per-domain stage entirely:
> recording, chart hermeticity, and version allocation are now unconditional
> and release-critical for every domain, all the time, once
> `APP_REGISTRY_CICD_OPT_IN` is on — see "Release lifecycle (issue #558)" →
> "Availability, restated per adoption stage" for the current picture. The
> claim above is no longer accurate for `AllocateVersion` or
> `CheckChartHermeticity`; it still describes best-effort recording's
> `continue-on-error` posture at the GitHub Actions layer.

## Version skew vs. outage (issue #570)

"Best-effort" above described every registry-call failure as one bucket: a
`continue-on-error` step goes red, the job stays green, an operator has to
read the step log to find out why. Issue #569 showed that bucket hides two
very different failures. `codes.Unavailable`/`DeadlineExceeded`/
`Unauthenticated` etc. are an **outage** — transient, self-clearing, exactly
what "best-effort" is designed to absorb. `codes.Unimplemented` is a
**deployment defect**: CI's `app-registry` CLI is built from the commit being
released, the server is whatever was last deployed, and if the CLI calls an
RPC the server predates, retrying the same release does nothing — only
rolling the server forward (or turning off `APP_REGISTRY_CICD_OPT_IN`) clears
it. In #569 that ran silently for two releases across two different missing
RPCs before anyone noticed, because both cases produced the same
`::warning::`.

The CLI now classifies this centrally instead of every caller string-grepping
stderr: `cli/cmd/root.go`'s `exitCodeFor` maps a gRPC status of
`codes.Unimplemented` to `exitVersionSkew` (process exit code 4), distinct
from the generic exit 1 every other failure (including every outage code
above) still gets, and from `exitOwnerNotReconciled` (exit 3, issue #547 —
an *application*-level rejection, not a missing method). Every composite
action and inline script in `.github/actions/app-registry-*` and
`release.yml`'s chart-recording loops branches on exit 4 to emit `::error::`
instead of `::warning::`, naming it a version-skew/deployment defect that
will not clear on retry rather than "the registry might be down, try again."

This closes the loudness gap `AssertApps`-first-step notwithstanding: even
though `app-registry-assert` runs before every other registry call in a
release (AR-7c, see "`AssertApps` (additive) vs. `ReconcileApps`" above), a
skew there now surfaces immediately rather than only once whichever
downstream RPC happens to be the first one the deployed server lacks.

**Original scope note, now superseded below:** at the time this section was
written, the steps stayed `continue-on-error: true` with no job-level
consequence — an `::error::` annotation was louder than `::warning::` in the
run's summary, but nothing turned the job red on its own, so seeing it still
required someone to read the summary. See "App Registry recording health: no
more silent job-level failures" below for how that gap was closed, without
giving up the underlying `continue-on-error` posture this whole section
depends on. The `main`-push reconcile sweep (`ci.yml`,
`app-registry-reconcile`) already fails red on any error including skew,
uniformly, because it was already NOT `continue-on-error` (AR-7a) — it needed
no change here or below.

## App Registry recording health: no more silent job-level failures

The gap the note above described — a recording failure being real but
invisible at the job level — is what let the chart-repository `BeginPublish`
bug (see "`artifact.repository` on the `∅ → publishing` branch" above) ship
and run for as long as it did: every individual recording step's failure was
masked by its own `continue-on-error`, so the job, and the whole run, stayed
green regardless of the `::error::`/`::warning::` annotation's loudness.
Nobody watches annotations on a green run.

Each of `plan-release`, `release`, and `release-helm-charts` now ends with
one **App Registry recording health** step. Every individual recording step
in that job is still `continue-on-error: true` — the availability contract
above is unchanged, and a registry outage still cannot block a real
image/chart push. This step runs LAST, after every real push, tag, and
upload in the job has already happened, and is deliberately NOT
`continue-on-error`: it reads every recording step's own `steps.*.outcome`
in that job and fails if any of them is `failure`. That failure is the
job's, and the whole run's, only because nothing real was left to protect by
the time it runs.

This goes further than issue #570 originally asked for (only make version
skew louder): ANY recording failure — outage, skew, or the routine "release
ran ahead of reconcile" case (`exitOwnerNotReconciled`, issue #547) — now
reddens the job. The last of those is expected and self-heals, but the
recommended response (re-run once `main`'s CI has caught up, or accept the
miss) is the same either way, and a red run is a far better prompt for that
than a warning nobody reads. See OPERATIONS.md "Recording (automatic,
best-effort)" for what an operator sees and does next.

## `APP_REGISTRY_CICD_OPT_IN` — the bootstrap kill switch

"Best-effort" is not enough on its own. The registry is built and released by
the very pipeline that would call it, so before it is deployed and its secrets
exist there is a genuine chicken-and-egg risk: **you must never be unable to
build the app because the app is not yet deployed.**

Every CI step that talks to the registry is therefore gated on a GitHub repo
variable:

```yaml
if: vars.APP_REGISTRY_CICD_OPT_IN == 'true'
```

- **Unset or anything other than `true` (the default): CI makes no registry
  calls at all.** The pipeline behaves exactly as it does today. This is the
  state the repo ships in and stays in until the registry is deployed and its
  credentials are configured.
- **`true`:** recording steps run, still `continue-on-error` so a registry
  outage warns rather than failing a release. Each job's own **App Registry
  recording health** step still runs last and turns the JOB red on a
  recording failure — but only after the real release work in that job has
  already completed. See "App Registry recording health" above.

There used to be two independent gates here: `APP_REGISTRY_CICD_OPT_IN`
(GitHub Actions — does CI talk to the registry at all?) and
`domain_adoption.stage` (registry server — for a given domain, what is the
registry authoritative for?). **The AR-5 cutover removed the second one.**
`domain_adoption` is dropped, and the registry is now authoritative for
recording, chart hermeticity, and version allocation for every domain,
unconditionally, the moment it is called at all. `APP_REGISTRY_CICD_OPT_IN`
is therefore the only gate left: it decides whether CI talks to the registry
at all, and turning it off is the single-lever rollback for the entire CI
integration — there is no longer a second, per-domain lever underneath it.

Applies from AR-2c (the first phase to add CI steps) onward, including AR-5's
`AllocateVersion` — version allocation must fall back to the tag-based path
when the opt-in is off, or a registry outage becomes a release outage.

