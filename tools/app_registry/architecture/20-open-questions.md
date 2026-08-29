# Open questions

The gitops repo layout is deferred rather than unresolved — it is answered when
the writeback stub is replaced with the real implementation.

All five AR-7 (issue #558) refinements that were parked here have been decided
in review of PR #559 and folded into the sections that own them:

| Question | Decision | Where |
|---|---|---|
| Declared intent set for a release run | The plan step writes an `allocated` row per target unconditionally; no `build_target` table | "The run log" |
| `reconcile-app-registry` and `continue-on-error` | Dropped — the job fails red on any error, with `APP_REGISTRY_CICD_OPT_IN` as the only lever | "Availability and bootstrap" |
| Where recording becomes mandatory | Every domain, unconditionally | same |
| Manifest snapshot storage | Verbatim `JSONB` + stored generated columns for `deploy_unit` / `image_repository` | "App identity vs. per-build manifest snapshot" |
| AR-7f compose-time chart enforcement | Unconditional for every domain, no per-domain gate | "Rejected alternatives (issue #558)" |
