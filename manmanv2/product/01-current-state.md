# Current state

Survey of what exists today in `manmanv2/`, checked against the capability map above. Verified against code, not just docs, where the docs seemed likely to be stale.

### Control-plane API (`api/`, gRPC :50051 + REST gateway)
Go/Postgres. ~85 RPCs off `protos/api.proto` (game, gameconfig, server, servergameconfig, session, backup, backup_config, action_definition/execution, patch, volume, registration, logs) plus a separate `workshop.proto`. 34 applied migrations (`manmanv2/migrate/migrations/`, `001`–`033` with two down-only variants) — the schema has already been through at least one full normalize/rollback cycle (typed parameter system added in migration `006`, never wired to any Go code, still schema-only and dead today). This directly backs **C1–C4, C12–C16**.

### UI (`ui/`, Go+templ+HTMX, DB-backed sessions via `libs/go/htmxauth`)
19 pages today (games, gameconfig CRUD, servers, sessions, SGC/deployment detail, backups, actions manager, workshop search/library/installation/addon-detail, config-strategy docs, session detail w/ live log viewer). Chrome/nav already migrated to shared `libs/go/htmxui` (daisyUI, Phase 2/3 of PR #1080/#1081/#1082); page bodies still raw Tailwind `dark:` classes — an in-flight migration, not a stalled one. A full **redesign is already designed, not yet built**: `manmanv2/docs/DESIGN_UI_REDESIGN.md` (dated 2026-07-18, status "Draft — Alex to finalize") has already made most of the calls C20/C21/C31/C25 imply (terminology, IA, blade pattern, baseline+delta model) and — critically — contains an explicit table of "assumed but does not exist" backend gaps that lines up almost exactly with the Next/Later bucket: no public connect address anywhere, no player-count telemetry, ports not editable, no one-click start/stop, per-deployment env overrides unbuilt, GC-level workshop libraries unbuilt, drain/undrain doesn't exist. This is a gift for scoping — the brief doesn't need to rediscover these gaps, but it does need to decide which milestone each belongs to.

### Host manager (`host/`, Go, bare-metal, Docker SDK)
Manages containers directly (create, attach stdin/stdout, stop, label-based recovery), self-registers (TLS-capable), publishes status/health/logs to RabbitMQ, renders configuration (including the `env_vars` strategy layer — see below), orchestrates Workshop addon installs, takes local backups. Self-updates in production via the `tools/compose-resolver` sidecar (`host/RESOLVER.md`, shipped PR #1323) polling App Registry promotion — confirmed live, not aspirational; the resolver mounts `/var/run/docker.sock` (root-equivalent) and documents that trust level explicitly.

### Event processor (`processor/`)
RabbitMQ consumer syncing host/session status to Postgres, session state-machine validation, stale-host detection with auto-recovery, republishes internal events onto an `external` exchange for downstream integrations (backs **C19**).

### Log-processor (`log-processor/`, gRPC :50053)
Real-time log streaming (fan-out) + optional S3 archival gated on `PG_DATABASE_URL`+`S3_BUCKET`+`API_ADDRESS`; backs the UI live log viewer; `GetLogHistogram`/`GetHistoricalLogs` RPCs.

### Auth
Platform-wide `GRPC_AUTH_MODE` (`none`/`oidc` via Keycloak), independently set per component (API, log-processor, host, UI's own `AUTH_MODE` for the browser session plus `GRPC_AUTH_MODE` for token forwarding) but required to match — confirmed in `ENV.md` and every `main.go`. There is no per-component auth policy and no code path that would let one component run `oidc` while another runs `none`.

### Connect-address / public-IP gap (relevant to C20)
Confirmed by grep: no `public_ip`, `ConnectAddress`, or equivalent field exists anywhere in `manmanv2` Go or proto source. This matches `DESIGN_UI_REDESIGN.md`'s own assessment verbatim ("No public IP anywhere in the system... Biggest blocker"). C20 is not a UI-only feature — it needs a new field the host manager populates and the API exposes.

### Env-override design (relevant to C22)
`manmanv2/docs/DESIGN_SGC_ENV_OVERRIDES.md` is a finished, accepted design doc (2026-07-18, "Option B accepted, not yet scheduled"), not a TBD. It found, in code: container env is built solely from `GameConfig.env_template` at session start (`host/main.go:265`); a **second**, mostly-built layering system already exists (`ConfigurationStrategy`/`ConfigurationPatch`, DB+API+renderer all present) but its `env_vars` render path is never invoked at session start; and a **third**, fully dead system (migration `006`'s typed parameter tables) has no Go references at all. The doc picks Option B (expand the patch system behind a thin convenience API) over a simpler flat `env_overrides` map, explicitly to avoid a fourth mechanism, and lays out a 5-step work plan including retiring the dead parameter tables.

### SGC / Deployment terminology and customization-layer direction
Also already decided (both design docs, agreed 2026-07-18): SGC is user-facing "Deployment"; the entity trio is Game → Game Config → Deployment; SGC is the customization layer over the GC baseline; Workshop libraries move from SGC-level to GC-level (inherited), with per-deployment one-off addons layered on top. Code/schema keep `sgc` internally, renamed opportunistically — this is intentional, not drift.

### Constraints confirmed in schema/code
- No uniqueness constraint on `(server_id, game_config_id)` in `server_game_configs` (migration `001`) — deploying the same GameConfig twice to one host is allowed by design. The only conflict guard is `server_ports`' `UNIQUE(sgc_id, server_id, port, protocol)` (migration `008`).
- `workshop_installations` has `UNIQUE(sgc_id, addon_id)` (migration `022`) — SGC-scoped, confirming the GC-level promotion (C28) needs a migration off this key, not just new UI.
- Container identity/reconciliation is entirely label-based: `manman.type`, `manman.session_id`, `manman.sgc_id`, `manman.server_id`, set in `host/session/manager.go` and read back in `host/session/recovery.go` — confirmed as the sole mechanism, no secondary reconciliation path.

### Doc hygiene (flagged, not a blocker)
`manmanv2/ARCHITECTURE.md` is stale — it's an early "System Design Document" (header: "Status: Design Complete") that still lists `api/`/`processor/`/`host/` as directory-structure "(planned)" and carries an implementation-phase checklist for work that's long since shipped (Phases 1–6 all checked off, but the doc never graduated past planning-doc framing). It has no mention of the UI, log-processor, or host resolver at all — three of the platform's live components. `manmanv2/ABOUT.md` currently holds old Tilt-setup content, not a product narrative. Neither should be trusted as a structural reference as-is; recommend both get refreshed as part of (or immediately after) this product brief landing, since `PRODUCT.md` is about to become the authoritative vision/roadmap doc and `ARCHITECTURE.md` should defer to it rather than duplicate stale narrative.

No evidence of a recently reverted or orphaned manmanv2 package in `git log` — the one revert in the last 30 commits (`71d828b0`, "Revert plan #1166 v2 merged scaffolding") is entirely `leaflab/`, unrelated.
