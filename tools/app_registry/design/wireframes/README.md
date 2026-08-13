# App Registry Admin UI — Wireframes

Design-iteration wireframes for a possible App Registry admin UI. Not
production code — fake data, static markup, daisyUI classes. See
[../USER_STORIES.md](../USER_STORIES.md) for what these screens are for,
[../PRINCIPLES.md](../PRINCIPLES.md) for the design principles behind them,
and [../CONCEPTS_AUDIT.md](../CONCEPTS_AUDIT.md) for which screens assume a
capability the registry API doesn't actually have yet — read that before
turning any of this into real code.

```
bazel run //tools/wireframe -- --dir tools/app_registry/design/wireframes --title "App Registry Wireframes"
open tools/app_registry/design/wireframes/preview.html
```

`preview.html` is generated and gitignored — never edit or commit it; edit
the fragments in `screens/` (and `_shell.html` for shared chrome) and re-run
the assembler. Workflow and guardrails: `.claude/skills/wireframe/SKILL.md`.
Kit docs: `tools/wireframe/README.md`.

## Screen map

| Screen | Covers |
|---|---|
| `01-dashboard` | Cross-environment overview, drift/health alerts, recent promotions |
| `09-environments` | Environment administration — rare, admin-only; separate from what's deployed |
| `10-environment-status` ("Deployments" in nav) | What's currently promoted, **across every environment at once** — one row per promotable entity, one column per environment, tree-expand for a chart's composed apps |
| `11-app-detail` | One app across all environments + its promotion timeline; links out to its chart (if `VIA_CHART`) and its full version history |
| `12-environment-diff` | Side-by-side comparison of two environments |
| `13-app-version-history` | Every artifact ever recorded for one app, independent of promotion state |
| `20-apps-catalog` | Every registered app/chart, its deploy unit, and current version per environment |
| `21-artifact-detail` | One image artifact by digest — its build provenance and which chart(s) pin it |
| `22-chart-detail` | One chart — current version per environment, and the apps it publishes at that version |
| `30-builds` | CI run search, recording-health status per run (the AR-7 release/artifact pipeline) |
| `31-build-detail` | Per-run artifact states (`publishing`/`published`/`failed`/`allocated`) |
| `32-reconcile-runs` | `ReconcileApps` sweep history (the AR-8 identity pipeline) — applied vs. rejected-stale, which manifests changed |
| `40-drift-audit` | Drifted overrides and adopted-artifact audit, cross-environment |
| `50-promote` (layer) | Promote form — promotability guardrail, override ack, reason, dry-run |
| `51-rollback` (layer) | Rollback confirmation showing the exact SCD2-derived target |
| `52-adopt` (layer) | Admin-only disaster-recovery artifact adoption, reached only from troubleshooting |
| `53-environment-form` (layer) | Create/edit an environment; archive lives in this same layer's danger zone |

Two CI-driven pipelines get separate troubleshooting screens on purpose:
Builds (`30`/`31`, AR-7: what CI published) and Reconcile Runs (`32`, AR-8:
what CI told the registry apps/charts look like). They answer different
questions and shouldn't be conflated into one table.

`09-environments` (environment *configuration*) and `10-environment-status`
(environment *state*) are deliberately separate screens under separate nav
items ("Environments" vs. "Deployments") — conflating "what environments
exist" with "what's deployed to them" was the original design's mistake.
