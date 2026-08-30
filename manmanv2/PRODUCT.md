# ManManV2 — Product Brief

Vision, personas, load-bearing decisions, and non-goals for ManManV2. See the jump table below for current state, the full capability map, and the milestone roadmap.

## Jump table

- [Current state](product/01-current-state.md) — survey of what exists today, verified against code
- [Capability map](product/02-capability-map.md) — C1–C36, bucketed Now/Next/Later
- [Roadmap](product/03-roadmap.md) — M1–M6 milestone definitions

## Vision

ManManV2 lets a small operator run and manage third-party and custom game
servers across a fleet of bare-metal hosts from one control plane: spin up a
Minecraft, Steam, or custom game server; deploy configurable instances of it
across hosts; watch logs and status live; back up and restore save data; and
mod servers through a Steam-Workshop-style addon and library system — all
without hand-managing Docker on each box. A split-plane design keeps this
centrally controllable while letting host capacity scale independently: the
cloud control plane owns state and the API surface, bare-metal host managers
own Docker execution, and host-manager software updates itself in place via
App Registry promotion instead of manual SSH-and-restart.

## Personas

- **Admin** — keeps the platform up. Wants visibility into crashes and
  failures across the fleet, and configures everything through a simple UI.
  Technical, data-heavy.
- **Server Manager** — sets up new Docker images, deploys and restarts
  servers, manages backups/restores, and adds or removes commands. Still
  technical but less data-heavy than Admin; comfortable googling or using AI
  to work through unfamiliar problems.
- **Gamer** — not technical. Wants to sign in, check whether the server is
  up, and grab the connection IP. Occasionally runs a command or sends a
  console command, but never sets anything up.

## Load-bearing decisions

Derived from the Next/Later capability map against verified current state. This is a retrofit brief, so most of these are "codify the split that already exists and protect it going forward" rather than new choices — but each still has a real cost if the brief lets a near-term milestone erode it.

**LB1 — RabbitMQ topic-exchange wire contract is the fleet compatibility surface**
At risk: C9 (auto-recovery), C10 (self-updating host manager), C19 (downstream event subscribers), and every future host-manager capability — the fleet runs mixed host-manager versions during any rollout by construction (C10 exists specifically so hosts self-update independently, on their own poll cycle).
Decide now: `command.*` / `status.host.*` / `status.session.*` / `health.*` routing-key shapes and payload fields are additive-only from M1 forward — no renaming a field or repurposing a routing key, even for a "cheap" fix, without a version-tolerant rollout plan for hosts running an older payload contract.
Stays cheap: adding new message types, new fields with defaults, new consumers (C19) — none of that touches the existing contract.

**LB2 — Container/game direct-attach model is threaded through session lifecycle, not just log streaming**
At risk: C11 (log streaming/histogram), C5 (start/stop/stdin), any future non-Docker or wrapped-process execution model.
Decide now: crash detection (EOF on the attached stream), stdin forwarding, and stdout/stderr demuxing all assume a single live Docker-attach connection per container from M1 onward — a future execution backend (VM, wrapper process, remote agent) would need a parallel implementation of all three, not a config flag. The non-goals section already excludes non-Docker workloads, which is consistent with this — flagging it here so it's explicit that this isn't a "we'll generalize later" seam, it's a rewrite seam.
Stays cheap: new container *runtime options* (resource limits, network modes) within the Docker-attach model.

**LB3 — Label-based container identity is the sole orphan-recovery mechanism**
At risk: C9 (auto-recovery/orphan cleanup), and any future multi-tenant or multi-control-plane host-sharing scenario.
Decide now: `manman.type`/`manman.session_id`/`manman.sgc_id`/`manman.server_id` labels are the only source of truth host-manager restart recovery trusts (confirmed in `host/session/recovery.go`) — any future capability that lets two control planes or two ManManV2 deployments share a host would need a namespacing scheme in these labels from the start, not bolted on, since recovery logic pattern-matches on them directly.
Stays cheap: adding new labels for new metadata (e.g. a future org/tenant label) — the recovery logic reads specific keys, it doesn't reject unknown ones.

**LB4 — Env/config layering: which of three systems is canonical (blocks C22, C27, C32, and indirectly C28)**
At risk: C22 (per-deployment env overrides), C27 (port-binding template vars), C32 (session-level overrides) — all three sit directly on top of whichever config-layering mechanism wins, and C28 (GC-level workshop libraries) is explicitly framed in the design docs as "another instance of the same customization-layer model."
Decide now: `DESIGN_SGC_ENV_OVERRIDES.md` already made this decision (Option B: expand `ConfigurationStrategy`/`ConfigurationPatch`, not the flatter Option A `env_overrides` map, not a ground-up Option C). This brief should treat that as accepted and cite it, not re-derive it — the risk isn't the decision being wrong, it's a milestone quietly reintroducing Option A (a flat override map) under schedule pressure because it's "10 lines," which would produce a fourth env mechanism the design doc explicitly wrote Option B to avoid becoming.
Stays cheap: the convenience API surface on top (`GetEffectiveEnv`, `SetSGCEnvOverrides`) and session-level overrides (`session`-level patches) — the patch system already models the level, per the design doc's own open-questions section.

**LB5 — Public connect-address requires new state the host manager must originate, not just a new API field**
At risk: C20 (gamer connect-address lookup), C25 (simplified gamer view — same underlying need), C26 (aggregate rollups, if they end up address/reachability-scoped).
Decide now: no component in the system today tracks a container's externally-reachable `ip:port` (confirmed by grep — no such field exists anywhere in `manmanv2`). Whichever milestone builds C20 needs to decide *where this is sourced* — host-manager self-reported IP, an operator-configured per-host address, or something DNS/proxy-based — because that's a wire-contract and schema decision (new field on host or SGC/session status), not a UI decision, and the wrong choice (e.g., host self-reported private IP with no override) forecloses NAT/cloud-proxied deployments later.
Stays cheap: the UI surface for displaying it (copyable address chip, stripped gamer view) — genuinely cosmetic once the data exists.

**LB6 — Workshop library attachment level is a migration, not a toggle**
At risk: C28 (GC-level library inheritance).
Decide now: `workshop_installations` has `UNIQUE(sgc_id, addon_id)` today (migration `022`) — SGC-scoped, confirmed in code. Both design docs already agree the target is GC-level-inherited-plus-per-deployment-extras, so this is recorded as a load-bearing decision to *protect* — not to change now — but any interim SGC-scoped work (e.g. shipping C15/C16 fixes) should avoid adding new SGC-scoped uniqueness assumptions elsewhere that the eventual GC-level migration would also have to unwind.
Stays cheap: everything about *which* addons are in a library, library nesting, and the browse/search/install UI (C15) — none of that is keyed to the attachment level.

**LB7 — Auth mode is a single platform-wide toggle, not a per-component or per-persona policy**
At risk: C25 (simplified gamer view) and C17 (SSO) if a future milestone wants gamer-only auth to be optional while admin/server-manager auth stays required (e.g. anonymous "check status" access for gamers).
Decide now: `GRPC_AUTH_MODE`/`AUTH_MODE` is one value that every component (API, log-processor, host, UI) must match, confirmed in `ENV.md` and every component's `main.go` — there is no code path for per-route or per-persona auth policy today. If C25's "gamer just wants to check status" implies anonymous or lower-friction access than Admin/Server Manager get, that's a new authorization axis (route- or role-scoped, not just on/off), not a config toggle, and should be flagged as a design question before C25 is scoped rather than discovered mid-implementation.
Stays cheap: adding new OIDC roles/claims within the existing single-mode model (e.g., a `gamer` realm role gating UI sections) — that's authorization logic, not a wire-mode change.

**LB8 — Workshop cache must be addon-content-addressed, not SGC-scoped, with an explicit staleness field**
At risk: C35 (cross-host cache reuse), C36 (auto-refresh on source change) — and any future capability that reports per-addon freshness/version in the UI.
Decide now: today's download storage is keyed entirely by SGC (`getSGCHostDir`/`getSGCInternalDir`, `sgc-<id>`) — there is no addon-content identity anywhere in the host-manager storage path. M4 must introduce a cache key derived from addon content identity (steam_workshop_id + a version/timestamp SteamCMD or the Workshop API reports), independent of SGC/deployment/host, plus a field the API can compare against a periodic steamcmd-driven freshness check for C36. Per-SGC install becomes "copy/link from the cache into this SGC's directory," not "always download." Getting the key wrong (SGC-scoped, or unversioned) means a backfill/migration of every cached entry once staleness or cross-host reuse needs to disambiguate versions.
Stays cheap: the storage backend (shared NFS volume vs. S3 vs. a host-to-host peer-fetch protocol) and the concurrency/locking strategy for two hosts racing to populate the same key — both are implementation choices behind whatever interface the host manager uses to read/write the cache, swappable later without touching the addon-content key scheme or the schema recording cache state. In particular: if the key is content-addressed as above, a race between two hosts writing the same key is self-correcting (same content, idempotent overwrite) — no distributed lock is required at M4, that's a later optimization, not a correctness requirement.

## Non-goals

- Migrating existing duplicated GameConfigs automatically when per-deployment env overrides (C22) ship — that cleanup stays manual.
- A manmanv2-local UI component library — the UI reuses `libs/go/htmxui` primitives rather than re-deriving its own.
- Non-container, non-Docker game server workloads — the platform is built around bare-metal hosts running Docker; it does not target VM-based, serverless, or otherwise non-containerized execution.
- Revisiting host self-registration — it shipped, is stable, and is closed history. The design docs under `manmanv2/docs/ARCHIVE/` are reference material only, not an open work area.
