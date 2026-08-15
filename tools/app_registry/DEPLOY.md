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

1. [Keycloak objects](#1-keycloak-objects) — interactive client, mappers, service accounts, roles, human assignments
2. [Verify tokens by hand](#2-verify-tokens-by-hand) ← before touching the deployment (UI + service accounts)
3. [Server configuration](#3-server-configuration) — API + UI env vars
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

Create the interactive client first so you can verify its tokens by hand (see §2).

#### Interactive UI client — `app-registry-ui`

This is a normal login client: humans authenticate through it with a browser. It is **confidential** (`client_secret` set), has Standard flow enabled, and carries an audience mapper so its access tokens are accepted by the API.

1. **Clients** → **Create client**
2. **Client ID**: `app-registry-ui` → Next
3. **Authentication flow**: check **Standard flow** (authorization-code) — leave **Direct access grants**, **Implicit flow** unchecked. Check **Service accounts roles** so the admin console shows a dedicated client scope; you do not need to assign anything to its service account yet.
4. **Client authentication**: On ← generates a secret needed for PKCE and token refresh. → Next
5. **Redirect URIs**:

   | Environment | Redirect URI |
   |---|---|---|
   | Local / Tilt dev server | `http://localhost:8000/auth/callback` |
   | Dev cluster (ingress) | `https://<dev-ingress-host>/auth/callback` |
   | Stage cluster (ingress) | `https://<stage-ingress-host>/auth/callback` |
   | Prod cluster (ingress) | `https://<prod-ingress-host>/auth/callback` |

6. Leave **Root URL**, **Web origins** blank for now — add them if your OIDC library validates CORS origins later. → **Save**.

##### Protocol mappers (both required on the dedicated scope `app-registry-ui-dedicated`)

Go to **Client scopes** tab → click `app-registry-ui-dedicated` → **Add mapper** → **By configuration**:

| Mapper | Type | Purpose | Failure if omitted |
|---|---|---|---|
| `api-audience` | **Audience** — adds `aud` claim to token | `libs/go/grpcauth`'s verifier checks `aud == clientID` (`libs/go/grpcauth/KEYCLOAK.md` §3-4). This is the same audience name as the `app-registry-api` client. | Every gRPC call from the UI fails with `Unauthenticated` — the token's `aud` stays `["account"]` and the API rejects it immediately. |
| | **Included Client Audience** | `app-registry-api` |
| | **Add to access token** | On |
| `realm-roles` | **Realm roles** mapper with "Add to ID token" enabled | The UI's auth layer (`libs/go/htmxauth/auth.go:383`, `db_session.go:80`) reads claims from the **ID token**, not the access token. Keycloak's built-in realm-roles mapper targets the **access token** by default, so this custom one is required to surface roles in the ID token. | `realm_access` is simply absent from the ID token; every principal resolves to zero roles and every control disappears silently (the UI renders as empty/gray rather than broken). |
| | **Add to** | ID token |

##### Client secret

After saving, go to the **Credentials** tab → copy **Client secret**. Store it in environment variables for your deployment (see §3b below) — do not commit it.

#### Service-account clients (repeat-per-identity pattern from KEYCLOAK.md)

| Client ID | Purpose | Config | Needed |
|---|---|---|---|
| `app-registry-api` | Names the audience. Never obtains a token. | Client authentication **On**, **all** flows unchecked, no roles | **now** |
| `app-registry-builder-dev` / `app-registry-builder-prod` | CI recording (`ReconcileApps`, `RecordBuild`, `RecordArtifact`) | Confidential, **Service accounts roles** only, both assigned the single `app-registry-builder` realm role, audience mapper → `app-registry-api` | **now** |

> **Builder clients are environment-scoped, like the promoter clients below**
> (issue #539). `release.yml`'s recording steps set
> `GRPC_AUTH_CLIENT_ID: app-registry-builder-${{ vars.APP_REGISTRY_BUILDER_ENV || 'dev' }}`
> — `APP_REGISTRY_BUILDER_ENV` is a repository variable selecting which
> builder identity CI authenticates as. Only `dev` is wired up in CI today
> (hence the fallback); set the repository variable and provision the
> matching realm role/client before pointing `APP_REGISTRY_ADDRESS` at a prod
> registry.
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

### Assigning roles to human users (the five existing realm roles)

The five roles from §1's role table (`app-registry-builder`,
`app-registry-promoter-dev`, `app-registry-promoter-stage`,
`app-registry-promoter-prod`, `app-registry-admin`) must be **assignable to**
and **assigned to** human users — not only service accounts. No new roles are
needed; you reuse exactly the ones already listed above.

#### Via admin console (one-time setup)

1. Go to **Clients** → `app-registry-ui` → **Role mapping** tab (the dedicated
   scope does *not* have a role-mapping UI — only the client itself does).
2. Click **Add assigned roles** → switch the filter dropdown to **Filter by realm
   roles** ← easy to miss; it defaults to client roles, which are invisible to
   `grpcauth` and `htmxauth`.
3. Tick all five role names:

```
app-registry-builder
app-registry-promoter-dev
app-registry-promoter-stage
app-registry-promoter-prod
app-registry-admin
```

4. Click **Assign**. This step is purely a console UI quirk — it registers the
   realm roles as "available assigned roles" so they can be mapped to users and
   groups later. After this one-time setup, every subsequent assignment only
   requires steps 5–7 below.

#### Assigning individual humans

1. **Users** → select user → **Role mapping** tab.
2. Click **Add selected roles**.
3. Filter by realm roles → tick the role(s) to assign → **Assign**.

#### Assigning via group (preferred for teams)

1. **Groups** → **Create group**, e.g. `app-registry-admins`.
2. The group's **Role mapping** tab → add selected roles (filter by realm roles) → tick the roles → **Assign**.
3. Add members to the group. Membership changes require no role edits, and the
   group is what you audit.

#### Deriving the promoter role name from an environment key

The promoter role for a given environment is derived as
`app-registry-promoter-<environment_key>`, where `<environment_key>` is one of
the lowercase strings `dev`, `stage`, or `prod`. This matches the constant
`RolePromoterDev`, `RolePromoterStage`, and `RolePromoterProd` in
`tools/app_registry/server/auth/auth.go`:

```go
// tools/app_registry/server/auth/auth.go
const RolePromoterDev = "app-registry-promoter-dev"
const RolePromoterStage = "app-registry-promoter-stage"
const RolePromoterProd = "app-registry-promoter-prod"
```

The `RequirePromoter` helper in the same file does exactly this concatenation:

```go
func RequirePromoter(ctx context.Context, env string) error {
    return Require(ctx, "app-registry-promoter-"+env)
}
```

If an admin creates a new environment through the UI whose key is not one of
`dev`, `stage`, or `prod`, **no corresponding role exists yet** and no human can
promote to it. The admin must create the matching realm role in Keycloak before
any promotion attempt will succeed — this is by design: roles are added only when
the team has formalized who holds them.

> **Note on `app-registry-builder` for humans.** A human user needs this role if
> they run the CLI recording commands (e.g. `app-registry builds record`). In most
> teams the builder credential is reserved for CI; granting it to a person is fine
> but gives them write access to all apps, not just one environment's promotions.

---

## 2. Verify tokens by hand

### UI client — interactive login ID token

After creating the `app-registry-ui` client and its two mappers, verify the user can complete an authorization-code (or password) login:

```bash
TOKEN=$(curl -s -X POST \
  https://<host>/realms/<realm>/protocol/openid-connect/token \
  -d grant_type=password \
  -d username=<human-user> \
  -d password=<password> \
  -d client_id=app-registry-ui \
  -d client_secret=<secret> | jq -r .id_token)

echo "$TOKEN" | cut -d. -f2 | base64 -d 2>/dev/null | jq '{sub, aud, realm_access}'
```

Expected:

```json
{
  "sub": "...",
  "aud": ["app-registry-api"],               ← audience mapper worked (access tokens only)
  "realm_access": { "roles": ["default-roles-...", ...] }  ← realm-roles mapper worked (ID token)
}
```

Key differences from the service-account check below:
- The `aud` claim contains only `["app-registry-api"]` — no `"account"` — because an interactive login does not use the account service.
- `realm_access.roles` must be present in this **ID token** for the HTMX session to work. If it is missing, the realm-roles mapper's "Add to ID token" setting is off or the custom mapper is on the wrong client scope.

### Service-account tokens — `client_credentials` (repeat from original §2)

```bash
TOKEN=$(curl -s -X POST \
  https://<host>/realms/<realm>/protocol/openid-connect/token \
  -d grant_type=client_credentials \
  -d client_id=app-registry-builder-dev \
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
- The role is absent from both → it was never assigned to this client's
  service account.

All three of the above surface identically at call time — `rpc error: code =
PermissionDenied desc = requires role "<role>"` — so decoding the token is
what tells you which one you're looking at (see issue #602).

The three promoter clients (`app-registry-promoter-dev/-stage/-prod`) are
provisioned independently, so a fix to one says nothing about the others.
Check all of them in one pass:

```bash
for env in dev stage prod; do
  echo "=== app-registry-promoter-$env ==="
  TOKEN=$(curl -s -X POST \
    https://<host>/realms/<realm>/protocol/openid-connect/token \
    -d grant_type=client_credentials \
    -d client_id=app-registry-promoter-$env \
    -d client_secret=<that environment's secret> | jq -r .access_token)
  echo "$TOKEN" | cut -d. -f2 | base64 -d 2>/dev/null | jq '{realm_access, resource_access}'
done
```

Expect `realm_access.roles` to contain `app-registry-promoter-<env>` for each.

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

#### UI client server configuration (§3b)

Set on the UI deployment (see [ENV.md](ENV.md)):

| Variable | Value |
|---|---|
| `AUTH_MODE` | `oidc` |
| `OIDC_ISSUER` | `https://<host>/realms/<realm>` — same issuer as the API |
| `OIDC_CLIENT_ID` | `app-registry-ui` |
| `OIDC_CLIENT_SECRET` | the client secret from the Credentials tab above |
| `OIDC_REDIRECT_URI` | matches one of the redirect URIs in §1 (e.g. `http://localhost:8000/auth/callback`) |
| `SESSION_SECRET` | random 32+ character string; used to encrypt session cookies for the HTMX session store |

The UI reads its ID token claims via `libs/go/htmxauth`, which stores an
authenticated principal in a signed cookie. The same access token is kept in
the session so it can be forwarded as a gRPC metadata header (via
`libs/go/grpcauth`) to the API — this is why the audience mapper and realm-roles
mapper are both required: one satisfies the gRPC verifier, the other populates
the HTMX session with role data.

> **Tilt note.** In local dev the UI runs on `localhost:8000` (see §1's redirect
> table) while the API is forwarded to `localhost:50061`. Set both services to
> `AUTH_MODE=none` / `GRPC_AUTH_MODE=none` so they work without Keycloak. The
> UI injects a fake principal holding every app-registry role, matching what the
> gRPC server's `AuthModeNone` does (see §3a above).

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

These five env vars, and the reconcile/record-build/record-artifact call
shapes themselves, are duplicated across `release.yml`, `ci.yml`, and
`promote.yml`. Rather than keep them in sync by hand, they're wired through
composite actions in `.github/actions/`:

| Action | Wraps |
|---|---|
| `app-registry-auth` | Exports the five `APP_REGISTRY_ADDRESS`/`GRPC_AUTH_*` vars from typed inputs. Used directly by `promote.yml`; used internally by the other three below. |
| `app-registry-reconcile` | `release_helper_go manifest-set` piped into `app-registry apps reconcile`. |
| `app-registry-record-build` | `app-registry builds record`, outputting `build-id`. |
| `app-registry-record-image` | Digest resolution (`docker buildx imagetools inspect`) + `app-registry artifacts record --kind image`. |

Any future workflow that needs to record into the App Registry should call
these instead of re-copying the `env:` block or the bash.

**`app-registry-reconcile` runs from `ci.yml`, not `release.yml`.** It's a
`build-release-tools` → `reconcile-app-registry` job pair gated
`if: github.event_name == 'push' && github.ref == 'refs/heads/main'`, so it
only ever fires once per merge to `main`, against that merge's exact tree.
It deliberately does **not** run as part of a release: `release.yml` is a
`workflow_dispatch` that can target any ref, including an old tag for a
hotfix, and `ReconcileApps` treats whatever manifest set it's given as the
complete truth -- reconciling an old commit would flag every app added
since as `MISSING`. See ARCHITECTURE.md "Rejected alternatives" for the
scoped-registration alternative this ruled out.

Client-side variables the CLI reads (see [ENV.md](ENV.md)):

| Variable | Value |
|---|---|
| `GRPC_AUTH_MODE` | `oidc` — must match the server |
| `GRPC_AUTH_TOKEN_URL` | `https://<host>/realms/<realm>/protocol/openid-connect/token` |
| `GRPC_AUTH_CLIENT_ID` | `app-registry-builder-<APP_REGISTRY_BUILDER_ENV, default dev>` (recording) or `app-registry-promoter-<environment>` (promotion) |
| `GRPC_AUTH_CLIENT_SECRET` | that client's secret |

### Where each secret goes

This placement **is** the security control — it is what stops a build job from
promoting. Getting it wrong defeats the entire credential model.

| Secret | Location | Why |
|---|---|---|
| builder client secret (`APP_REGISTRY_BUILDER_CLIENT_SECRET`) | **Repository** secret | Every release job needs it; it grants recording only — wired into `release.yml`'s reconcile/build/artifact recording steps, each a call to one of the `.github/actions/app-registry-*` composite actions (see below) |
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
| `APP_REGISTRY_ADDRESS` | the API's ingress host:port — **must include the port** (e.g. `dev-app-registry.whalenet.dev:443`); `libs/go/grpcclient`'s TLS auto-detect only fires on `:443` or an `https://` prefix, so a bare hostname dials plaintext against a TLS-only ingress and hangs (issue #539) |
| `APP_REGISTRY_AUTH_TOKEN_URL` | the Keycloak token endpoint, e.g. `https://<host>/realms/<realm>/protocol/openid-connect/token` — same for every client, so it is a variable, not a secret |
| `APP_REGISTRY_CICD_OPT_IN` | `true` to enable recording — see below |
| `APP_REGISTRY_BUILDER_ENV` | which builder client `release.yml` authenticates as: `dev` or `prod`, matching a provisioned `app-registry-builder-<env>` client. Falls back to `dev` when unset — only `dev` is wired up in CI today. |

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
  Keycloak service accounts — see PLAN-HISTORY.md → AR-3 → "Credential
  model". Worth revisiting for secretless CI once this is proven.

## Related

- [`libs/go/grpcauth/KEYCLOAK.md`](../../libs/go/grpcauth/KEYCLOAK.md) — Keycloak configuration, step by step, and the reusable pattern
- [ENV.md](ENV.md) — every environment variable for the server, CLI, and migration
- [ARCHITECTURE.md](ARCHITECTURE.md) — the authorization model and why it splits where it does
- [PLAN.md](PLAN.md) — phase status and what is still to come
- [TESTING.md](TESTING.md) — running the registry locally in Tilt
