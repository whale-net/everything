# Release Registry — Architecture

## Overview

The release registry is a gRPC service that tracks application metadata, git commits, container artifacts, and environment promotions. It acts as the single source of truth for "which artifact version runs in which environment" so downstream consumers (ArgoCD plugin adapter, dashboard API) can call `Resolve()` to discover the active release without re-evaluating build pipelines.

## Components

```
┌─────────────────────┐     ┌───────────────────┐
│  ArgoCD Plugin      │    │  Dashboard / CLI   │
│  (internal K8s)     │    │  (external calls)  │
└──────────┬──────────┘    └──────────┬──────────┘
           │ Resolve()                │ Resolve()
           ▼                          ▼
┌─────────────────────────────────────▼──────────────┐
│              release_registry API (:50054)          │
│                                                     │
│  Auth Interceptor — OAuth2 client-credentials       │
│  (Register / Promote gated; Resolve open)           │
│                                                     │
│  ┌─────────┐  ┌──────────┐  ┌────────────┐        │
│  │ Register│  │Promote   │  │ Resolve    │        │
│  │ Service │  │ Service  │  │ Service    │        │
│  └────┬────┘  └────┬─────┘  └──────┬─────┘        │
│       │            │               │                │
│       └────────────┴───────────────┘                │
│                         │                            │
│                  libs/go/db (pgxpool)                │
│                         │                            │
│                   PostgreSQL                         │
│              (apps, commits, artifacts,               │
│               promotions — SCD2 on promotion_log)    │
└─────────────────────────────────────────────────────┘
```

---

## Data Model

### Entities

| Table | Purpose | Notes |
|-------|---------|-------|
| `apps` | Application identity and container registry info | Canonical key = `<domain>-<name>` |
| `commits` | Git commit records (repo, sha, ref, timestamp) | Indexed on `(repo, sha)` |
| `artifacts` | App-version pairs with kind (IMAGE / HELM_CHART) | Version is monotonically increasing per app+kind |
| `promotion_log` | Promotion history — SCD2 shaped via `valid_from` / `valid_to` | `valid_to IS NULL` = active promotion for env |

### Promotion Model

Promotions follow the repo's SCD2 convention:

```sql
-- Close previous promotion for this app+env+kind
UPDATE promotion_log SET valid_to = NOW() WHERE app = $1 AND env = $2 AND kind = $3 AND valid_to IS NULL;
-- Open new one
INSERT INTO promotion_log (app, env, kind, version, sha) VALUES ($1, $2, $3, $4, $5);

-- Active resolution
SELECT * FROM promotion_log
WHERE app = $1 AND env = $2 AND kind = $3
  AND valid_from <= NOW()
  AND (valid_to IS NULL OR valid_to > NOW());
```

---

## RPC Surface

| RPC | Auth | Description |
|-----|------|-------------|
| `RegisterApp` | Service account | Register an app's metadata and container registry info |
| `RegisterCommit` | Gated on `github.event_name == 'push'` | Record a new commit; used by CI webhook or CLI wrapper |
| `RegisterArtifact` | Service account | Link a built artifact (image / helm chart) to a commit and version |
| `Promote` | Service account | Promote a version to an environment; SCD2 close+open on promotion_log |
| `Resolve` | Open (no auth) | Return the active artifact for app + env + kind |

---

## Deployment Pattern

The registry service runs as a K8s deployment in the infra namespace:

- **Image**: Built from `//release_registry/api` (`go_binary` → `image`)
- **Container Port**: 50054 (gRPC)
- **ConfigMap/Secret**: DB connection string, Keycloak OIDC settings
- **Sidecar / Plugin Adapter**: Optional ArgoCD plugin adapter container in the same pod for internal K8s communication

---

## Design Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| gRPC over REST | gRPC + protobuf | Consistent with manmanv2 API; efficient for Resolve hot path |
| Timestamp format | `int64` Unix seconds | Matches existing manmanv2 proto convention; avoids well-known type import |
| Promotion history | SCD2 table (`valid_from`/`valid_to`) | Enables audit trail and "history at time T" queries without extra logic |
| Auth gating | Resolve open, Register/Promote require service account | Consumers shouldn't need credentials; writers must be authenticated |
| Versioning | Monotonic integer per app+kind | Simple semantic version derived from artifact registration order |

---

## Directory Structure

```
//release_registry/
├── protos/
│   ├── registry.proto        # Service definition + messages
│   └── BUILD.bazel           # go_proto_library + proto_library
├── api/                      # TBD — gRPC server implementation
│   ├── main.go               # Binary entry point
│   ├── server.go             # Registry service impl + interceptors
│   └── BUILD.bazel
├── cli/                      # TBD — CLI wrapper (register, resolve, promote)
│   ├── cmd/                  # Cobra commands
│   └── BUILD.bazel
└── BUILD.bazel               # Root BUILD; :protos library target
```

---

## References

- manmanv2 proto convention: `//manmanv2/protos/api.proto`
- gRPC auth interceptor: `//libs/go/grpcauth/README.md`
- DB connection pool: `//libs/go/db/README.md`
- Prometheus metrics: `//release_registry/api/metrics.go` (TBD)
