# Authorization

Enforced with `libs/go/grpcauth` (Keycloak-issued OIDC service accounts, per
[`KEYCLOAK.md`](../../libs/go/grpcauth/KEYCLOAK.md)), split along the service
boundary. Role names are realm roles in Keycloak, service-prefixed, enforced
in `server/auth`:

| Role | Services | Credential |
|---|---|---|
| `app-registry-builder` | `AppRegistry` (writes), `ArtifactRegistry` (writes except `AdoptArtifact`) | Keycloak service account, CI/all workflows |
| `app-registry-promoter-dev` | `PromotionRegistry` (writes), `dev` only | Keycloak service account scoped to the `dev` GitHub Environment; humans |
| `app-registry-promoter-stage` | `PromotionRegistry` (writes), `stage` only | Keycloak service account scoped to the `stage` GitHub Environment; humans |
| `app-registry-promoter-prod` | `PromotionRegistry` (writes), `prod` only | Keycloak service account scoped to the `prod` GitHub Environment; a small human group |
| `app-registry-admin` | `EnvironmentRegistry` (writes), `SetAppStatus`, `AdoptArtifact` (AR-7e) | Human only |
| *(public / anonymous)* | All read RPCs (`GetApp`, `ListApps`, `ListCharts`, `GetArtifact`, `ListArtifacts`, `ResolveArtifact`, `ListArtifactPins`, `CheckChartHermeticity`, `GetEnvironmentState`, `ListPromotions`, `ListPromotionEvents`, `GetEnvironment`, `ListEnvironments`, `GetReleaseRun`, `ListBuilds`, `ListReconcileRuns`) | None (anonymous access permitted) |

Roles are flat and explicit — `app-registry-admin` does not imply
`app-registry-builder` or any promoter role, and a promoter role for one
environment does not imply another. A principal must hold literally every
role a call requires; see `server/auth.Require`'s doc comment.

**Public Read Paths (#853):**
Similar to container registries and Helm chart repositories, read-only discovery
and environment state queries are unauthenticated. CI runners (e.g. `download-release-tools`),
local developer CLI commands, and deployment tools can query active versions,
metadata, and promotion history without provisioning Keycloak credentials. Mutation,
lifecycle state transitions, and promotion remain strictly authenticated and role-gated.

**The critical constraint:** the credential every build job already holds must
not be able to promote. Each identity is a *different Keycloak client*, so the
builder token simply does not carry a promoter role — no matter what a
compromised build job does with the secret it holds. **Environment scoping
comes from GitHub, not Keycloak:** the `app-registry-promoter-prod` client
secret lives on the GitHub Environment named `prod`, so only a workflow job
declaring `environment: prod` can read it, and that declaration is what
triggers required reviewers. Per-environment `allowed_principals` narrows this
further.

`reason` is required on promotions to any environment above rank 0.

