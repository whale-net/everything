# ListArtifactPins (issue #609)

`ArtifactRegistry.ListArtifactPins` is `ResolveArtifact`'s mirror image:
given an image artifact (by `artifact_id` or `digest`), it returns the chart
artifacts whose `artifact_link` row pins it, walking the same table as
"Chart → image lockfile" above but in the opposite direction (`WHERE
al.image_artifact_id = $1` against `artifactSelectBase` joined on
`al.chart_artifact_id = a.artifact_id`, instead of `ResolveArtifact`'s
`WHERE al.chart_artifact_id = $1`).

**Not-found vs. empty (FR3.2/FR3.3).** An image artifact that exists but
that nothing currently pins returns an empty list, not an error — same
"exists but has none" convention used elsewhere in this API. An
`artifact_id`/`digest` that doesn't resolve to any artifact at all is
`NotFound`, the same convention `GetReleaseRun` uses for an unknown
`workflow-run-id`. Passing a *chart* artifact's id/digest — the reverse
lookup only makes sense for images — is rejected with `InvalidArgument`,
mirroring `ResolveArtifact`'s own chart-kind check inverted to require an
image.

**Deliberately unpaginated.** Unlike `ListReconcileRuns` (issue #607) and
`ListBuilds` (issue #608), this RPC takes no page size/token:
`artifact_link_image_artifact_id_idx` (migration 001) already backs the
lookup, and fan-in per image is bounded by the number of distinct pinning
chart-*versions*, not by total repo history the way `build`/`reconcile_run`
grow unboundedly over time. Revisit if a widely-shared base image's fan-in
ever grows unbounded in practice.

