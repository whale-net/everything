# Design: ManManV2 UI Redesign

**Status**: Draft — Alex to finalize. Wireframes are the source of truth
for layout; this doc records decisions, assumptions, and open items so
future sessions can continue without re-deriving context.
**Written**: 2026-07-18.

Wireframes: `manmanv2/ui/design/wireframes/` — proposal screens are the
`9x-*` fragments ("Proposal: …" in the preview). Iterate with the
`/wireframe` skill; assemble with
`bazel run //tools/wireframe -- --dir manmanv2/ui/design/wireframes --title "ManMan Wireframes"`.

## Decisions made (2026-07-18)

1. **Terminology**: *Game → Game Config → Deployment*. "SGC" disappears
   from all user-facing surfaces (stays as internal code/schema name,
   renamed opportunistically). No SGC IDs shown to users.
2. **Baseline + delta model**: a Game Config is the common baseline;
   a Deployment is where it runs plus how that copy differs — env
   overrides ([DESIGN_SGC_ENV_OVERRIDES.md](DESIGN_SGC_ENV_OVERRIDES.md),
   Option B accepted) and one-off extra addons.
3. **Workshop libraries move to GC level**, inherited by the config's
   deployments; individual addons can be added per deployment. (Today
   libraries attach at SGC level: `/sgc/add-library`,
   `workshop_installations UNIQUE(sgc_id, addon_id)`.)
4. **IA / navigation**: Dashboard · Games · Activity · Infrastructure ·
   Workshop. Games is the hub; Activity is observation-only (start/stop
   happens in Games); Infrastructure is admin-only.
5. **Games page = flat list, expand in place** (not tiles, not a page
   hop): the expanded game shows Deployments, Configurations, Workshop
   Libraries, and a settings/danger footer inline. Chosen over
   card-grid + workspace-panel specifically to avoid multi-layer
   stacking.
6. **Blade layers only one deep, only for complex editing**: Customize →
   Deployment Settings (env overrides + extra addons), Edit → Config
   Editor (basics/ports/env/volumes+backups). Scrim/Esc tabs out.
   Everything routine stays inline in the list.
7. **Users care about "what games, are they up, how do I connect"** —
   game rows lead with status and a copyable `address:port`.
8. **Actions manager stays as-is for now** (legacy page works well
   enough); new-game action definitions should eventually be UI-driven
   instead of `scripts/seed_*_actions.sh`, but no redesign now.
9. **Infrastructure**: hosts register automatically on start — no
   register/deregister UI; **drain/undrain is the only mutation**.
   Draining hosts leave Deploy pickers.
10. **Styling**: daisyUI is the target (see DESIGN_SYSTEM.md "Future
    Direction"); wireframe `themes.css` is the canonical theme mapping,
    including the pure-black OLED the current app lacks.

## Functionality the wireframes assume but that does NOT exist yet

Backend / platform work implied by the proposal screens:

| Assumed in wireframe | Reality today |
|----------------------|---------------|
| Connect address (`public_ip:port`) per deployment, copyable; shown on game rows | **No public IP anywhere in the system.** Host manager must report it; API must expose it. Biggest blocker. |
| Player counts (`4/10`) and aggregate game status ("2 running") | No player telemetry or per-game rollup endpoint; needs verification/instrumentation. |
| Ports editable on the Game Config (Config Editor "Ports — new") | Ports are not editable via UI/API; set by `scripts/load-*-config.sh`. |
| Start/Stop directly on a deployment row; Restart on a crashed game | Start is a separate sessions flow today; no deploy-and-start composite; no one-click restart. |
| Per-deployment env overrides (Deployment Settings) | Accepted design, unbuilt — see DESIGN_SGC_ENV_OVERRIDES.md work plan. |
| GC-level libraries + per-deployment extra addons | Schema/API change (libraries are SGC-level today); not covered by any work plan yet. |
| Library/addon install-target picker (volume + path, preset-backed) | Install target is implicit today; presets exist but aren't wired into attach flows. |
| Volume backup config inline on the volume (Config Editor) | Backups exist as separate backup-configs; UI/API reshaping assumed. |
| Drain/undrain host | Does not exist. |
| "Last played", uptime/duration fields (Games list, Activity) | Availability unverified. |
| Delete Game cascading (configs, deployments, presets) | Cascade semantics need confirming before exposing one button. |

## TBD / not yet designed

- **Player vs admin**: one UI with role-hidden sections (Configurations,
  Infrastructure, settings) vs a separate stripped player view. Current
  lean: one UI, progressive disclosure later.
- **Session detail**: proposal links to the current-state page; whether
  it becomes a layer, a drawer, or stays a page is undecided.
- **Workshop top-level page**: library/addon CRUD (fetch, create,
  search) stays top-level for now; only game-scoped attach moved into
  the Games page. Revisit whether Workshop shrinks further.
- **Actions**: where action *definitions* surface in the new IA once
  they become UI-driven (expanded game row? config editor section?).
- **Dashboard**: unchanged so far; may want per-game connect tiles.
- **Restart semantics**: what happens to running deployments when their
  config or deployment settings change (prompt to restart? badge for
  "config drift"?). Related open question in the env-overrides doc.
- **Migration path**: current 19 pages → new IA (which pages die, which
  redirect); nothing planned yet.
- **Collapsed game rows** show a one-line summary; exact summary
  content (players vs address vs last-played) not settled.

## Wireframe fidelity caveats

Current-state screens (`01`–`54`) approximate the real pages from
template structure — good enough for flow reasoning, not pixel
reference. `43-actions-manage` is aspirational (real page is the legacy
manager). The kit's layering feature (`parent=` fragments) is documented
in `tools/wireframe/README.md`.
