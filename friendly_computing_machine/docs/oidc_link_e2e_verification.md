# OIDC identity link — end-to-end verification runbook

> **Runbook status: WRITTEN, NOT YET EXECUTED.**
>
> The procedure below is complete and every variable and endpoint in it is
> cross-checked against [ENV.md](../ENV.md), [ARCHITECTURE.md](../ARCHITECTURE.md),
> and the code they document. The **executed** half of the deliverable — one
> real run against a real Slack workspace and a real Keycloak realm, with the
> evidence pasted into [Recorded evidence](#recorded-evidence) — is **outstanding
> and needs a human with Slack and Keycloak access.** The evidence section is an
> empty template on purpose. Do not fill it with a dry run, a Tilt run, or a
> guess; see [What does not count as evidence](#what-does-not-count-as-evidence).

This runbook proves, in one continuous live run, that:

1. A browser OIDC authorization-code login against a real Keycloak realm links
   a Slack user to their `(iss, sub)` identity.
2. An unlinked `@mention` is gated with a one-time link prompt; the login
   persists the mapping; and the **next** `@mention` starts a whagent-net
   session whose recorded `on_behalf_of` is that user, with follow-up turns
   continuing under fcm's service credential.

It is written for a second operator who was not part of the original plan.

## Read this before you touch anything: the fail-closed default

**Delegation is opt-in, per-deployment, and off by default. Setting the
allowlist is NOT part of normal operation and NOT a setup step you perform
because this runbook told you to.**

`WHAGENT_ON_BEHALF_OF_ALLOWED_CLIENT_IDS` (read by whagent-net `api`) ships
**empty**. Empty means *no caller may ever set `on_behalf_of`*: every request
carrying one is rejected `PERMISSION_DENIED`. That is the correct default
(`whagent_net/ENV.md` "`api` server"; NFR1), and it is what the local Tilt
config and every deployment ship.

This runbook requires you to opt in **temporarily, for one verification run**,
because the delegated happy path is unreachable while the allowlist is empty —
that is the point of the fail-closed default, not a bug to route around. Treat
the opt-in as a change to a security control:

- Make it on **one** deployment you control, never shared or production.
- Scope it to the **single** client id — fcm's own service account,
  `WHAGENT_CLIENT_ID`. Not a wildcard, not a list of everyone's clients.
- **Revert it in step 9.** Leaving it set is a standing authorization for fcm to
  act as any linked user, indefinitely, with nobody watching.
- If you are running against production and cannot guarantee the revert,
  **do not run this.** A missing evidence block is a much smaller harm than a
  silently-widened delegation grant.

Nothing in this repository's checked-in configuration weakens that default, and
nothing here should be edited to make this runbook easier. The knob on the
whagent-net `Tiltfile` defaults to empty for exactly this reason.

## Prerequisites

You cannot begin until every one of these is true. A half-configured run must
never be mistaken for a pass, so check them explicitly rather than assuming.

| # | Prerequisite | How to check | Source |
|---|---|---|---|
| P1 | A **real Slack workspace** with fcm installed | Open the workspace in a browser; the app is listed under Apps | — |
| P2 | fcm's Slack app has Socket Mode enabled and the `app_mention` event subscribed | Slack app admin → Event Subscriptions | `bot run-slack-socket-app` |
| P3 | The Slack app holds the `chat:write` scope | The bot posts an ephemeral prompt via `client.chat_postEphemeral` | `bot/handlers/whagent.py` |
| P4 | A **real Keycloak realm**, shared by fcm's `web` app and whagent-net's `api`/`ui` | Log into the realm's admin console | `ENV.md` "OIDC identity link web app" |
| P5 | fcm registered in that realm as a **Confidential** client, **Standard flow** enabled | Keycloak → Clients → your client | `ENV.md` same section |
| P6 | That client's **Valid redirect URI** is exactly `${FCM_WEB_PUBLIC_URL}/link/callback` — no trailing slash, no query | Keycloak → Clients → your client → Settings | `web/config.py` `callback_url` |
| P7 | A human user exists in the realm with a known `sub` | Keycloak → Users | — |
| P8 | A whagent-net `agent_id` exists that the test user is allowed to run | `whagent_net` → Agents | — |
| P9 | A Slack channel is linked to that agent | `SELECT 1 FROM fcm.slackchannelagentlink WHERE slack_channel_id = '<C…>' AND enabled;` | `docs/whagent_integration.md` |
| P10 | Postgres, Temporal, and whagent-net `api` are all reachable from where fcm runs | `GET /health` on fcm web returns 200 (step 2) | — |
| P11 | You have DB read access to **both** databases — fcm's (`POSTGRES_URL`) and whagent-net's (`PG_DATABASE_URL`) | `psql "$POSTGRES_URL" -c '\conninfo'` | — |
| P12 | You have shell/log access to the `bot`, `web`, and whagent-net `api` processes | — | — |

### Values you will need to fill in

Record these before starting. Do not guess them mid-run.

```
FCM_WEB_PUBLIC_URL=          # externally-reachable base URL of fcm's web app, no trailing slash
FCM_OIDC_ISSUER_URL=         # Keycloak realm issuer, e.g. https://kc.example.com/realms/whagent
FCM_OIDC_CLIENT_ID=          # confidential browser-login client id (NOT WHAGENT_CLIENT_ID)
FCM_OIDC_CLIENT_SECRET=      # confidential browser-login client secret
FCM_WEB_SESSION_SECRET=      # long random signing key for the session cookie
WHAGENT_CLIENT_ID=           # fcm's service-account client id -> the ONE allowlist entry (step 5)
WHAGENT_CLIENT_SECRET=
WHAGENT_KEYCLOAK_TOKEN_URL=  # .../realms/<realm>/protocol/openid-connect/token
WHAGENT_API_URL=             # gRPC host:port of whagent-net api
WHAGENT_UI_PUBLIC_URL=       # base URL; fcm posts <url>/sessions/<id> in the thread
SLACK_TEAM_ID=               # T… , the Slack workspace id
SLACK_USER_ID=               # U… , the unlinked test user's id
REALM_ISSUER=                # must equal FCM_OIDC_ISSUER_URL exactly
REALM_SUB=                   # the human's Keycloak sub, from P7
AGENT_ID=                    # from P8
CHANNEL_SLACK_ID=            # C… , from P9
```

`FCM_OIDC_CLIENT_ID` and `WHAGENT_CLIENT_ID` are **different clients** and must
not be confused: one is the browser-facing login the human completes, the other
is fcm's machine identity it calls whagent-net with.

## The flow, and where each step observes it

```
Slack            fcm bot            fcm web        Keycloak        whagent-net api
   |                 |                  |               |                  |
   | @mention        |                  |               |                  |
   |---------------->| no mapping?      |               |                  |
   |                 | mint token, post ephemeral /link/<token>            |
   |<----------------|                  |               |                  |
   | click link      |                  |               |                  |
   |---------------------------------->| peek token, session stash,        |
   |                  |                 | redirect to authorize            |
   |                  |                 |--------------->|                  |
   |                  |                 |<---------------| code             |
   |                  |                 | code exchange + verified id_token |
   |                  |                 |------------------------------->|
   |                  |                 | consume token + upsert mapping   |
   |<----------------------------------| "Linked"      |                  |
   | @mention again  |                  |               |                  |
   |---------------->| mapping found -> start_workflow(on_behalf_of=iss,sub)|
   |                 |                  |               |     StartSession |
   |                 |                  |               |<-----------------|
   |                 |                  |               |     delegated    |
   |<----------------| session link + replies                           |
```

## Step 1 — Pre-flight: assert the allowlist is empty

Do this **before** configuring anything, and record the result. It proves you
are starting from the shipped fail-closed state, so the later opt-in in step 5
is visible as a change you made.

```bash
kubectl exec deploy/whagent-net-api -- printenv WHAGENT_ON_BEHALF_OF_ALLOWED_CLIENT_IDS
```

- **Unset or empty** → correct. Continue.
- **Anything else** → stop. Someone has already opted this deployment in. Find
  out who and why before you touch it.

Also confirm `GRPC_AUTH_MODE=oidc` on whagent-net `api`. Its default is `none`,
which injects dev claims and never verifies a token — with `none` there is no
real `client_id`, the allowlist can never match, and the delegated path in
step 6 is unreachable no matter what else you configure.

```bash
kubectl exec deploy/whagent-net-api -- printenv GRPC_AUTH_MODE   # expect: oidc
```

## Step 2 — Start fcm's identity-link web app

```bash
export $(cat .env | xargs)   # per README.md; .env.example lists every variable above
fcm web run --log-otlp
```

It serves on `FCM_WEB_PORT` (default `8000`) and binds `0.0.0.0`.

**Observe:**

```bash
curl -s -o /dev/null -w '%{http_code}\n' "${FCM_WEB_PUBLIC_URL}/health"   # expect: 200
```

The app exposes exactly three routes:

| Route | Purpose |
|---|---|
| `GET /health` | liveness |
| `GET /link/{token}` | validates a one-time token, stashes it in the session cookie, redirects to Keycloak's authorize endpoint |
| `GET /link/callback` | completes the code exchange, writes the mapping, clears the session |

**If it does not come up:** `web run` refuses to start if Alembic reports a
pending migration (`need to run migration`). Run `fcm migration run` first, or
pass `--skip-migration-check` if you have already migrated this database.
Confirm `POSTGRES_URL` points at the same database the `bot` uses — a `web` app
on a different database mints tokens the `bot` will never see.

## Step 3 — Trigger the gate with an unlinked `@mention`

Pick a test user with **no** row in `fcm.slackkeycloakidentity`. Confirm that
first:

```sql
SELECT * FROM fcm.slackkeycloakidentity WHERE slack_user_id = :'SLACK_USER_ID';
-- expect: 0 rows
```

In Slack, in the linked channel, have that user send `@fcm hello`.

**Observe — all three, or the gate did not fire:**

1. Slack shows the test user (and only them) an ephemeral message reading
   "Link your Slack account to use this agent: **Link my account** (one-time
   link, expires in 10 minutes)." It is posted at channel top level, not
   thread-scoped, so it renders no matter which thread they have open.
2. A `slacklinktoken` row exists, unconsumed, expiring in ~10 minutes:
   ```sql
   SELECT id, slack_team_id, slack_user_id, expires_at, consumed
   FROM fcm.slacklinktoken
   WHERE slack_user_id = :'SLACK_USER_ID'
   ORDER BY id DESC LIMIT 1;
   ```
3. **No** `fcm.slackthreadsession` row was created for this thread. The gate
   runs *before* any workflow starts, so a blocked mention must not leave an
   orphaned `ACTIVE` row. This is as important to confirm as the prompt itself.

The bot's log at INFO shows `issued identity link prompt slack team=… user=…` —
team and user ids only; the token is never logged.

**If nothing appears in Slack at all:** check for a thread reply saying "No
whagent-net agent is configured for this channel" — that means step P9 failed
and you are not exercising the gate at all. If the bot logs an **ERROR**
beginning `FCM_WEB_PUBLIC_URL is unset`, the mention is still correctly
blocked, but no link was posted; set the variable on the `bot` app and repeat.

## Step 4 — Complete the browser OIDC login

Click **Link my account** (or open the same URL in that user's browser) within
10 minutes.

1. `GET /link/{token}` looks the token up **without consuming it**. An unknown,
   expired, or already-consumed token renders a static "Link failed" page and
   does **not** redirect.
2. The token is stashed in the Starlette session cookie, and the browser is
   redirected to Keycloak's authorize endpoint with scope `openid email profile`.
3. The human authenticates with their realm credentials.
4. Keycloak redirects back to `${FCM_WEB_PUBLIC_URL}/link/callback?code=…&state=…`.
5. Authlib exchanges the code and verifies the returned ID token. The verified
   `iss` and `sub` claims are read — nothing is hand-verified.
6. `complete_link` consumes the token and upserts the mapping **in a single
   transaction**. Either both happen or neither does.

**Observe — the browser shows:**

> **Linked** — Your Slack account is now linked. Return to Slack.

**And the database shows exactly one new mapping, whose `iss` equals your
`FCM_OIDC_ISSUER_URL` and whose `sub` equals the value from P7:**

```sql
SELECT id, slack_team_id, slack_user_id, keycloak_iss, keycloak_sub, created_at
FROM fcm.slackkeycloakidentity
WHERE slack_team_id = :'SLACK_TEAM_ID' AND slack_user_id = :'SLACK_USER_ID';
```

The same token row now reads `consumed = true` with a `consumed_at` set. The
bot logs at INFO `linked slack team=… user=… to keycloak iss=… sub=…`.

**Security assertions to confirm while you are here:** no access, refresh, or ID
token was persisted — `fcm.slackkeycloakidentity` has no column for one, and
only the `(iss, sub)` pair is stored. No token appears in the `web` app's logs.

**If the callback fails:**
- *"No pending link request found in this session"* → the session cookie did
  not survive the redirect. Check that `FCM_WEB_SESSION_SECRET` is identical on
  every `web` replica and stable across the run, and that the browser is
  accepting cookies for the public URL.
- *"Sign-in could not be completed"* → the code exchange failed. Check the
  client id/secret pair, that the client is Confidential, and that P6's redirect
  URI matches byte for byte.
- *"This link is no longer valid"* (HTTP 410) → the token was already consumed
  or expired. Mint a fresh one by repeating step 3; do not edit the row.
- Anything in that path writes **nothing**. A failed or replayed link leaves
  the database exactly as it was. Start again from step 3.

## Step 5 — Opt in the delegation allowlist (temporary, one client)

This is the deliberate, temporary exception described in
[the fail-closed default](#read-this-before-you-touch-anything-the-fail-closed-default).
Do it now, not earlier, and undo it in step 9.

Set `WHAGENT_ON_BEHALF_OF_ALLOWED_CLIENT_IDS` on whagent-net `api` to exactly
one value — fcm's service-account client id:

```
WHAGENT_ON_BEHALF_OF_ALLOWED_CLIENT_IDS=<the value of fcm's WHAGENT_CLIENT_ID>
```

Not `FCM_OIDC_CLIENT_ID` — that is the browser-login client and is not a
whagent-net gRPC caller. Whitespace around entries is trimmed and empty entries
ignored, so a stray comma is harmless, but a second id is not: remove it.

Restart `api` and confirm:

```bash
kubectl exec deploy/whagent-net-api -- printenv WHAGENT_ON_BEHALF_OF_ALLOWED_CLIENT_IDS
# expect: exactly the one client id
```

The gate is on the *caller's own* `client_id` as recorded in the access token,
so this authorizes fcm and nothing else. A human who authenticates to
whagent-net directly is still not permitted to assert another subject.

## Step 6 — Trigger the delegated session

In Slack, in the same channel, send a **new** `@mention` (a new top-level
message, so it is a fresh thread, not a reply in the previous one).

**Observe — the gate does not fire again.** No ephemeral link prompt, and no
new `slacklinktoken` row:

```sql
SELECT count(*) FROM fcm.slacklinktoken
WHERE slack_user_id = :'SLACK_USER_ID' AND consumed = false;   -- expect: 0
```

Instead the bot starts a session and replies in a thread with:

> Started a whagent-net session: `${WHAGENT_UI_PUBLIC_URL}/sessions/<session_id>`

**If it fails:** the in-thread reply *"Couldn't start a whagent-net session for
this request…"* plus an **ERROR** in the worker log reading `whagent-net denied
StartSession -- a delegated start needs fcm's client_id in whagent-net's
on_behalf_of allowlist` means the allowlist in step 5 is wrong, or `api` was not
restarted, or `GRPC_AUTH_MODE` is still `none`. Nothing was written; fix and
re-mention.

## Step 7 — Record the delegated subject

Take `<session_id>` from the session link posted in step 6 and read whagent-net's
row:

```sql
SELECT session_id, subject_iss, subject_sub, subject_kind,
       on_behalf_of_iss, on_behalf_of_sub, on_behalf_of_kind, agent_id, status
FROM sessions
WHERE session_id = :'SESSION_ID';
```

This is the single most important assertion in the runbook:

| Column | Expected | Why |
|---|---|---|
| `on_behalf_of_iss` | **=** `fcm.slackkeycloakidentity.keycloak_iss` | the linked user's realm |
| `on_behalf_of_sub` | **=** `fcm.slackkeycloakidentity.keycloak_sub` | the linked human |
| `on_behalf_of_kind` | `human` | asserted subject kind |
| `subject_iss` | **=** `REALM_ISSUER` | the authenticated caller |
| `subject_sub` | **=** `WHAGENT_CLIENT_ID` | fcm's service account, not the human |
| `subject_kind` | `service` | the Slack user never holds a credential |
| `status` | `running` | — |

`subject_*` and `on_behalf_of_*` are deliberately separate NOT NULL column sets.
A run where `on_behalf_of_sub` equals `subject_sub` proves nothing: that is
what a **non-delegated** session records. The assertion is that the two differ,
and that the `on_behalf_of_*` half matches the row written in step 4.

The whagent-net `ui` carries `on_behalf_of` into its session view model but does
not render it in any page, so the row is where you observe this. Do not conclude
anything from the UI alone.

## Step 8 — Confirm follow-up turns run under the service credential

In the **same** Slack thread, send a plain message with no `@mention` — the
`message` event is relayed into the already-active session, so no new session is
started and no new `on_behalf_of` is ever asserted.

**Observe:**

- The bot posts a reply in that thread and the session id does not change.
- `SELECT count(*) FROM sessions WHERE session_id = :'SESSION_ID';` is still 1.
  Every `SendTurn` carries only the service-account bearer token; turns do not
  re-assert a subject, and the row's `on_behalf_of_*` are unchanged.
- `fcm.slackthreadsession` still maps this thread to the same
  `whagent_session_id`, with status `ACTIVE`.

This is the intended shape and worth stating plainly: **delegation applies to
session start only.** The human is never authenticated to whagent-net — the
asserted subject is data on fcm's service-credential call.

## Step 9 — Revert the opt-in

Set `WHAGENT_ON_BEHALF_OF_ALLOWED_CLIENT_IDS` back to unset/empty on
whagent-net `api` and restart it. Confirm as in step 1. Then re-mention as a
*different*, unlinked user and confirm you get the link prompt again, not a
`PERMISSION_DENIED` — the flow's normal posture is a gated mention, and a
gated mention still works with the allowlist empty.

**Do not skip this step.** It is the only thing that makes step 5 acceptable.

## Recorded evidence

**EMPTY — this runbook has not been executed.** No run date, workspace, realm,
client id, row id, or observed subject has been recorded, because no run has
happened. The table below is a template; it stays empty until a human fills it
in from a real run.

| Field | Value |
|---|---|
| Run date (UTC) | _(not yet run)_ |
| Operator | _(not yet run)_ |
| Slack workspace (`SLACK_TEAM_ID`) | _(not yet run)_ |
| Keycloak realm (`REALM_ISSUER`) | _(not yet run)_ |
| Browser-login client id (`FCM_OIDC_CLIENT_ID`) | _(not yet run)_ |
| fcm service-account client id (`WHAGENT_CLIENT_ID`) | _(not yet run)_ |
| Link prompt observed (step 3) | _(not yet run)_ |
| No `slackthreadsession` row after the blocked mention (step 3) | _(not yet run)_ |
| `slackkeycloakidentity.id` (step 4) | _(not yet run)_ |
| `keycloak_iss` / `keycloak_sub` written (step 4) | _(not yet run)_ |
| `session_id` (step 6) | _(not yet run)_ |
| `on_behalf_of_iss` / `on_behalf_of_sub` / `on_behalf_of_kind` (step 7) | _(not yet run)_ |
| `subject_iss` / `subject_sub` / `subject_kind` (step 7) | _(not yet run)_ |
| Follow-up turn under service credential (step 8) | _(not yet run)_ |
| Allowlist reverted (step 9) | _(not yet run)_ |

When you do run it, replace every `_(not yet run)_` with the observed value and
remove this banner. The two columns that must differ — `on_behalf_of_sub` and
`subject_sub` — are the run's actual result; if they match, the delegated path
did not engage and the run is a fail.

### What does not count as evidence

- A local Tilt run. It has no real Slack workspace, no real realm, and runs
  whagent-net `api` with `GRPC_AUTH_MODE=none` and an empty allowlist.
- A mocked or SQLite-backed test. The suite under
  `friendly_computing_machine/tests/` covers these paths with fakes; it proves
  the code is shaped correctly, not that the deployment works.
- Anything inferred rather than observed. If you did not read the row, you did
  not record the subject.

A fabricated entry here is worse than a blank one: the next operator will trust
it.

## What is deliberately not covered

- **A `/link` slash command, and any unlink path.** M1 is an onboarding
  milestone with no new user-facing surface. Do not add one to make this
  runbook easier.
- **Keycloak realm provisioning.** The realm, its users, and fcm's confidential
  client are set up through normal release and human steps, not from this
  repository. `ENV.md` states the required client configuration; it does not
  create it.

## Open question for a human

**Without a `/link` command, a user can only link or relink by re-mentioning an
agent.** There is no command to force a fresh link for an already-linked user,
and no way to unlink at all.

That leaves a real question this runbook cannot answer by building something: is
re-mentioning a sufficient way to re-link, given that an already-linked user is
never re-prompted and so cannot change the identity they are bound to without
an out-of-band database edit? Someone who needs to switch Keycloak accounts, or
a user who linked the wrong identity, currently has no supported remedy.

**A human should confirm this is acceptable for M1 before the runbook is
considered complete.** The answer is a product decision, not a documentation
one, and it is deliberately not pre-empted here.

## Cross-check record

Every variable and endpoint cited above, verified against the code on this
branch (not against an earlier revision of the docs):

| Cited | Verified against | Result |
|---|---|---|
| `FCM_WEB_PUBLIC_URL` | `web/config.py`, `bot/handlers/whagent.py`, `ENV.md` | matches; read by both `web run` and `bot run-slack-socket-app` |
| `FCM_OIDC_ISSUER_URL` | `web/config.py`, `ENV.md` | matches; Authlib fetches `<issuer>/.well-known/openid-configuration` |
| `FCM_OIDC_CLIENT_ID` / `FCM_OIDC_CLIENT_SECRET` | `web/config.py`, `libs/python/cli/types.py`, `ENV.md` | match; distinct from `WHAGENT_CLIENT_ID` |
| `FCM_WEB_SESSION_SECRET` | `web/config.py`, `web/app.py` `SessionMiddleware` | matches; signs the cookie carrying the link token and OIDC `state`/`nonce` |
| `FCM_WEB_PORT` (default 8000) | `cli/web_cli.py` | matches |
| `WHAGENT_CLIENT_ID` / `WHAGENT_CLIENT_SECRET` | `whagent/client.py`, `ENV.md` | match; fcm's service account |
| `WHAGENT_KEYCLOAK_TOKEN_URL` | `whagent/client.py` `_fetch_token`, `ENV.md` | matches; `client_credentials` grant |
| `WHAGENT_API_URL` | `whagent/client.py` `grpc.insecure_channel` | matches; gRPC `host:port` |
| `WHAGENT_UI_PUBLIC_URL` | `temporal/whagent/workflow.py` | matches; fcm posts `<url>/sessions/<id>` |
| `WHAGENT_ON_BEHALF_OF_ALLOWED_CLIENT_IDS` | `whagent_net/api/main.go`, `api/handlers/start.go` + `session.go`, `whagent_net/ENV.md` | matches; read by `api`, empty = fail closed, `PERMISSION_DENIED` |
| `GRPC_AUTH_MODE` | `whagent_net/api/main.go`, `whagent_net/ENV.md` | matches; `none` (dev) vs `oidc` |
| `POSTGRES_URL` / `PG_DATABASE_URL` | `ENV.md` (FCM) / `whagent_net/ENV.md` | match; FCM tables in schema `fcm`, whagent-net `sessions` unqualified |
| `GET /link/{token}` | `web/app.py` | matches |
| `GET /link/callback` | `web/app.py`, `web/config.py` `callback_url` | matches; `${FCM_WEB_PUBLIC_URL}/link/callback` |
| `GET /health` | `web/app.py` | matches |
| Link token TTL of 10 minutes | `identity_dal.py` `LINK_TOKEN_TTL` | matches; also the wording in the prompt text |
| Tables `fcm.slacklinktoken`, `fcm.slackkeycloakidentity`, `fcm.slackthreadsession`, `fcm.slackchannelagentlink` | migration `c1d4e8a7b2f9`, `docs/whagent_integration.md` | match |
| whagent-net `sessions` columns `subject_*`, `on_behalf_of_*` | `whagent_net/migrate/schema/migrations/001_initial_schema.up.sql` | match |

`HELM_CHART_NAME` and `HELM_RELEASE_NAME` are read by `libs/python/logging/context.py`
and set by `tools/helm/templates/job.yaml.tmpl`; they are documented in
`libs/python/logging/README.md` and are **absent** from FCM's `ENV.md`
resource-attribute table. They are telemetry labels with no role in this flow,
so nothing above depends on them.
