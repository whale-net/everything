# Deploying the App Registry — setup checklist

What to create, where, and in what order, to run `app-registry-api` with real
authentication and let CI talk to it.

Companion to [`libs/go/grpcauth/KEYCLOAK.md`](../../libs/go/grpcauth/KEYCLOAK.md),
which is the *how* — click-by-click Keycloak configuration and the gotchas.
This document is the *what for this service*: the exact objects, names, and
secrets.

> **Status: AR-3a.** The role model is merged and enforced on the recording
> RPCs. Promotion does not exist yet. Anything marked **⏳ later** is defined in
> code but enforces nothing until AR-3c/AR-3d land — safe to create now, but not
> yet load-bearing. See [PLAN.md](PLAN.md).

---

## Order of operations

Each step assumes the previous one. Step 4 is the one that changes behaviour;
everything before it is inert.

1. [Keycloak objects](#1-keycloak-objects)
2. [Verify a token by hand](#2-verify-a-token-by-hand) ← before touching the deployment
3. [Server configuration](#3-server-configuration)
4. [CI credentials](#4-ci-credentials) ← **read the warning first**
5. [Turn CI recording on](#5-turn-ci-recording-on)

---

## 1. Keycloak objects

Create these in one realm. The realm URL (`https://<host>/realms/<realm>`) is
your issuer. Follow
[KEYCLOAK.md §3–4](../../libs/go/grpcauth/KEYCLOAK.md#3-create-the-api-client-the-audience)
for the console steps — in particular the **audience mapper**, without which
every call returns `Unauthenticated`.

### Clients

| Client ID | Purpose | Config | Needed |
|---|---|---|---|
| `app-registry-api` | Names the audience. Never obtains a token. | Client authentication **On**, **all** flows unchecked, no roles | **now** |
| `app-registry-builder` | CI recording (`ReconcileApps`, `RecordBuild`, `RecordArtifact`) | Confidential, **Service accounts roles** only, realm role `app-registry-builder`, audience mapper → `app-registry-api` | **now** |
| `app-registry-admin` | `EnvironmentRegistry`, `SetAppStatus` | Same shape, realm role `app-registry-admin` | **now** |
| `app-registry-promoter-dev` | Promote to `dev` | Same shape, realm role `app-registry-promoter-dev` | ⏳ later |
| `app-registry-promoter-stage` | Promote to `stage` | Same shape, realm role `app-registry-promoter-stage` | ⏳ later |
| `app-registry-promoter-prod` | Promote to `prod` | Same shape, realm role `app-registry-promoter-prod` | ⏳ later |

### Realm roles

Create all five as **realm** roles, not client roles — `grpcauth` reads
`realm_access.roles` and never looks at `resource_access`. Names must match
`tools/app_registry/server/auth/auth.go` character for character:

```
app-registry-builder
app-registry-promoter-dev
app-registry-promoter-stage
app-registry-promoter-prod
app-registry-admin
```

**Roles are flat — no role implies another.** `app-registry-admin` does *not*
grant builder. A principal that needs to both record and administer holds both
roles. This is deliberate and pinned by a test.

### Humans

Assign roles via a **group**, not to individuals — e.g. a `registry-admins`
group holding `app-registry-admin`. Membership then becomes the thing you audit
and change.

---

## 2. Verify a token by hand

Do this **before** changing the deployment. It separates a Keycloak
misconfiguration from an application problem, and the two most common
mistakes are both invisible until you look at a decoded token.

```bash
TOKEN=$(curl -s -X POST \
  https://<host>/realms/<realm>/protocol/openid-connect/token \
  -d grant_type=client_credentials \
  -d client_id=app-registry-builder \
  -d client_secret=<secret> | jq -r .access_token)

echo "$TOKEN" | cut -d. -f2 | base64 -d 2>/dev/null | jq '{sub, aud, realm_access}'
```

Expected:

```json
{
  "sub": "…",
  "aud": ["app-registry-api", "account"],
  "realm_access": { "roles": ["app-registry-builder", "default-roles-…"] }
}
```

- `aud` missing `app-registry-api` → the audience mapper is missing. Every call
  will fail. See KEYCLOAK.md §4d.
- The role appears under `resource_access` instead of `realm_access` → you
  created a client role. Re-assign it as a realm role.

---

## 3. Server configuration

Set on the `app-registry-api` deployment (see [ENV.md](ENV.md)):

| Variable | Value |
|---|---|
| `GRPC_AUTH_MODE` | `oidc` |
| `GRPC_OIDC_ISSUER` | `https://<host>/realms/<realm>` — no trailing slash |
| `GRPC_OIDC_CLIENT_ID` | `app-registry-api` |

> ### ⚠️ `none` mode is wide open
>
> `GRPC_AUTH_MODE` defaults to `none`, and in `none` mode the server injects a
> fake principal holding **every app-registry role**. That is required for Tilt
> and local development to work at all, but it means **a deployed server that
> does not explicitly set `oidc` grants every caller full access, including
> `SetAppStatus`.**
>
> Treat setting `GRPC_AUTH_MODE=oidc` as part of deploying, not as a later
> hardening step.

The server resolves the issuer's discovery and JWKS endpoints **at startup**;
if Keycloak is unreachable then, the process exits. Confirm network policy
allows the pod to reach Keycloak before flipping the mode.

`app-registry-api` is an `external-api`, so it needs an ingress host set per
environment in a values override — no `ingress_host` is baked into the
manifest, deliberately.

---

## 4. CI credentials

> ### ⚠️ Read this before enabling `oidc`
>
> **`.github/workflows/release.yml` currently sets no `GRPC_AUTH_*` variables
> at all.** The AR-2c recording steps pass only `APP_REGISTRY_ADDRESS`, so the
> CLI runs in `none` mode and sends no credentials.
>
> The moment the server runs `oidc`, those calls fail `Unauthenticated`. They
> are `continue-on-error`, so **the release still succeeds and recording
> silently stops** — the failure mode is a registry that quietly goes stale, not
> a red build.
>
> Wiring the builder credential into `release.yml` is an outstanding task. Until
> it is done, either leave `APP_REGISTRY_CICD_OPT_IN` unset, or accept that
> recording is off while you exercise auth by hand with the CLI.

Client-side variables the CLI reads (see [ENV.md](ENV.md)):

| Variable | Value |
|---|---|
| `GRPC_AUTH_MODE` | `oidc` — must match the server |
| `GRPC_AUTH_TOKEN_URL` | `https://<host>/realms/<realm>/protocol/openid-connect/token` |
| `GRPC_AUTH_CLIENT_ID` | `app-registry-builder` |
| `GRPC_AUTH_CLIENT_SECRET` | that client's secret |

### Where each secret goes

This placement **is** the security control — it is what stops a build job from
promoting. Getting it wrong defeats the entire credential model.

| Secret | Location | Why |
|---|---|---|
| builder client secret | **Repository** secret | Every release job needs it; it grants recording only |
| `app-registry-promoter-prod` secret ⏳ | **Environment** secret on the `prod` GitHub Environment | Only a job declaring `environment: prod` can read it, and that declaration triggers the environment's required reviewers |
| `app-registry-promoter-stage` / `-dev` ⏳ | Environment secret on the matching Environment | Same, per environment |
| admin client secret | Not in GitHub | Human-operated; keep it out of CI entirely |

**Never put a promoter secret in a repository secret.** A repository secret is
readable by any workflow, which removes the boundary between building and
promoting — the one property the whole model exists to provide.

Configure **required reviewers** on the `prod` Environment. That approval, not
anything in the registry, is the human gate on promotion.

Repository *variables* (not secrets):

| Variable | Value |
|---|---|
| `APP_REGISTRY_ADDRESS` | the API's ingress host:port |
| `APP_REGISTRY_CICD_OPT_IN` | `true` to enable recording — see below |

---

## 5. Turn CI recording on

Set `APP_REGISTRY_CICD_OPT_IN=true` as a repository variable. With it unset —
the default, and how the repo ships — CI makes **no registry calls whatever**,
which is what lets the pipeline that builds and releases the registry run before
the registry exists.

Turn it on only once §4's warning is resolved, or recording will silently no-op.

Verify with a real release: the `Record build and artifact in App Registry` step
should run and succeed, and

```bash
app-registry apps list
```

should show the app. Recording steps are `continue-on-error`, so **check the
step's log rather than the job's status** — a failed recording still shows a
green job.

---

## Not settled yet

Do not build infrastructure around these; they may change in AR-3b/3c/3d.

- **Promoter roles enforce nothing.** `RequirePromoter` exists and is tested,
  but `PromotionRegistry` is `Unimplemented` until AR-3c. Creating the promoter
  clients now is harmless, just not yet meaningful.
- **`allowed_principals`** — ARCHITECTURE.md describes a per-environment
  narrowing beyond the role check. It lands in AR-3b/3c and may add a second
  condition the prod promoter must satisfy.
- **`promote.yml`** does not exist yet (AR-3d). The GitHub Environment names
  above are the intended design, not a shipped contract.
- **GitHub Actions OIDC** was considered and rejected for now in favour of
  Keycloak service accounts — see PLAN.md → AR-3 → "Credential model". Worth
  revisiting for secretless CI once this is proven.

## Related

- [`libs/go/grpcauth/KEYCLOAK.md`](../../libs/go/grpcauth/KEYCLOAK.md) — Keycloak configuration, step by step, and the reusable pattern
- [ENV.md](ENV.md) — every environment variable for the server, CLI, and migration
- [ARCHITECTURE.md](ARCHITECTURE.md) — the authorization model and why it splits where it does
- [PLAN.md](PLAN.md) — phase status and what is still to come
- [TESTING.md](TESTING.md) — running the registry locally in Tilt
