# App Registry Admin UI — Guiding Principles

These principles govern `design/wireframes/` and should carry forward into
any real implementation. They exist to keep the UI honest about what the
registry actually is — see [README.md](../README.md): **the registry records
and answers questions, it does not deploy anything.** Every principle below
is a consequence of that one sentence, or of a specific operational lesson in
[OPERATIONS.md](../OPERATIONS.md) / [ARCHITECTURE.md](../ARCHITECTURE.md).

1. **State, not control.** No screen implies the UI can make a cluster do
   anything. "Promote" writes a promotion row and an outbox intent; it does
   not deploy. Copy, icons, and button labels must never suggest otherwise —
   see [OPERATIONS.md "What actually deploys anything, today?"](../OPERATIONS.md#what-actually-deploys-anything-today).

2. **Promotability is the guardrail, always visible.** `PROMOTABLE` /
   `VIA_CHART` / `NOT_PROMOTABLE` is the single most useful thing the
   registry computes (README.md "Promotability"). It must be visible
   everywhere an artifact appears — catalog, status, promote form — not
   discovered only after a rejected submit. A `VIA_CHART` promotion is always
   visually distinct from a first-class one, both before promoting
   (override acknowledgment) and after (drift-tracked, forever).

3. **Digests over labels.** The semver tag is a label; the digest is
   identity (README.md "Core concepts"). Any view showing "what's deployed"
   shows the digest (truncated, but copyable/expandable) alongside the
   version — two apps can share a version string; they cannot share a
   digest.

4. **Time is a dimension, not just "now."** Promotion state is SCD2: current
   answers `valid_to IS NULL`, historical answers a window read
   (`AGENTS.md` SCD2 conventions; README.md "Incident-time query"). Status
   and diff views are built around an explicit instant from the start, so
   "what was prod running last Tuesday" is a first-class query, not a
   feature bolted on later.

5. **Never hide a failure behind green.** This is a lesson paid for in
   production: issues #547, #570, and the `BeginPublish` chart-repository
   bug all shipped because a masked step-level failure kept a job green
   (OPERATIONS.md "Recording (automatic, best-effort)"). The UI's job is the
   opposite of `continue-on-error` — every partial, failed, or stale state
   (build artifact states, recording health, drift) is rendered loud and
   specific, never folded into a generic "OK" or a silent absence.

6. **Reason is a first-class field, not an afterthought.** Every mutating
   action the registry exposes to a human — promote, rollback, adopt —
   carries a `reason` that becomes part of the permanent audit trail
   (`promotion_event`, adopted-artifact logs). The UI treats it as required
   input worth a real label and helper text, not a placeholder-only textbox
   that's easy to fill with "fix".

7. **Dry-run is how you build trust in a write you can't undo cheaply.**
   Promotions above `dev` default toward dry-run first (OPERATIONS.md
   "Prefer `dry_run: true` first against anything above `dev`"). A dry-run
   result must be impossible to mistake for a committed write — different
   framing, not just a small badge.

8. **Rare, dangerous actions look rare and dangerous.** `AdoptArtifact` is
   "a rare, deliberate operation, not routine maintenance"
   (OPERATIONS.md "Adoption and disaster recovery"), admin-role only, and
   asserts a fact about the outside world the registry cannot verify. It
   lives behind a distinct troubleshooting path, never next to the routine
   promote button, and its output stays permanently tagged
   (`provenance: adopted`) everywhere it's later shown.

9. **Data density over decoration, for the troubleshooting half of the job.**
   The admin's bad-day work (build/run status, artifact state tables, drift
   audits) is inherently tabular and detail-heavy. Favor dense, scannable
   tables with monospace identifiers and consistent state badges over cards
   and whitespace — this is an ops console for that workflow, not a
   marketing dashboard. The calm-day half (dashboard, environment overview)
   can afford more breathing room; the distinction is deliberate, not
   inconsistency.

10. **One vocabulary, everywhere.** State names (`PUBLISHING` / `PUBLISHED` /
    `FAILED` / `ALLOCATED`), promotability values, and provenance
    (`observed` / `adopted`) are rendered with one consistent badge color and
    label per value across every screen. An admin should never have to
    re-learn what a color means between the dashboard and a build-detail
    page.
