# whagent-net — TOC

Temporal-backed AI agent framework: session service, transcript store,
Temporal session workflows, MCP surface, and an embeddable session UI.

## Start Here

- [`README.md`](README.md) — What this domain is, current status (pre-M1,
  design only), the planned binaries and shared packages.
- [`ARCHITECTURE.md`](ARCHITECTURE.md) — Component map, the
  session/transcript/context split, transcript storage tiers (RMQ bus /
  Postgres hot / S3 cold), service-vs-package boundary, session workflow,
  tool contract for domain-owned MCP servers, embeddable UI model, auth
  chaining, idempotency, phasing, open items. Read before scoping or
  designing any whagent-net work.
- [`ENV.md`](ENV.md) — Environment variables. Skeleton until M1 lands
  binaries that read them.

## Product Docs

- [`PRODUCT.md`](PRODUCT.md) — Product brief: vision, personas, load-bearing
  decisions (LB1–LB7), non-goals, and a jump table to the current state,
  capability map (C1–C26), and milestone roadmap (M1–M3). Read before scoping
  or designing any whagent-net work; amended only via `/project-manager:product`.

## Related

- GitHub issue #1552 — the original manmanv2-scoped exploration this domain
  generalizes.
- [`audience_score_system/TOC.md`](../audience_score_system/TOC.md) —
  first consumer: existing Go MCP server (M1 tool target) and the M3
  embedded research-agent host.
- manmanv2 is a non-goal of this product (still idea phase; would need the
  `ControlClient` extraction from #1552 first).
