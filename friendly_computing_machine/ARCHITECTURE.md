# Friendly Computing Machine — Architecture

> System design for the Slack bot, its Temporal workflows, and the OIDC identity-link web app.
> Read this before adding new commands, workflows, or integrations.

## Overview

Friendly Computing Machine is a Slack bot (Socket Mode) that turns `@mention`s into whagent-net AI
sessions, runs ad-hoc polls, and orchestrates game-server work through ManMan. Slack-facing commands
are quick; anything long-running is handed to a Temporal workflow so it survives restarts and can be
queried later.

The bot also exposes one small inbound HTTP surface — the **identity-link web app** — used only to bind
a Slack user to their Keycloak identity so whagent-net's `on_behalf_of` delegation can act as them.
An `@mention` in an agent-linked channel is gated on that binding: a user with no stored mapping is
prompted with a one-time link (see below) instead of being given a session.

## Components

| Component | Entry point | Role |
|---|---|---|
| Bot (Socket Mode) | `bot run-slack-socket-app` | Slack event handling; queues work, posts replies. |
| Task pool | `bot run-taskpool` | Background execution for bot tasks. |
| Workflow worker | `workflow run` | Temporal worker that runs the long-running workflows and calls whagent-net. |
| Subscriber | `subscribe run` | Consumes ManMan RabbitMQ status notifications. |
| Identity-link web | `web run` | FastAPI app that performs the browser OIDC login and writes the Slack→Keycloak mapping. |
| Migration job | `migration run` | Applies Alembic migrations. |

### Identity-link web app (`web`)

`friendly_computing_machine/src/friendly_computing_machine/web/` is a small FastAPI app. It performs a
standard OIDC authorization-code login against the Keycloak realm whagent-net uses, via Authlib's
`authlib.integrations.starlette_client.OAuth`. It serves only static redirect/result pages — no HTMX,
no rich UI.

Two tables back the flow (see `db/dal/identity_dal.py`):

- **`slacklinktoken`** — a one-time, short-lived (10 min) link token minted for a Slack user. It carries
  a link attempt across the Keycloak redirect so the callback can identify *which* Slack identity to
  bind without trusting anything from the browser.
- **`slackkeycloakidentity`** — the durable one-row-per-Slack-user mapping from
  `(slack_team_id, slack_user_id)` to `(keycloak_iss, keycloak_sub)`.

#### Link flow

1. `GET /link/{token}` — validates the token with `peek_link_token`. An unknown, expired, or already
   consumed token renders a static error page (no redirect). A valid token is stashed in the Starlette
   session (alongside Authlib's own OIDC `state`/`nonce`) and the browser is redirected to Keycloak's
   authorize endpoint.
2. `GET /link/callback` — completes the code exchange with `authorize_access_token`. Authlib verifies
   the ID token (no custom verification). The verified `iss`/`sub` claims are read and
   `complete_link(token, iss, sub)` is called, which consumes the token and upserts the mapping in a
   single transaction. The session is cleared afterwards.

Guarantees:
- The DB row is the sole authority for the Slack binding — the session only carries a reference to it.
- On any failure or abandonment mid-flow, nothing is written: consumption and the mapping write happen
  in one transaction, so a failed or replayed link leaves the DB unchanged.
- No access, refresh, or ID token is ever persisted or logged; only the `(iss, sub)` pair is stored.
- A successful link logs at INFO with the Slack team/user ids and `iss`/`sub` (never tokens).

#### Link gating (bot)

`bot/handlers/whagent.py`'s `handle_whagent_app_mention` is **block-until-linked**. After the channel's
agent link resolves and *before* any workflow is started, it looks up `get_keycloak_identity(team_id,
user_id)`. If the mentioning user has no stored mapping it:

1. mints a one-time token with `mint_link_token(team_id, user_id)`,
2. posts a Slack `chat_postEphemeral` (visible only to that user) containing a clickable
   `${FCM_WEB_PUBLIC_URL}/link/<token>` link, and
3. returns without starting anything.

Because the check runs before `start_workflow` (and before any `slackthreadsession` row is written), a
blocked mention never leaves an orphaned `ACTIVE` thread-session row. The prompt is self-contained — no
slash command or out-of-band instruction. A prompt issuance logs at INFO with the Slack team/user ids
(never the token). If `FCM_WEB_PUBLIC_URL` is unset the mention is still blocked and an ERROR is logged.
Multi-participant attribution inside an already-linked thread is out of scope.

## Integrations

- **whagent-net** — `@mention`-triggered AI sessions. The bot queues a turn, the workflow worker calls
  whagent-net's `api` over gRPC with a service-account Keycloak token, and the reply is posted back to
  the Slack thread. See [docs/whagent_integration.md](docs/whagent_integration.md).
- **ManMan** — `subscribe` consumes status notifications over RabbitMQ. See
  [docs/manman_subscribe.md](docs/manman_subscribe.md).
- **Keycloak** — whagent-net service accounts (`WHAGENT_*`) and the web app's confidential browser-login
  client (`FCM_OIDC_*`) both authenticate against the same realm. See [ENV.md](ENV.md).
- **ArgoCD** — deploy notifications. See [docs/argocd-integration.md](docs/argocd-integration.md).

---

# Current-state survey

> **A dated snapshot, not a live description.** Taken **2026-09-23** from the
> code; re-verified **2026-09-26** against `main` at `66d710b4`. Every claim
> below is labelled **still true**, **drifted** (the survey's 2026-09-23
> answer no longer holds), or **added since** (post-dates the survey). Where
> the survey was wrong, the correction is stated rather than the old answer
> quietly dropped — a survey that hides its own errors cannot be trusted on
> the claims it does not flag.
>
> This content used to live in `product/01-current-state.md`, which
> `krill/render` replaces with a placeholder. It is hand-authored here
> instead: a current-state survey of a running system is not a spec of
> record, and krill's renderer is scoped to the product doc set on purpose
> (see `krill/render/README.md`).
>
> **FCM domain owner: this is a proposal on an unmerged branch, not a landed
> edit.** Correct anything that has drifted since 2026-09-26 before landing
> it — FCM is mid-migration off manman V1 right now, and this file is
> describing a system that is actively moving.

## Features (paths under `src/friendly_computing_machine/`)

- **`/wai` AI command** — **still true, one correction.** `bot/handlers/commands.py`
  still registers `@app.command("/wai")`, logs the prompt to `genaitext`, and
  calls `SlackContextGeminiWorkflow` **synchronously from the bolt handler**
  rather than starting it and returning. The workflow
  (`temporal/slack/workflow.py`) still gathers channel context, then runs
  summary, vibe, prompt, and Gemini steps, followed by call-to-action
  detection and tag fixing.
  **Drifted:** the survey's central claim that this used the deprecated,
  unpinned `google.generativeai` SDK with the default model no longer holds —
  `google.generativeai` has **zero hits** anywhere in the tree. What backs
  generation now was not traced; check before relying on either statement.
- **Music poll** — **still true.** `bot/task/musicpoll.py` posts the poll
  weekly, processes it hourly, and archives daily with 89- and 30-day
  windows, plus a one-off init. It runs on the **custom task pool**, not
  Temporal: `bot/task/taskpool.py` imports it directly, and no Temporal
  workflow references it. `bot/handlers/events.py` stores only messages from
  music-poll channels; `message_changed` is not implemented. The poll text is
  hardcoded.
- **Ad-hoc polls** — **still true.** `/wpoll` in `bot/handlers/poll.py` posts
  a Simple Poll-style Block Kit poll. Votes and closes arrive over Socket
  Mode; each vote is written to `pollvote` and the message re-rendered with
  `chat.update`. See `docs/poll.md`.
- **Custom task pool** — **still true.** `bot/task/taskpool.py` is a
  sleep-loop scheduler writing to `task` and `taskinstance`. Only the four
  music-poll tasks remain; `genai.py`, `slack_qod.py`, and `find*.py` are
  present, commented as "migrated to temporal", and dead.
- **Temporal schedules** — **still true, list changed.** `temporal/worker.py`
  upserts a schedule per registered workflow subclassing
  `AbstractScheduleWorkflow`. `WORKFLOWS` is now `SayHello`,
  `SlackContextGeminiWorkflow`, `SlackMessageQODWorkflow`,
  `SlackUserInfoWorkflow`, `SlackThreadAgentWorkflow` — the survey named
  only the middle three plus `SayHello`. The `SayHello` sample
  (`temporal/sample.py`) is still registered and is still dead.
- **manman V1 server control** — **still present, and the survey's warning
  about it is the sharpest thing here.** `bot/handlers/actions.py`,
  `shortcuts.py`, and `views.py` still provide start/stop/restart/stdin
  buttons, a server-select modal, and a shortcut commented "UNUSED?", all
  calling the V1 experience API (`manman/api.py`,
  `//generated/py/manman:*`). `/test` is still a live debug command opening
  the same modal. FCM is mid-removal of this; treat it as **going away, not
  yet gone**.
- **manman V1 subscribe** — **still present, also going away.**
  `bot/subscribe/service.py` consumes `fcm-{env}.manman.generic.status` on
  the V1 `external_service_events` exchange, upserting `manmanstatusupdate`
  and posting or editing Slack messages. The target channel type is still
  hardcoded to `"manman_dev"`; the channels for that type are looked up in
  the `slackspecialchannel` tables, not hardcoded. The `subscribe` release
  app is still declared in `BUILD.bazel`.
- **"ArgoCD integration" is a stale doc, not a feature** — **still true.**
  `docs/argocd-integration.md` describes Helm values that do not exist under
  `tools/`.
- **Identity-link web app** (`web run`) — **added since the survey.** A
  FastAPI app doing the browser OIDC login against Keycloak and writing the
  Slack→Keycloak mapping. Documented in full at the top of this file; it is
  the sixth `release_app` and the only one with a health check.
- **CLI** (`cli/`) — **drifted.** As surveyed: `bot run-slack-socket-app |
  run-taskpool | send-test-command | who-am-i`, `workflow run | test`,
  `subscribe run`, `migration run`, and a leftover
  `tools_cli.update_helm_chart_version` from the old standalone repo. Since
  2026-09-23 a `web` command has been added.

## Runtime shape

- **Deployments — drifted on the count.** `BUILD.bazel` declares **six**
  `release_app`s: `bot`, `taskpool`, `subscribe`, `worker`, `web`, and the
  `migration` Job. The survey said five, which was correct before `web`
  landed. All are built from the `fcm_cli` binary and run one replica each.
  **`health_check_enabled` is no longer uniformly `False`:** `bot`,
  `taskpool`, `subscribe`, and `worker` are `False`; `web` is `True`. They
  compose into the helm chart `bot-services` (namespace `fcm`) and publish to
  `ghcr.io/whale-net/friendly-computing-machine-*`. Local dev runs through
  the `Tiltfile`.
- **Datastore — drifted on the count.** Postgres schema `fcm`. The survey
  recorded 17 tables from 12 migrations; as of 2026-09-26
  `src/migrations/versions/` holds **15 migrations** (migrations also moved
  out from under `friendly_computing_machine/db/` to `src/migrations/`).
  None of the tables are SCD2.

  | Area | Tables |
  |---|---|
  | Slack | `slackteam`, `slackuser`, `slackchannel`, `slackmessage`, `slackcommand`, `slackspecialchanneltype`, `slackspecialchannel` |
  | AI | `genaitext` |
  | Music poll | `musicpoll`, `musicpollinstance`, `musicpollresponse` |
  | Ad-hoc poll | `poll`, `polloption`, `pollvote` |
  | Task pool | `task`, `taskinstance` |
  | manman | `manmanstatusupdate` |
  | Identity link (added since) | `slacklinktoken`, `slackkeycloakidentity` |
  | Agent links (added since) | `slackchannelagentlink`, `slackthreadsession` |

  `manmanstatusupdate` has **not** yet been dropped on `main` — the drop is
  part of the in-flight V1 removal and `helm rollback` after it is a one-way
  door (see `docs/deploy_recovery.md`).
- **External dependencies — one resolved, one added.**
  - Slack (bot and app tokens) — current.
  - Temporal (a single `main` task queue, sandbox passthrough on for all
    modules) — current; `temporal/worker.py` runs a `SandboxedWorkflowRunner`
    over a 100-slot `ThreadPoolExecutor`.
  - RabbitMQ (the V1 `external_service_events` exchange) — current, and
    still load-bearing for `subscribe`. `//libs/python/rmq` is still a BUILD
    dep of both `bot/BUILD.bazel` and `cli/BUILD.bazel`.
  - Gemini (`GOOGLE_API_KEY`) — the SDK is gone (see `/wai` above); the
    `libs/python/cli/providers/gemini` config provider is not.
  - The manman V1 HTTP APIs — current, and going away with the rest of V1.
  - Keycloak and whagent-net — **added since**; see Integrations above.
- **Shared libraries — still true.** `libs/python/{alembic,
  cli/providers/*, logging, rmq}`. The `cli/providers/*` set is now
  `app_env`, `gemini`, `postgres`, `rabbitmq`, `slack`, `temporal`.

## Tests and docs

- **Tests — drifted.** The survey recorded 5 `py_test` targets and about 29
  tests covering Slack models, block rendering, the DB util, manman util and
  subscribe parsing, the abstract task, and one workflow fix. `tests/` has
  grown substantially since — including a 641-line `test_wai_command.py` and
  new poll, Slack-sync, and route-table suites. It remains true that there
  are **no tests for Temporal workflow integration or music-poll logic on
  the task pool**. Re-read `tests/BUILD.bazel` rather than trusting either
  count.
- **Docs.** `ENV.md` is now the maintained environment reference. The
  survey's complaints — `README.md` describing `uv sync` and `fcm bot run`,
  `docs/microservices-architecture.md` and `docs/slack_tokens.md`
  referencing removed commands, the real env list living only in
  `.env.example` — are dated; check them before repeating them.

## Coupling

- **FCM → manman V1 — still true, and the load-bearing compile dependency.**
  FCM has a compile-time dependency on
  `//generated/py/manman:{experience,status,worker_dal}_api` and a runtime
  dependency on V1's `external_service_events` exchange.
  `manman/src/models.py:321` points back at FCM's subscribe doc. **If V1 is
  retired or its specs change, FCM's build breaks.** This is the strongest
  argument for finishing the removal, and it is not optional.
- **FCM → manmanv2.** No integration. v2 publishes to a different exchange
  (`external`, routing keys `manman.#`) with different payloads.
  `manmanv2/product/03-roadmap.md` says `libs/go/temporal` is "already used
  by friendly_computing_machine", which is **incorrect** — FCM uses the
  Python SDK, not `libs/go/temporal`.
- **FCM → whagent-net.** `@mention` AI sessions via the workflow worker's
  gRPC call, now gated on the identity link. whagent_net's brief named a
  "Slack user" persona not yet wired through FCM; that wiring is what the
  identity-link app adds, and it is the consumer that overlaps with `/wai`.
- **Inbound breakage — drifted, and the survey's version of this is wrong.**
  The survey said FCM breaks when these change:
  - `libs/python/cli/providers/*`, `libs/python/alembic`, and
    `libs/python/rmq` — **still true**, all three are still BUILD deps.
  - the release tooling, naming `tools/release_helper_go`'s `FCM_APPS` and
    `tools/bazel/container_image.bzl`. **Both are stale.** `FCM_APPS` no
    longer exists as a constant — the only surviving mention is a comment in
    `tools/release_helper_go/cmd/discover_fast.go` listing it among other
    apps' constants — and `container_image.bzl` now mentions `fcm_cli` only
    in docstring examples, not as a special case. FCM is discovered by the
    generic path. **Drop these two; do not go looking for the special case
    that is no longer there.**

## Risks

- **Two schedulers coexist, the custom task pool and Temporal** — **still
  true, and still the top risk.** The migration stalled halfway: the music
  poll is still driven by `bot/task/taskpool.py` while `temporal/worker.py`
  upserts its own schedules. Two systems own "run this on a schedule."
- **Deprecated unpinned `google.generativeai`** — **resolved.** Zero hits in
  the tree. Closed out; do not re-open without finding what replaced it.
- **`/wai` blocks the bolt handler on a synchronous workflow** — **still
  true, and arguably worse now that the flow is shared.** The handler calls
  the workflow directly rather than starting it and returning. All workflows
  share one queue, so a slow `/wai` delays every other mention.
- **Configuration is hardcoded** — **still true.** The `"manman_dev"` channel
  type and the poll templates are both literals in code.
- **Process-global singletons live in `bot/app.py`** — **still true.**
  `_app_instance`, `bot_config_lock`, and `agent_link_cache_lock` are all
  module-level, so two app instances in one process would share them.
- **No health checks, thin tests, skeletal or wrong docs** — **partly
  improved.** `web` has `health_check_enabled = True`; the other five do
  not. The test suite has grown. The doc drift is the surviving half.

## Re-verifying this survey

Every claim above was checked against `main` at `66d710b4` on 2026-09-26. To
re-check it yourself:

```bash
cd friendly_computing_machine
grep -rl "google.generativeai" src/                 # resolved: expect no hits
grep -rn "libs/python/rmq" --include=BUILD.bazel .  # still a dep
grep -c "^release_app(" BUILD.bazel                 # 6
grep -n "health_check_enabled" BUILD.bazel          # 4 False, web True
grep -rn "FCM_APPS" ../tools/release_helper_go/      # stale: comment only
find src -path "*versions*" -name "*.py" | wc -l    # 15
grep -n "WORKFLOWS = \[" -A 8 src/friendly_computing_machine/temporal/worker.py
grep -n "musicpoll" src/friendly_computing_machine/bot/task/taskpool.py
ls src/friendly_computing_machine/bot/handlers/     # V1 files still here
```
