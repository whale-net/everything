# Deploying the App Registry — setup checklist

What to create, where, and in what order, to run `app-registry-api` with real
authentication and let CI talk to it.

Companion to [`libs/go/grpcauth/KEYCLOAK.md`](../../libs/go/grpcauth/KEYCLOAK.md),
which is the *how* — click-by-click Keycloak configuration and the gotchas.
This document is the *what for this service*: the exact objects, names, and
secrets.

> **Status: AR-3d.** The role model, `PromotionRegistry`, its CLI commands, and
> `promote.yml` are all merged. Promoter clients (marked ⏳ below) are safe to
> create now and are load-bearing the moment `promote.yml` runs against a
> `oidc`-mode server. See [PLAN.md](PLAN.md).

---

## Order of operations

Each step assumes the previous one. Step 4 is the one that changes behaviour;
everything before it is inert.

1. [Keycloak objects](#1-keycloak-objects)
2. [Verify a token by hand](#2-verify-a-token-by-hand) ← before touching the deployment
3. [Server configuration](#3-server-configuration)
4. [CI credentials](#4-ci-credentials)
5. [Turn CI recording on](#5-turn-ci-recording-on)
6. [Promote via `promote.yml`](#6-promote-via-promoteyml)

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

> **Do not split `app-registry-builder` per environment.** Unlike the promoter
> clients below, recording (`RecordBuild`/`RecordArtifact`) has no
> environment concept — `release.yml` runs it once per release, before any
> promotion decision exists, against the single `APP_REGISTRY_ADDRESS`. There
> is exactly one `app-registry-builder` client and one `app-registry-builder`
> realm role (`server/auth/auth.go`'s `RoleBuilder`); creating
> `app-registry-builder-dev` / `-prod` variants has no corresponding code path
> to select between them and will just leave `release.yml`'s
> `GRPC_AUTH_CLIENT_ID: app-registry-builder` unable to authenticate. If a
> per-environment split is ever needed here, it requires a code change in
> this repo (an environment input to the recording steps) first — do not
> introduce the split Keycloak-side alone.
| `app-registry-admin` | `EnvironmentRegistry`, `SetAppStatus` | Same shape, realm role `app-registry-admin` | **now** |
| `app-registry-promoter-dev` | Promote to `dev` (via `promote.yml`) | Same shape, realm role `app-registry-promoter-dev` | **now** — needed before `promote.yml` can run against `dev` |
| `app-registry-promoter-stage` | Promote to `stage` (via `promote.yml`) | Same shape, realm role `app-registry-promoter-stage` | **now** — needed before `promote.yml` can run against `stage` |
| `app-registry-promoter-prod` | Promote to `prod` (via `promote.yml`) | Same shape, realm role `app-registry-promoter-prod` | **now** — needed before `promote.yml` can run against `prod` |
| `app-registry-worker` | The writeback worker's own calls back into the API (`GetEnvironmentState`) | Confidential, **Service accounts roles** only, audience mapper → `app-registry-api`. **No realm role** — those reads only require an authenticated caller | **now, if you run the worker** |

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

The `app-registry-worker` client deliberately gets **no** realm role: it only
reads (`GetEnvironmentState`), and the read RPCs require an authenticated
caller rather than a specific role. Give it a role only if you later have it
write.

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

> ### Resolved in AR-3d
>
> Earlier versions of this doc warned that `.github/workflows/release.yml` set
> no `GRPC_AUTH_*` variables, so the moment the server ran `oidc` the AR-2c
> recording steps would fail `Unauthenticated` while `continue-on-error` hid
> it — a release staying green while recording silently went stale.
>
> The `Record build and artifact in App Registry` and `Record chart artifacts
> in App Registry` steps now set `GRPC_AUTH_MODE: oidc` and read the builder
> client's credentials from the repository secret/variable named below. Their
> `continue-on-error` and `APP_REGISTRY_CICD_OPT_IN` gating are unchanged —
> recording is still best-effort and still off by default. What changed is
> that when it *is* on, it can now actually authenticate once the server runs
> `oidc`.
>
> `promote.yml` (§5 below) exists as of AR-3d too, reading each environment's
> promoter secret the same way.

Client-side variables the CLI reads (see [ENV.md](ENV.md)):

| Variable | Value |
|---|---|
| `GRPC_AUTH_MODE` | `oidc` — must match the server |
| `GRPC_AUTH_TOKEN_URL` | `https://<host>/realms/<realm>/protocol/openid-connect/token` |
| `GRPC_AUTH_CLIENT_ID` | `app-registry-builder` (recording) or `app-registry-promoter-<environment>` (promotion) |
| `GRPC_AUTH_CLIENT_SECRET` | that client's secret |

### Where each secret goes

This placement **is** the security control — it is what stops a build job from
promoting. Getting it wrong defeats the entire credential model.

| Secret | Location | Why |
|---|---|---|
| builder client secret (`APP_REGISTRY_BUILDER_CLIENT_SECRET`) | **Repository** secret | Every release job needs it; it grants recording only — wired into `release.yml`'s two recording steps |
| `app-registry-promoter-prod` secret (`APP_REGISTRY_PROMOTER_CLIENT_SECRET`) | **Environment** secret on the `prod` GitHub Environment | Only a job declaring `environment: prod` can read it, and that declaration triggers the environment's required reviewers — `promote.yml` declares `environment: ${{ inputs.environment }}` for exactly this reason |
| `app-registry-promoter-stage` / `-dev` (`APP_REGISTRY_PROMOTER_CLIENT_SECRET`) | Environment secret on the matching Environment, same secret name, different Environment | Same, per environment — the Environment scoping is what selects the right client, not the secret name |
| admin client secret | Not in GitHub | Human-operated; keep it out of CI entirely |

**Never put a promoter secret in a repository secret.** A repository secret is
readable by any workflow, which removes the boundary between building and
promoting — the one property the whole model exists to provide.

Configure **required reviewers** on the `prod` Environment (and `stage` if
desired). That approval, not anything in the registry, is the human gate on
promotion. Create three GitHub Environments named exactly `dev`, `stage`,
`prod` — matching the `environment` keys seeded by AR-3b and the promoter
client names above — each holding its own
`APP_REGISTRY_PROMOTER_CLIENT_SECRET`.

Repository *variables* (not secrets):

| Variable | Value |
|---|---|
| `APP_REGISTRY_ADDRESS` | the API's ingress host:port |
| `APP_REGISTRY_AUTH_TOKEN_URL` | the Keycloak token endpoint, e.g. `https://<host>/realms/<realm>/protocol/openid-connect/token` — same for every client, so it is a variable, not a secret |
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

## 6. Promote via `promote.yml`

`.github/workflows/promote.yml` is a `workflow_dispatch` job with inputs
`environment`, `registry_environment`, `action` (`promote`/`rollback`),
`owner_full_name`, `version`, `reason`, `allow_override`, `dry_run`. Its job
declares `environment: ${{ inputs.environment }}`, which is what scopes it to
that GitHub Environment's `APP_REGISTRY_PROMOTER_CLIENT_SECRET` and triggers
that Environment's required reviewers — see §4's secret table. Unlike the
AR-2c recording steps it is **not** `continue-on-error`: a failed promotion
fails the run.

`environment` and `registry_environment` are deliberately two separate
inputs, not one string doing double duty:

- `environment` is the **GitHub Environment name**, whatever this repo's
  Environments actually happen to be called (e.g. `promotion-dev`,
  `promotion-prod`). It only has to match a real GitHub Environment.
- `registry_environment` is the **App Registry environment key** — it drives
  both `GRPC_AUTH_CLIENT_ID: app-registry-promoter-${{
  inputs.registry_environment }}` and the CLI's `--env` flag, and must match
  one of `dev`/`stage`/`prod` per §1's client table, independent of what the
  GitHub Environment is named.

Pick both correctly for the target: e.g. `environment: promotion-prod`,
`registry_environment: prod`. A mismatch (say `registry_environment: dev`
under `environment: promotion-prod`) doesn't bypass anything — it just fails
Keycloak authentication, because that GitHub Environment's secret is the
`prod` client's secret, which does not pair with the `dev` client id.

To promote to `prod`: create the corresponding GitHub Environment (§1's
client table and §4's secret table), configure required reviewers on it, run
the workflow with that `environment` and `registry_environment: prod`, and
approve the run when prompted.

---

## Not settled yet

- **`allowed_principals`** — ARCHITECTURE.md describes a per-environment
  narrowing beyond the role check. It lands alongside `EnvironmentRegistry`
  admin tooling and may add a second condition the prod promoter must
  satisfy.
- **GitHub Actions OIDC** was considered and rejected for now in favour of
  Keycloak service accounts — see PLAN.md → AR-3 → "Credential model". Worth
  revisiting for secretless CI once this is proven.

## Related

- [`libs/go/grpcauth/KEYCLOAK.md`](../../libs/go/grpcauth/KEYCLOAK.md) — Keycloak configuration, step by step, and the reusable pattern
- [ENV.md](ENV.md) — every environment variable for the server, CLI, and migration
- [ARCHITECTURE.md](ARCHITECTURE.md) — the authorization model and why it splits where it does
- [PLAN.md](PLAN.md) — phase status and what is still to come
- [TESTING.md](TESTING.md) — running the registry locally in Tilt
