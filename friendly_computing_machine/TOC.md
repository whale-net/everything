# Friendly Computing Machine — TOC

Slack bot with Temporal workflows.

- [README.md](README.md) — Setup and overview
- [ARCHITECTURE.md](ARCHITECTURE.md) — System design: bot/task-pool/workflow-worker components, the OIDC identity-link web app and its two tables, the retained-but-possibly-empty Slack special channel route tables, and external integrations (whagent-net, Keycloak, ArgoCD).
- [ENV.md](ENV.md) — Every environment variable the code reads, per app: core, whagent-net, the OIDC web app's `FCM_*` settings, the shared `RABBITMQ_*` connection variables, and logging/telemetry.
- [PRODUCT.md](PRODUCT.md) — Product brief: vision, personas, load-bearing decisions, and a jump table to current state, capability map, and milestone roadmap. Read before scoping or designing any FCM milestone. Rendered by `krill/render`; do not hand-edit.
- [docs/poll.md](docs/poll.md) — Both poll features. Start here if you are diagnosing: how the weekly music poll is picked up, why the current week correctly has no response rows, how to tell a stuck poll from a healthy open one, and the timezone hazard. Also covers the `/wpoll` ad-hoc command.
- [docs/whagent_integration.md](docs/whagent_integration.md) — `@mention`-triggered whagent-net AI sessions: threaded turn flow, message queuing, channel setup, and the `slackchannelagentlink`/`slackthreadsession` tables
- [docs/deploy_recovery.md](docs/deploy_recovery.md) — Deploy ordering, why the `manmanstatusupdate` drop is a one-way door, why `helm rollback` after the V1 removal makes things worse rather than better, and the two actual recovery paths. Read this during a post-M1 incident.
- [docs/slack_tokens.md](docs/slack_tokens.md) — Slack token configuration
- [docs/argocd-integration.md](docs/argocd-integration.md) — ArgoCD event notifications
- [docs/microservices-architecture.md](docs/microservices-architecture.md) — The release apps, their CLI commands, and how they compose into the chart
