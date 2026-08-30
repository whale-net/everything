# Capability map

### Now

C1 — Admin can create, update, delete, and list Games in the catalog (title, Steam app metadata).
C2 — Server Manager can create, update, delete, and list GameConfigs (image, args/env templates, volumes), including third-party Docker Hub images.
C3 — Server Manager can deploy a GameConfig to a host, producing a Deployment, and update or delete that deployment.
C4 — Server Manager can validate a deployment's configuration before or while deploying it.
C5 — Server Manager can start and stop a session (a run of a deployed config) and send raw stdin to the running game process.
C6 — Gamer or Server Manager can run a defined Action (console command) against a running session.
C7 — Admin can define reusable Actions and see which ones are available for a given session.
C8 — Admin can view and manage host machines that have self-registered, or manually pre-register one, and list, inspect, update, or delete any of them.
C9 — Admin can rely on a host manager to auto-recover orphaned game containers after a restart and clean up untracked containers on its own.
C10 — Admin can get host manager software updated automatically via App Registry promotion, with no manual SSH or restart.
C11 — Admin or Server Manager can stream a session's live logs and query historical logs and a log histogram.
C12 — Server Manager can trigger, list, get, and delete backups of a session's save data, and manage reusable BackupConfigs.
C13 — Server Manager can manage a GameConfig's volumes, typed for backup relevance.
C14 — Admin can define ConfigurationStrategies and layered ConfigurationPatches, and preview the effective rendered configuration for a session.
C15 — Server Manager can browse and search Workshop addons, fetch addon metadata, and install, reset, or remove addon installations on a deployment.
C16 — Server Manager can manage Workshop libraries (curated addon collections), nest libraries, and attach a library to a deployment so all its addons install together.
C17 — Admin, Server Manager, or Gamer can sign in once via platform SSO (JWT/OIDC through Keycloak), togglable per environment.
C18 — Admin, Server Manager, or Gamer can pick a light, night, or OLED UI theme, and have the choice persist.
C19 — Admin can let downstream services (Slack bot, monitoring, audit log) subscribe to host/session lifecycle events on the shared exchange.

### Next

C20 — Gamer can see whether a server is up and grab its public connect address (`ip:port`), without going through the full operator UI.
C21 — Server Manager can start, stop, or restart a deployment in one click from the deployment list, instead of today's multi-step flow.
C22 — Server Manager can set per-deployment environment variable overrides, without forking the underlying GameConfig.
C23 — Server Manager can add, edit, or remove console command Actions through the UI, without needing a seed script.
C24 — Server Manager can edit a GameConfig's ports through the UI/API, without needing a seed script.
C26 — Admin can see player counts and aggregate game status rollups across the fleet.
C28 — Server Manager can attach a Workshop library at the GameConfig level so every deployment of that config inherits it.
C29 — Admin can drain and undrain a host (stop new deployments from landing there without deleting it).
C30 — Server Manager can see and edit a volume's backup config inline in the config editor, instead of a separate BackupConfig-management surface.
C31 — Admin or Server Manager can navigate a redesigned UI information architecture (Dashboard/Games/Activity/Infrastructure/Workshop) using the "Blade" editing pattern and deployment-first terminology.
C33 — Server Manager can add a Steam Workshop collection to a library in one action, with every item in the collection added automatically instead of one at a time.
C34 — Server Manager can create multiple Workshop addons in a single action (e.g. a pasted batch of Workshop IDs), instead of creating each one individually.
C35 — Server Manager can install a Workshop addon on any host without re-downloading it from Steam if another host in the fleet already has it cached.
C36 — Server Manager can trust that a cached Workshop addon is refreshed automatically when its Steam Workshop source has changed, instead of silently serving stale content.

### Later

C25 — Gamer can use a stripped-down, simplified view of the UI scoped to their job (sign in, check status, get the connect address, run a command), separate from the Admin/Server Manager surface.
C27 — Server Manager can use port-binding template variables inside env values.
C32 — Server Manager can override configuration at the session level, not just the deployment level.
