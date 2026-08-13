# App Registry Admin UI — Wireframes

Design-iteration wireframes for a possible App Registry admin UI. Not
production code — fake data, static markup, daisyUI classes. See
[../USER_STORIES.md](../USER_STORIES.md) for what these screens are for and
[../PRINCIPLES.md](../PRINCIPLES.md) for the design principles behind them.

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
| `10-environment-status` | What's currently promoted in one environment — the primary "what's deployed" screen |
| `11-app-detail` | One app across all environments + its promotion timeline |
| `12-environment-diff` | Side-by-side comparison of two environments |
| `20-apps-catalog` | Every registered app/chart, its deploy unit, and current version per environment |
| `21-artifact-detail` | One artifact by digest — its build provenance and, for charts, what it pins |
| `30-builds` | CI run search, recording-health status per run |
| `31-build-detail` | Per-run artifact states (`publishing`/`published`/`failed`/`allocated`) |
| `40-drift-audit` | Drifted overrides and adopted-artifact audit, cross-environment |
| `50-promote` (layer) | Promote form — promotability guardrail, override ack, reason, dry-run |
| `51-rollback` (layer) | Rollback confirmation showing the exact SCD2-derived target |
| `52-adopt` (layer) | Admin-only disaster-recovery artifact adoption, reached only from troubleshooting |
