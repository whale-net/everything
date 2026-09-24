# Friendly Computing Machine — TOC

Slack bot with Temporal workflows.

- [README.md](README.md) — Setup and overview
- [ARCHITECTURE.md](ARCHITECTURE.md) — System design: bot/task-pool/workflow-worker components, the OIDC identity-link web app and its two tables, and external integrations (whagent-net, ManMan, Keycloak, ArgoCD).
- [ENV.md](ENV.md) — All environment variables, including the OIDC web app's `FCM_*` settings and the required Keycloak client config.
- [PRODUCT.md](PRODUCT.md) — Product brief: vision, personas, load-bearing decisions, and a jump table to current state, capability map, and milestone roadmap. Read before scoping or designing any FCM milestone.
- [docs/poll.md](docs/poll.md) — `/wpoll` ad-hoc polls: usage, vote rules, Socket Mode flow, and the `poll`/`polloption`/`pollvote` tables
- [docs/whagent_integration.md](docs/whagent_integration.md) — `@mention`-triggered whagent-net AI sessions: threaded turn flow, message queuing, channel setup, and the `slackchannelagentlink`/`slackthreadsession` tables
- [docs/slack_tokens.md](docs/slack_tokens.md) — Slack token configuration
- [docs/manman_subscribe.md](docs/manman_subscribe.md) — ManMan subscribe integration
- [docs/argocd-integration.md](docs/argocd-integration.md) — ArgoCD event notifications
- [docs/microservices-architecture.md](docs/microservices-architecture.md) — Service architecture
