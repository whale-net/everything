# Roadmap

The `Now` bucket (C1–C19) is already shipped and live in production — see Capability map above. There is no "already shipped" milestone for it: milestones exist to carry work through design and build, and the roadmap ledger (comments on the tracking issue) tracks status transitions — `in design` → `planned` → shipped — that only mean something for work not yet started. C1–C19 need no such tracking; they're referenced here only as context for the milestones below. The roadmap starts at the first genuinely new milestone and covers six milestones: M1–M3, as originally scoped, deliver the `Next`-bucket capabilities C20–C24 and C33–C36; M4–M6, added after human review, deliver the remaining `Next`-bucket capabilities (C26, C28, C29, C30, C31 — moved up from `Later` now that each has a concrete milestone) covering Workshop management at fleet scale and the UI redesign.

### M1 — A gamer can check a server's status and grab its connect address without going through the operator UI

Delivers: C20
Must not foreclose: LB1, LB5
Deliberately deferred: full stripped-down gamer UI (C25 → Later), aggregate player-count/status rollups (C26 → M6)
FR budget: 10

**Address-sourcing decision (LB5):** M1 ships against an operator-configured public host/IP field — an Admin sets a per-host public address (host/IP, optionally per-port) once, and the API surfaces `host_public_address : session_port` as the connect address; no auto-detection of any kind. This is the simplest option that doesn't foreclose anything: it needs no host-manager wire changes beyond exposing the field, matches the Admin persona's existing "configures everything through a simple UI" job, and sidesteps the private-IP trap a host-self-reported address would hit for any NAT'd or cloud-proxied host. Host self-reported IP and proxy/DNS-based auto-detection are explicitly deferred — noted as a fancier alternative to revisit only if manual per-host configuration proves to be an operational burden at fleet scale.

### M2 — A Server Manager can start, stop, or restart a deployment in one click from the deployment list

Delivers: C21
Must not foreclose: LB1, LB2
Deliberately deferred: redesigned UI information architecture and Blade pattern (C31 → M5, M6)
FR budget: 8

### M3 — A Server Manager can configure a deployment's env overrides, console Actions, and ports entirely through the UI, without a seed script

Delivers: C22, C23, C24
Must not foreclose: LB1, LB4
Deliberately deferred: GC-level Workshop library inheritance (C28 → M6), session-level config overrides (C32 → Later), port-binding template variables (C27 → Later)
FR budget: 14

### M4 — A Server Manager can manage Workshop content at collection and fleet scale, with downloaded addons cached and kept fresh across every host

Delivers: C33, C34, C35, C36
Must not foreclose: LB1, LB6, LB8
Deliberately deferred: GC-level Workshop library inheritance (C28 → M6, an unrelated attachment-level schema change), port-binding template variables (C27 → Later)
FR budget: 12

**Cache design (LB8) and why LB6 is cited too:** M4 builds the addon-content-addressed shared download cache LB8 requires — key derived from `steam_workshop_id` plus a version/timestamp SteamCMD or the Workshop API reports, independent of SGC/deployment/host; storage backend and cross-host locking are swappable implementation details this milestone does not need to settle. LB6 is cited alongside LB8 even though M4 doesn't touch `workshop_installations`'/`sgc_workshop_libraries`'s SGC-scoped uniqueness directly — C33/C34 write into the library-scoped `workshop_library_addons` table (migration 023) instead, which has no SGC dimension. It's cited because LB6 already flagged the platform's move away from SGC-scoped identity as a direction to protect, and LB8 demands the same discipline for the cache: M4 must not quietly introduce a *new* SGC-scoped assumption in the cache layer while that broader migration is still pending.

### M5 — An Admin or Server Manager can find, inspect, and edit a deployment's settings, config, and status without leaving the Games/Dashboard/Activity surfaces, using the Blade pattern and deployment-first terminology (UI redesign phase 1)

Delivers: C31 (Dashboard/Games/Activity slice: flat-list expand-in-place Games page including a read-only Workshop Libraries panel, Blade layers for Deployment Settings and Config Editor basics/ports/env, deployment-first terminology throughout)
Must not foreclose: LB1, LB2, LB4
Deliberately deferred: Infrastructure page and drain/undrain (C29 → M6), Workshop top-level page reshaping including GC-level library attachment and collection/bulk management UI (C28, C33, C34 → M6), player counts and aggregate status rollups (C26 → M6), volume backup config inline in the Config Editor (C30 → M6), stripped-down Gamer UI (C25 → Later — a distinct persona-scoped surface pending its own LB7 design pass, not part of this IA rework)
FR budget: 16

**Phase split (M5 vs M6):** `DESIGN_UI_REDESIGN.md` and the wireframes don't define an explicit phase boundary — the decisions record the target IA and Blade pattern but not a build order. This split follows the design doc's own IA (Dashboard/Games/Activity/Infrastructure/Workshop) at its natural seam: Dashboard/Games/Activity are the primary day-to-day surface Server Manager and Admin already use for deployment work, and M1–M3 already shipped the backend capabilities those pages need (connect address, one-click start/stop, env overrides, Actions UI, ports) — nothing blocks building them now. Infrastructure (drain/undrain) and Workshop (GC-level libraries, bulk/collection management from M4) are lower-traffic admin surfaces whose own backend capabilities are still landing in M4 and Later, so they fall naturally into phase 2. Flagging this split explicitly for architect to sanity-check against the wireframes.

**LB7 dropped from Must-not-foreclose:** LB7 protects C25 (stripped gamer UI) and per-persona auth policy; M5's persona scope is Admin/Server Manager only, C25 stays deferred out of M5, and nothing in M5's Delivers text touches auth mode or reuses Blade components across a persona boundary. No M5-specific reason to cite it, so it's dropped rather than carried forward as a copy-paste artifact from M1–M4's citation pattern.

**Workshop Libraries panel (Games-page/M6 overlap — resolved):** M5's expand-in-place Games row ships all four sections `DESIGN_UI_REDESIGN.md` decision #5 specifies, including Workshop Libraries — but that panel is read-only in M5, rendered against today's still-SGC-scoped `sgc_workshop_libraries` data (already shipped, backing C15/C16), with a visible "coming soon: shared across configs" note. The real GC-level panel (edit-in-place, inherited-by-default) lands when M6 ships C28 and LB6's migration. This ships a complete Games page against its own cited design decision in M5, without pulling M6's schema work forward.

### M6 — An Admin can see fleet-wide status and drain a host, and a Server Manager can manage GC-level Workshop libraries and inline volume backup config, without leaving the Infrastructure/Workshop surfaces, using the same Blade pattern and terminology as M5 (UI redesign phase 2)

Delivers: C31 (Infrastructure/Workshop slice), C26, C28, C29, C30
Must not foreclose: LB1, LB6
Deliberately deferred: stripped-down Gamer UI (C25 → Later — a distinct persona-scoped surface pending its own LB7 design pass, not part of this IA rework)
FR budget: 18 (revised up from 14 — this milestone now covers a schema migration off SGC-scoped uniqueness, not just new UI, on top of four Delivers items)

**Library-attachment migration (LB6 — resolved):** M6's C28 work retires the SGC-scoped `workshop_installations`/`sgc_workshop_libraries` uniqueness (migrations `022`/`024`); it does not add a parallel GC-level table alongside it. Existing SGC-scoped library attachments are migrated to their owning GameConfig as part of this work, not left for a manual re-attach per deployment. Per-deployment one-off addon installs (C15's addon-level `workshop_installations` rows, not the library-attachment rows) are unaffected and stay SGC-scoped by design — a deployment can still layer one-off addons on top of its inherited library. Leaving the old SGC-scoped uniqueness live alongside a new GC-level table would satisfy the UI screen but not LB6's actual intent: an unmigrated SGC-scoped assumption sitting unused isn't retired, it's dead weight the next schema change trips over.
