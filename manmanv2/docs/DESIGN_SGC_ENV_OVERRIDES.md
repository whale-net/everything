# Design: Per-Instance (SGC) Env Var Overrides

**Status**: Accepted 2026-07-18 — **Option B** (expand the patch system
behind the convenience API). Not yet scheduled for implementation.
**Problem owner**: Alex. **Written**: 2026-07-18.

## Problem

Env vars live only on the GameConfig (`GameConfig.env_template`). Values
that must differ per deployed instance — the game's port setting, world
name, etc. — can't vary per SGC, so deploying the same config twice to a
server means **duplicating the whole GameConfig** with one env var changed.
SGCs already vary `port_bindings` (host-side mapping), but the game process
reads its port from env, so the container side still forces duplication.

## Current state (verified in code)

- Container env is built **solely** from the GC template at session start:
  `host/main.go:265` (`cmd.GameConfig.EnvTemplate` → `env`). No other env
  source is consulted.
- `ServerGameConfig` (`protos/messages.proto`) carries `port_bindings` and
  status only — no env.
- **A layered override system already exists and is mostly implemented**:
  - `ConfigurationStrategy` (`strategy_type: "env_vars"` among others) +
    `ConfigurationPatch` with `patch_level` ∈ `game_config` /
    `server_game_config` / `session` (`protos/messages.proto`).
  - Full DB + API CRUD: `api/repository/postgres/patch.go`,
    `api/handlers/strategy.go` (`configuration_patches` table).
  - Host renderer handles `env_vars` (`host/config/renderer.go:69`).
  - `RenderedConfiguration`/`PatchLayer` already model "show me the
    layering" for UI.
- **A third, dormant system exists**: migration
  `006_normalize_parameters` created typed per-game
  `parameter_definitions` with values at both GC
  (`game_config_parameter_values`) and SGC
  (`server_game_config_parameter_values`) levels — exactly per-instance
  overrides, with types/validation/defaults. **No Go code references
  these tables**; it is schema-only. `server_game_configs.parameters
  JSONB` (001) is likewise unused.
- **The gap**: the `env_vars` strategy output is never merged into the
  container env at session start. Three env/param systems: one live and
  flat, one coded but unwired, one schema-only.

## Options

The patch system doesn't *have* to be the answer — it's simply already
there. The decision is whether to expand it, go around it, or replace it.

### Option A — `env_overrides map<string,string>` on ServerGameConfig

Add a plain override map to the SGC proto/table; host merges
`env_template` + `env_overrides` at start.

- **For**: small, obvious data model; one proto field, one column, a
  ~10-line merge in `host/main.go`; matches the single real use case
  today; nothing depends on the health of the patch system.
- **Against**: creates a second (third, counting strategies) env
  mechanism; no path to session-level overrides or templated values;
  the patch system stays half-used and the "why do both exist"
  question compounds.

### Option B — expand the existing patch system

Treat `env_template` as the base layer of an implicit `env_vars`
strategy; SGC overrides are `ConfigurationPatch` rows at
`server_game_config` level. Merge order (later wins): `env_template` →
`game_config` patches → `server_game_config` patches → (`session`
patches, deferred).

- **For**: reuses shipped machinery (DB, CRUD, renderer, layering
  models); session-level overrides and effective-config visibility come
  almost free; one mental model for all config layering.
- **Against**: heavier — strategy plumbing for what is today "change one
  env var"; the env_vars render path is **not currently exercised at
  session start**, so expanding it means validating it, not just using
  it.

### Option C — build a new config-layering system

Retire both `env_template` and the strategy/patch system; design one
mechanism for all config (env, files, args) with explicit layers
(game → config → instance → session) and templating from day one.

- **For**: one coherent model instead of two-and-a-half; designed
  against actual needs rather than inherited shape; chance to fix the
  strategy system's over-generality (patch formats, apply_order) that
  nobody uses.
- **Against**: by far the biggest lift for what is today one missing
  override; requires migrating `env_template` data, existing
  `configuration_patches` rows, and the config-strategies docs/UI;
  highest risk of stalling at the design stage while GC duplication
  continues.

### Recommendation (accepted)

**B, behind the thin convenience API below** — the UI and callers see
"per-instance env overrides", the patch system is an implementation
detail. Tipping factors:

- If validating the patch-render path turns out to be a project of its
  own, or session-level overrides are firmly out of scope forever, →
  **A** (and it can migrate to B later; the merge semantics are
  identical).
- If validation instead reveals the patch system is unfit (wrong
  granularity, unused generality getting in the way), that's the
  evidence that justifies **C** — decide then, with the findings in
  hand, rather than designing a new system speculatively now.

## Work plan (assuming B; A collapses steps 1–2 into one)

### 1. Wire env rendering into session start (host/processor)

`StartSessionCommand` (`host/rmq/messages.go`) gains a
`rendered_env map[string]string`. The processor (or API) renders
env = template + patches **server-side** before publishing, so the host
stays dumb. `host/main.go` uses `rendered_env` when present, falling back
to `EnvTemplate` (rolling-upgrade safe).

### 2. Convenience API for the common case

The patch CRUD is generic (strategy + level + format). Add a thin RPC so
callers (UI) don't need strategy plumbing:

- `GetEffectiveEnv(sgc_id)` → rows of `{key, base_value, override_value}`
  (reuses `RenderedConfiguration`/`PatchLayer` shapes)
- `SetSGCEnvOverrides(sgc_id, map<string,string>)` → upserts a single
  `env_vars` patch at `server_game_config` level (format
  `json_merge_patch`; empty map deletes the patch)

### 3. Port-binding template variables (stretch, kills the root cause)

Most per-instance env is "tell the game its port". Support template refs
in env values, resolved at render time against the SGC:
`SERVER_PORT={{ .HostPort 2456 }}` — the assigned host port for container
port 2456. Then one GC deploys N times with zero manual overrides.

### 4. UI (game workspace, wireframe when ready)

On each Instances row in the game workspace (`91-v2-game-workspace`): an
**Overrides** control opening an effective-env panel — three columns:
key, config value (muted), instance override (editable input, empty = 
inherit). Save calls `SetSGCEnvOverrides`. A count chip on the row
(`2 overrides`) signals divergence from the config. Wireframed as
`ui/design/wireframes/screens/95-v2-instance-overrides.html`.

### 5. Cleanup

- Existing duplicated GCs (`vanilla-2456`-style) are merged by hand after
  the feature ships; no automated migration.
- Drop the dead migration-006 parameter tables
  (`parameter_definitions`, `game_config_parameter_values`,
  `server_game_config_parameter_values`) and
  `server_game_configs.parameters` in a cleanup migration, so B leaves
  exactly one override mechanism standing. If typed/validated params
  are ever wanted, that's a future layer on top of patches — don't keep
  the dead schema around as an implicit promise.

## Open questions

- ~~Does deploying the *same* GC twice to one server work today?~~
  **Answered 2026-07-18**: yes — `server_game_configs` (001) has no
  UNIQUE on (server_id, game_config_id). The practical guard is port
  conflicts (`008_server_ports`: UNIQUE (sgc_id, server_id, port,
  protocol)), which phase 3's `{{ .HostPort }}` templating handles.
- Should `GetEffectiveEnv` also surface `game_config`-level patches
  distinctly, or collapse template+GC-patches into one "config" column?
  (UI currently assumes two columns.)
- Session-level overrides ("start once with a different world"): deferred;
  the patch system already models the level, so nothing here blocks it.
