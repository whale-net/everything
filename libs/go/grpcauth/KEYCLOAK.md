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

## 9. Service accounts: telling them apart from human callers

A service running behind `grpcauth` (e.g. whagent-net's `api`, FR6/#2243)
sometimes needs to know, per call, whether the caller is a human or a
Keycloak client-credentials service account -- not just *which* client/role
it holds. `grpcauth.Claims` carries this as two additive fields populated by
`oidcVerifier.Verify` (`auth.go`):

| Field | Source claim | What it is |
|---|---|---|
| `Claims.ClientID` | `azp` (falls back to `client_id`) | The client the token was issued to -- set on every token, human or service account. |
| `Claims.IsServiceAccount` | `preferred_username` | `true` iff `preferred_username` starts with `service-account-`. |

**The rule, and why it's reliable:** every service account Keycloak attaches
to a confidential client (step 4a, "Service accounts roles") gets a Keycloak
*user* named `service-account-<client-id>` -- Keycloak's own fixed naming,
not something you configure. A token minted for that user's
`client_credentials` grant carries that name in `preferred_username`, and no
human user can be named this way (Keycloak reserves the prefix). This is the
*only* signal available on the token itself: there is no separate token
type, scope, or claim, and `ClientID` alone is not enough, since a human
caller's token has one too (whichever client the human authenticated
through).

`grpcauth.Claims.IsServiceAccount` is what a handler should branch on --
never `ClientID` presence -- exactly as `whagent_net/api/handlers/session.go`'s
`callerSubject` does to pick `SubjectKindService` vs. `SubjectKindHuman`
(FR6/#2243). `AuthModeNone`'s injected dev Claims never set
`IsServiceAccount` (it defaults `false`), so local/Tilt development without a
real Keycloak always looks like a human caller -- there is no dev-mode way to
locally exercise a service-account-classified call short of running against
a real `oidc`-mode Keycloak (see `whagent_net/README.md` § "Client
credentials (service accounts)" for how one service documents that path).

**Role checks are identical either way.** A service account's realm role
still needs the same "Filter by realm roles" assignment as step 4c, just to
the client's own service account user (**Service accounts roles** tab, not a
separate human-vs-service role) -- `grpcauth` reads `realm_access.roles`
regardless of who the caller is, so there is no special-casing needed on the
authorization side, only on the identity side.

---

## 10. Token exchange (RFC 8693): a client that mints tokens for other identities

This is a different problem from everything above: instead of a client
proving *its own* identity, here a confidential client authenticates as
itself and asks Keycloak to mint a token asserting **someone else's**
identity, by user id (`requested_subject`) — the mechanism
`whagent_net/mcp` uses (issue #2249, `whagent_net/ARCHITECTURE.md`
"`mcp`'s OAuth2 credential and RFC 8693 token exchange") to turn an
already-resolved `(iss, sub)` pair into a real, verifiable Keycloak
access token before calling a downstream API that only trusts
Keycloak-signed tokens.

**This is a materially bigger blast radius than a normal client** (step
4): the credential that authenticates this client can mint a token as
*any* user in the realm, not just describe the client's own permissions.
Treat it accordingly — a dedicated client, never reused for anything
else, with the secret held only by the one process that needs it
(`whagent_net/ARCHITECTURE.md`'s NFR8 writeup has the full custody/
rotation story for the `whagent_net/mcp` instance of this).

### 10a. Create the client

Same as step 4a (**Clients → Create client**, confidential, **Client
authentication** on, **Service accounts roles** on, no standard/implicit
flow needed) — but do not reuse an existing caller client. Give it a name
that says what it's for (e.g. `whagent-net-mcp-token-exchange`), not the
name of the service that happens to hold it.

### 10b. Grant it token-exchange / impersonation permission

In newer Keycloak versions this lives under the realm's **Client policies**
/ fine-grained admin permissions (**Realm settings → User profile** is not
it — look for **Permissions** on the client itself, or the realm-level
**Authorization** tab, depending on your Keycloak version): enable
permissions on the client, then create (or reuse) a client policy that
allows this client's service account to exchange tokens for arbitrary
users. Exact admin-console navigation drifts between Keycloak versions —
search your version's docs for "token exchange" or "impersonation" if the
above doesn't match what you see; the underlying grant this section
configures does not change between versions, only where you click to
enable it.

### 10c. Verify with curl before touching application code

```bash
curl -s -X POST "$KEYCLOAK_URL/realms/$REALM/protocol/openid-connect/token" \
  -d grant_type=urn:ietf:params:oauth:grant-type:token-exchange \
  -d client_id=whagent-net-mcp-token-exchange \
  -d client_secret=$CLIENT_SECRET \
  -d requested_subject=$TARGET_USER_ID \
  -d requested_token_type=urn:ietf:params:oauth:token-type:access_token
```

A successful exchange returns an ordinary OAuth2 token response
(`access_token`, `token_type`, `expires_in`); decode `access_token` (step
7's `jwt` one-liner) and confirm its `sub` claim is `$TARGET_USER_ID`, not
this client's own service-account user. A `403`/`invalid_client` here
means 10b's permission grant did not take — fix that before wiring up
any application code, exactly like step 7's guidance for a normal client.

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
