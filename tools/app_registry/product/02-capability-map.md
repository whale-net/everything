# Capability map

### Now (shipped, in production use)

- C1 — CI can record every artifact it publishes (image or chart), traced
  back to its build, with no manual step.
- C2 — An operator can see, for any artifact, whether it is a legal
  promotion target (`promotability`), so "what can I promote?" is always
  answerable.
- C3 — An operator can promote an artifact to an environment, and that
  promotion writes back to the gitops repo for ArgoCD to act on.
- C4 — An operator can see the full SCD2 history of what was promoted to an
  environment and when, and — on a separate timeline — the full SCD2
  history of manifest *content* changes on `main` (AR-8), without
  conflating deploy-state history with source-content history.
- C5 — An operator can see per-release build and promotion activity
  (release-run tracking) rather than only per-artifact state.
- C6 — An operator has a full read+write operator surface in a live web
  UI: promote/rollback forms (dry-run enforced above dev, reason
  required), an environments/deployments matrix with as-of/SCD2 time
  travel, a drift & audit screen, per-release build/attempt history, a
  manual ArgoCD retry-sync action, RBAC-gated (presentation-only,
  server-enforced) admin actions, and a live SSE-driven readiness banner —
  not just a read-only view of state.
- C7 — An operator can override a chart-managed ArgoCD Application's name
  per chart, where the default doesn't fit.
- C8 — An operator can rely on structured operator-facing logs to
  diagnose a promotion or recording problem.
- C9 — An operator can tell, per promotion, whether the promoted artifact
  is actually healthy in its environment: ArgoCD sync/health is polled
  after every writeback, shown live via an SSE-driven readiness banner,
  with a manual retry-sync action available. Remaining gap: this is
  per-promotion only — there's no aggregate "is this environment healthy
  right now" view across everything currently deployed there.
- C13 — A release operator can trigger and drive an entire release (build
  → publish → record) for `manmanv2`, `leaflab`, and `app-registry` itself
  from the UI, instead of running `gh workflow run` by hand. Distinct from
  C5, which only tracks releases CI already ran.

### Next (unscheduled work made concrete by the roadmap below)

- C10 — An operator no longer has to manually roll back a bad promotion:
  the registry can automatically promote back to the prior artifact when
  post-deploy health checks (C9) fail. The health signal this needs
  already exists, and the rollback mechanism itself is already a
  first-class RPC (`Rollback`/`PROMOTION_ACTION_ROLLBACK`) — this is a
  decision-policy-and-trigger problem, not new plumbing. Scheduled as M2.
- C11 — A release operator's promotion attempt generates a first-class
  **approval-request**, scoped to the (environment, build) pair being
  promoted; the promotion only executes once that specific request is
  approved, and an approver can reject it instead. Scoped to the
  promotion-layer seam only (`environment.requires_approval`/
  `PromotionState.PENDING_APPROVAL`) — the separate release-trigger-layer
  stub (`worker/release/approval.go`) is not part of this capability; see
  LB1. Scheduled as M3.
- C14 — CI can publish a CLI binary (and, eventually, other non-OCI
  artifact kinds such as firmware) to durable storage through a
  registry-brokered upload: the registry authorizes the upload, the blob
  is stored and deduplicated by content hash, and the published artifact
  version is recorded as a pointer to that hash rather than a location CI
  tracks itself. See LB5 for the blob-identity decision this requires.
  Scheduled as M5.
- C15 — CI workflows and other consumers resolve which artifact kinds
  exist, and how to acquire or publish them, entirely from the registry —
  with no hardcoded tool-name or app-name list surviving in the release
  workflow, the CI acquisition action, or any worker/server code.
  Answerable over the existing publish/resolve mechanism, no CAS
  required. Scheduled as M4.

### Later (genuinely unscheduled, no current demand)

- C12 — (not yet scoped) Ephemeral/per-PR environments. Genuinely cheap
  given today's data model (`environment` is a managed table, writeback
  templates on `(domain, chart, environment.key)`), but no current demand.
