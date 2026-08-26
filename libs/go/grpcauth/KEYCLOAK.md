# Keycloak setup for `grpcauth` — a step-by-step guide

How to set up Keycloak so a Go service using `libs/go/grpcauth` can authenticate
callers and authorize them by role. Written for someone who has a Keycloak
instance running and has never configured a client before.

This is the **reference pattern for service-to-service auth in this repo**. It is
written generically; `app-registry` is used as the worked example throughout
because it is the first service to use roles. Section
["Applying this to a new service"](#applying-this-to-a-new-service) is the
checklist to copy.

---

## 1. The mental model

Five Keycloak concepts, and what each one does for you:

| Keycloak thing | What it is | What it does here |
|---|---|---|
| **Realm** | An isolated tenant — its own users, roles, clients, signing keys | One realm per platform. The realm URL is your OIDC **issuer**. |
| **Client** | An application that can obtain tokens | One client per *caller identity* — `app-registry-builder`, `app-registry-promoter-prod`, … |
| **Service account** | A machine "user" that Keycloak attaches to a confidential client | How CI gets a token with no human involved (`grant_type=client_credentials`) |
| **Realm role** | A named permission, global to the realm | What `grpcauth` reads to authorize. **This is the one that matters.** |
| **Audience (`aud`)** | Which API a token is *for* | What `grpcauth` validates so a token minted for another service is rejected here |

The flow, end to end:

```
GitHub Actions job
  │  client_id + client_secret  (GitHub Environment secret)
  ▼
Keycloak token endpoint  ──►  JWT  { sub, aud: ["app-registry-api"],
  │                                   realm_access.roles: ["app-registry-builder"] }
  ▼
gRPC call, Authorization: Bearer <JWT>
  ▼
app-registry-api
  ├─ grpcauth interceptor: fetch JWKS from issuer, verify signature,
  │  check aud == GRPC_OIDC_CLIENT_ID, put Claims in ctx
  └─ handler: claims.Roles contains "app-registry-builder"? → allow
```

### Two gotchas that will cost you an afternoon

Read these before clicking anything. Both are properties of
[`auth.go`](auth.go), not of Keycloak.

> **Gotcha 1 — `grpcauth` reads REALM roles only.**
> It parses `realm_access.roles` from the token and nothing else. Keycloak also
> has *client roles*, which land in `resource_access.<client-id>.roles` — those
> are **invisible to `grpcauth`**. The admin console makes client roles just as
> easy to create, and they will silently do nothing. Always use **Realm roles**.
>
> Because realm roles are global to the realm, **prefix them with the service
> name** (`app-registry-builder`, not `builder`) or you will collide with
> another service's roles later.

> **Gotcha 2 — Keycloak does not put your API in `aud` by default.**
> `grpcauth` verifies `aud` contains the server's `GRPC_OIDC_CLIENT_ID`. A
> client-credentials token from a fresh Keycloak client has `aud: ["account"]`,
> so **every call fails with `Unauthenticated` until you add an audience
> mapper** (step 4). This is the single most common failure.

---

## 2. Decide your roles before you start

Write the role table down first. Changing it later means re-editing every client.

For `app-registry`, from [ARCHITECTURE.md](../../../tools/app_registry/ARCHITECTURE.md#authorization):

| Realm role | Grants | Held by |
|---|---|---|
| `app-registry-builder` | `AppRegistry` writes, `ArtifactRegistry` writes | CI, all workflows |
| `app-registry-promoter-dev` | `PromotionRegistry` writes, `dev` only | CI job targeting the `dev` GH Environment; humans |
| `app-registry-promoter-stage` | `PromotionRegistry` writes, `stage` only | GH Environment `stage`; humans |
| `app-registry-promoter-prod` | `PromotionRegistry` writes, `prod` only | GH Environment `prod`; a small human group |
| `app-registry-admin` | `EnvironmentRegistry`, `SetAppStatus` | Humans only |
| *(any authenticated)* | all reads | everyone with a valid token |

**The security property this buys you:** the builder credential is a *different
Keycloak client* from the promoter credentials, and its token simply does not
carry a `promoter` role. A compromised build job cannot promote to prod, no
matter what it does with the secret it holds.

**Environment scoping comes from GitHub, not Keycloak.** The
`app-registry-promoter-prod` client secret is stored as a secret on the GitHub
**Environment** named `prod`. Only a workflow job that declares
`environment: prod` can read it — and that declaration is what triggers your
required reviewers. Keycloak just enforces what the role means once the token
exists.

---

## 3. Create the API client (the audience)

You need a client representing the **API being called**, so other clients can
name it as their audience. It never logs in and holds no secret you use.

1. Admin console → your realm → **Clients** → **Create client**
2. Client type `OpenID Connect`, **Client ID**: `app-registry-api` → Next
3. **Client authentication**: On. **Authorization**: Off.
4. Authentication flow: **uncheck everything** — Standard flow, Direct access
   grants, Service accounts roles. This client never obtains a token. → Next
5. Leave URLs blank → **Save**

That's it. Its only job is to be a name that shows up in the audience-mapper
dropdown, and to be the value of `GRPC_OIDC_CLIENT_ID` on the server.

> If you would rather not create this client, every audience mapper below has an
> **Included Custom Audience** free-text field you can use instead. Creating the
> client is better: it keeps the audience name from drifting via typo.

## 4. Create a caller client (repeat per identity)

Worked example: `app-registry-builder`. Repeat the whole section for
`app-registry-promoter-dev`, `-stage`, `-prod`, and `app-registry-admin`.

### 4a. Create the client

1. **Clients** → **Create client**
2. **Client ID**: `app-registry-builder` → Next
3. **Client authentication**: **On** ← makes it confidential, i.e. it gets a
   secret. Required for `client_credentials`.
4. Authentication flow — check **Service accounts roles** only. Uncheck
   Standard flow and Direct access grants: this identity is a machine, it must
   not be able to do a browser login or a username/password grant. → Next
5. Leave URLs blank → **Save**

### 4b. Get the secret

**Credentials** tab → copy **Client secret**. This is
`GRPC_AUTH_CLIENT_SECRET`.

Store it immediately in the right place (see step 6) — for promoter clients that
means a **GitHub Environment** secret, not a repository secret. A repository
secret is readable by any workflow and destroys the whole property you are
building.

### 4c. Create and assign the realm role

First create the role once per realm:

1. **Realm roles** → **Create role**
2. **Role name**: `app-registry-builder`, description: what it grants → **Save**

Then attach it to this client's service account:

3. Back to **Clients** → `app-registry-builder` → **Service accounts roles** tab
4. **Assign role** → change the filter dropdown to **Filter by realm roles**
   ← *easy to miss; it defaults to client roles, which `grpcauth` ignores*
5. Tick `app-registry-builder` → **Assign**

### 4d. Add the audience mapper (do not skip)

1. Still on the client → **Client scopes** tab
2. Click the dedicated scope, named `app-registry-builder-dedicated`
3. **Add mapper** → **By configuration** → **Audience**
4. Fill in:
   - **Name**: `app-registry-api-audience`
   - **Included Client Audience**: `app-registry-api` (the client from step 3)
   - **Add to access token**: **On**
5. **Save**

Now this client's tokens carry `aud: ["app-registry-api", "account"]` and the
server will accept them.

## 5. Human users

Humans authenticate as themselves, not via a client secret.

- **One person:** Users → select user → **Role mapping** → **Assign role** →
  *Filter by realm roles* → assign.
- **A team (preferred):** Groups → **Create group** e.g. `prod-promoters` →
  the group's **Role mapping** tab → assign `app-registry-promoter-prod`. Then
  add and remove people from the group. Membership changes need no role edits,
  and the group is the thing you audit.

Human tokens come from a normal interactive login through whatever client your
CLI/UI uses, so that client also needs the audience mapper from step 4d.

---

## 6. Wire it up

### Server — `app-registry-api`

```bash
GRPC_AUTH_MODE=oidc
GRPC_OIDC_ISSUER=https://auth.example.com/realms/whale       # no trailing slash
GRPC_OIDC_CLIENT_ID=app-registry-api                          # the expected aud
```

The server reaches the issuer's `/.well-known/openid-configuration` and JWKS
endpoint **at startup and periodically after**. If Keycloak is unreachable at
boot, `grpcauth.NewServerInterceptors` returns an error and the process exits —
make sure network policy allows the pod to reach Keycloak.

### CI — GitHub Actions

```yaml
jobs:
  record:                       # builder identity — no environment needed
    env:
      GRPC_AUTH_MODE: oidc
      GRPC_AUTH_TOKEN_URL: https://auth.example.com/realms/whale/protocol/openid-connect/token
      GRPC_AUTH_CLIENT_ID: app-registry-builder
      GRPC_AUTH_CLIENT_SECRET: ${{ secrets.APP_REGISTRY_BUILDER_SECRET }}

  promote:
    environment: prod           # ← this line is the access control
    env:
      GRPC_AUTH_MODE: oidc
      GRPC_AUTH_TOKEN_URL: https://auth.example.com/realms/whale/protocol/openid-connect/token
      GRPC_AUTH_CLIENT_ID: app-registry-promoter-prod
      GRPC_AUTH_CLIENT_SECRET: ${{ secrets.APP_REGISTRY_PROMOTER_SECRET }}
```

Both jobs reference `secrets.APP_REGISTRY_PROMOTER_SECRET`-style names, but the
promoter one resolves **only** inside the `prod` environment. Configure required
reviewers on that environment and the promotion gate is a human approval.

### Local development

Leave `GRPC_AUTH_MODE=none` (the default). The server injects
`Claims{Subject: "dev-user", Roles: ["admin"]}` and every check passes. **Client
and server modes must match** — a `none` client against an `oidc` server fails
every call with `Unauthenticated`.

---

## 7. Verify before you debug the app

Get a token by hand. This isolates Keycloak problems from application problems.

```bash
TOKEN=$(curl -s -X POST \
  https://auth.example.com/realms/whale/protocol/openid-connect/token \
  -d grant_type=client_credentials \
  -d client_id=app-registry-builder \
  -d client_secret=<secret> | jq -r .access_token)

# decode the payload (no verification — just to read it)
echo "$TOKEN" | cut -d. -f2 | base64 -d 2>/dev/null | jq '{sub, aud, realm_access}'
```

You are looking for exactly this:

```json
{
  "sub": "9f2c...",
  "aud": ["app-registry-api", "account"],   ← step 4d worked
  "realm_access": { "roles": ["app-registry-builder", "default-roles-whale"] }
}                                            ↑ step 4c worked, and it is REALM roles
```

If `aud` is only `["account"]`, redo step 4d. If `realm_access.roles` lacks your
role but `resource_access` has it, you assigned a client role — redo step 4c with
the filter switched to **realm roles**.

---

## 8. Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| Every call `Unauthenticated`, token looks fine | `aud` missing the API | Audience mapper, step 4d |
| Every call `Unauthenticated`, no token sent | Client `GRPC_AUTH_MODE` unset/`none` while server is `oidc` | Set both to `oidc` |
| Role checks fail, token has the role | Role is under `resource_access`, not `realm_access` | Re-assign as a **realm** role |
| `unauthorized_client` from the token endpoint | Service accounts flow disabled, or client is public | Step 4a: Client authentication On, Service accounts roles checked |
| `invalid_client` | Wrong secret, or secret rotated | Re-copy from the Credentials tab |
| Server won't start, OIDC provider error | Issuer URL wrong or unreachable | Must be `https://host/realms/<realm>` exactly, no trailing slash; check egress from the pod |
| Works locally, fails in cluster | Cluster can't reach Keycloak, or an internal issuer URL whose hostname doesn't match the `iss` claim | The issuer URL must match the `iss` in issued tokens *character for character* |
| Token expires mid-long-job | Access token lifespan too short | `grpcauth`'s service-account dial option refreshes automatically; if you are calling the token endpoint by hand, refresh it yourself |

---

## Applying this to a new service

The checklist, stripped of the example:

1. **Define roles first**, prefixed with the service name. Fewer is better — a
   role you cannot describe in one sentence should not exist.
2. **One realm role per distinct permission level**, not per caller. Multiple
   clients can hold the same role.
3. **One Keycloak client per caller identity.** Split identities exactly where
   you want a privilege boundary — the separation *is* the security control.
4. Create an **API client** whose only purpose is to be the audience name.
5. Per caller client: confidential + service accounts only → realm role →
   **audience mapper**.
6. Humans get roles via **groups**, never individually.
7. Server: `GRPC_AUTH_MODE=oidc`, `GRPC_OIDC_ISSUER`, `GRPC_OIDC_CLIENT_ID` =
   the API client id.
8. Secrets for privileged identities go in **GitHub Environment** secrets with
   required reviewers, never repository secrets.
9. Verify with the curl in step 7 **before** touching application code.
10. Enforce in handlers, and write a test that asserts the *low*-privilege role
    is **rejected** — the negative test is the one that proves the boundary.

## Related

- [README.md](README.md) — `grpcauth` API, env vars, dial options
- [auth.go](auth.go) — the verifier; `realm_access.roles` parsing lives here
- [`tools/app_registry/ARCHITECTURE.md`](../../../tools/app_registry/ARCHITECTURE.md) — the role split this guide implements

---

## Device Authorization Grant Flow

The device authorization grant (also called device flow) is suitable for CLI tools, development workflows, and non-interactive scenarios where a user may not have a browser readily available. Unlike the service account flow (which uses a secret), the device flow prompts the user once interactively for approval, then non-interactively refreshes the token thereafter.

### Prerequisites for device grant

The Keycloak client must be configured differently than a service account client:

1. **Public client** — The device grant flow requires a *public* client (no secret). The client cannot use `client_credentials` because the user's consent is part of the flow, not a pre-shared secret.

2. **Device authorization flow enabled** — In Keycloak, the device authorization flow is controlled by a feature toggle and must be explicitly enabled on the realm or the client.

3. **Token cache** — The resulting token is persisted to disk with restrictive permissions (`0600`) so subsequent invocations refresh without user interaction. By default, tokens cache to `$HOME/.cache/grpcauth/device_grant.json`. See `DeviceGrantConfig.CacheDir` to override.

### Creating a device grant client

1. **Clients** → **Create client**
2. **Client ID**: e.g., `leaflab-cli` → Next
3. **Client authentication**: **Off** ← this must be OFF for public clients
4. **Authentication flow** — Check **Device Authorization Grant** only. Uncheck all others. → Next
5. Leave URLs blank → **Save**

### Assigning roles to a device grant client

The same role assignment as service accounts applies:

1. **Service accounts roles** tab (yes, it still exists for public clients in terms of role assignment)
2. **Assign role** → *Filter by realm roles* → select the appropriate roles → **Assign**

Then add the audience mapper (step 4d from the service account flow):

1. **Client scopes** tab
2. Click the dedicated scope, named `leaflab-cli-dedicated`
3. **Add mapper** → **By configuration** → **Audience**
4. Fill in:
   - **Name**: `leaflab-api-audience`
   - **Included Client Audience**: `leaflab-api` (your API client)
   - **Add to access token**: **On**
5. **Save**

### Using device grant from Go

```go
authOpt, err := grpcauth.NewDeviceGrantDialOption(grpcauth.DeviceGrantConfig{
    TokenURL:                 "https://auth.example.com/realms/whale/protocol/openid-connect/token",
    ClientID:                 "leaflab-cli",
    RequireTransportSecurity: false, // internal cluster; set true if using TLS
})
if err != nil {
    log.Fatalf("grpcauth: %v", err)
}

conn, err := grpc.NewClient(addr,
    grpc.WithTransportCredentials(insecure.NewCredentials()),
    authOpt,
)
```

On the first run, the user sees:

```
Please authorize this application by visiting:

https://auth.example.com/device?user_code=ABCD-1234

Press Enter when authorized...
```

The user visits the URL, logs in, and enters the code. Thereafter, calls to `NewDeviceGrantDialOption` with the same `TokenURL` and `ClientID` silently refresh from the cached token until it expires.

### Keycloak device authorization grant configuration

As of Keycloak 22, device authorization is a realm-level feature. To enable it:

1. **Realm settings** → **Tokens** tab
2. Find the **Device Authorization Grant** section
3. Ensure it is enabled
4. (Optional) Configure timeout and polling intervals

If device authorization is disabled at the realm level, clients cannot use the device flow regardless of their individual settings.

---
