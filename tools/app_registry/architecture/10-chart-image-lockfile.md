# Chart → image lockfile

Resolving a chart's pinned images to **digests** requires a container registry
call. A Bazel action must never make that call — it would break hermeticity
and make chart output non-reproducible, undoing #dd23e807 (which specifically
made chart builds deterministic). So this is two steps, split across two
different environments, not one:

1. **Compose time (hermetic, no network).** `tools/helm/composer.go` already
   resolves the exact image repository and tag it bakes into a chart's
   `values.yaml`. As of AR-2b it additionally writes those same references —
   full app name, repository, and tag — to `image-lockfile.json` alongside
   `Chart.yaml` inside the generated chart. This is a pure function of the
   `AppManifest`s the composer already reads: no digests, no registry access,
   sorted for deterministic output. `tools/release_helper_go`'s
   `read-chart-lockfile <chart-name>` command builds a chart and reads this
   file back out, giving the release path a stable way to get at it without
   re-deriving anything from the manifests.
2. **Publish time (has registry access; implemented in AR-2c).** After
   `release-helm-charts`'s `needs: [plan-release, release, ...]` has pushed
   every image, a CI step resolves each lockfile entry's repository+tag to a
   digest and forwards the result to `RecordArtifact(kind = CHART, contains =
   [...])`.

Without step 2, chart promotion cannot answer "which image digest is
running," and the incident query degrades to rendering charts by hand. This is
the highest-value part of the recording phase and should not be deferred.

The server rejects a chart artifact that references an image digest it has never
recorded — a chart may not pin an unknown artifact.

That reject is correct but is a trap today, because charts pin digests
published by *earlier* runs: one image that never got recorded breaks every
future chart release containing it, permanently. The artifact lifecycle in
"Release lifecycle (issue #558)" is what makes the reject safe to keep — the
image's row exists from `publishing` onward, so failing here means the image
genuinely was never published.

This reject fires at *record* time, after the chart is already packaged and
pushed. "Compose-time chart hermeticity (AR-7f, issue #558)" above moves the
same rule earlier, before anything is pushed — unconditionally for every
domain — still without a registry call inside the Bazel action that
composes the chart, for the reason stated at the top of this section.

**This is also the deploy-time idempotency guarantee for a promoted chart's
app list — see "Resolved questions" #4.** `contains` is a pure function of
the manifest set at the moment the chart was *built*, computed hermetically
by `tools/helm/composer.go` with no registry access; `RecordArtifact` writes
it into `artifact_link` once, keyed to that specific chart artifact, and
nothing ever updates or rewrites an `artifact_link` row afterward. A later
`Reconcile` changing the chart's live `chart_app` composition therefore
cannot change what an already-recorded (and possibly already-promoted) chart
artifact renders.

