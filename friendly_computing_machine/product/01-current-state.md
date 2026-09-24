# Current state

Part of the [FCM product brief](../PRODUCT.md). Surveyed 2026-09-23 from the code. Where this file disagrees with `README.md` or `docs/*`, trust this file: those docs are stale.

FCM is a Python Slack bot built on slack-bolt (Socket Mode), SQLModel/Alembic, the Temporal Python SDK, and amqpstorm. Active development stopped in October 2025, after the service split, the CLI refactor, and the manman split (#245/#247/#249/#271). Every later commit was a repo-wide change. v0.2.0 of all five images was still released on 2026-08-19. The newest migration is from 2025-05-28.

## Features (paths under `src/friendly_computing_machine/`)

- **`/wai` AI command** (live). The handler in `bot/handlers/commands.py` logs the prompt to `genaitext`. It then calls `SlackContextGeminiWorkflow` synchronously from the bolt handler. That workflow (`temporal/slack/workflow.py`) gathers channel context, then runs summary, vibe, prompt, and Gemini steps, followed by call-to-action detection and tag fixing. It uses the deprecated `google.generativeai` SDK with the default model and no pinned version (`temporal/ai/activity.py`). The older direct path in `gemini/ai.py` is marked `@deprecated` and is dead.
- **Music poll** (live, on the task pool). `bot/task/musicpoll.py` posts the poll weekly, processes it hourly, and archives daily with 89- and 30-day windows. It also has a one-off init. `bot/handlers/events.py` stores only messages from music-poll channels. `message_changed` is not implemented. The poll text is hardcoded.
- **Ad-hoc polls** (live). `/wpoll` in `bot/handlers/poll.py` posts a Simple Poll-style Block Kit poll. Vote and close buttons arrive over Socket Mode; each vote is written to `pollvote` and the message is re-rendered with `chat.update`. See `docs/poll.md`.
- **Custom task pool** (live, legacy). `bot/task/taskpool.py` is a sleep-loop scheduler that writes to `task` and `taskinstance`. Only the four music-poll tasks remain. `genai.py`, `slack_qod.py`, and `find*.py` are commented out as "migrated to temporal" and are dead.
- **Temporal schedules** (live). `temporal/worker.py` upserts two schedules. `SlackMessageQODWorkflow` runs every 2 minutes to backfill IDs and dedupe messages. `SlackUserInfoWorkflow` runs every 30 minutes. The `SayHello` sample workflow (`temporal/sample.py`) is still registered and is dead.
- **manman V1 server control** (half-built). `bot/handlers/actions.py`, `shortcuts.py`, and `views.py` provide start/stop/restart/stdin buttons, a server-select modal, and a shortcut commented "UNUSED?". All of them call the V1 experience API (`manman/api.py`, `//generated/py/manman:*`). `/test` opens the same modal: a live debug command.
- **manman V1 subscribe** (live, V1 only). `bot/subscribe/service.py` consumes `fcm-{env}.manman.generic.status` on the V1 `external_service_events` exchange. It upserts `manmanstatusupdate` and posts or edits Slack messages. The target channel type is hardcoded to `"manman_dev"`. The channels for that type are looked up in the `slackspecialchannel` tables, not hardcoded.
- **"ArgoCD integration"** is a stale doc, not a feature. `docs/argocd-integration.md` describes Helm values that do not exist under `tools/`.
- **CLI** (`cli/`):
  - `bot run-slack-socket-app | run-taskpool | send-test-command | who-am-i`
  - `workflow run | test`
  - `subscribe run`
  - `migration run`
  - `tools_cli.update_helm_chart_version` is leftover from the old standalone repo.

## Runtime shape

- **Deployments.** `BUILD.bazel` defines five `release_app`s: `bot`, `taskpool`, `subscribe`, `worker`, and the `migration` Job. All five build from one binary, `//friendly_computing_machine/src:fcm_cli`. Each runs one replica with `health_check_enabled=False`. They are composed into the helm chart `bot-services` (namespace `fcm`) and published to `ghcr.io/whale-net/friendly-computing-machine-*`. Local dev runs through the `Tiltfile`.
- **Datastore.** Postgres schema `fcm` holds 17 tables from 12 migrations. None of them are SCD2.

  | Area | Tables |
  |---|---|
  | Slack | `slackteam`, `slackuser`, `slackchannel`, `slackmessage`, `slackcommand`, `slackspecialchanneltype`, `slackspecialchannel` |
  | AI | `genaitext` |
  | Music poll | `musicpoll`, `musicpollinstance`, `musicpollresponse` |
  | Ad-hoc poll | `poll`, `polloption`, `pollvote` |
  | Task pool | `task`, `taskinstance` |
  | manman | `manmanstatusupdate` |
- **External dependencies.**
  - Slack (bot and app tokens)
  - Temporal (a single `main` task queue, sandbox passthrough on for all modules)
  - RabbitMQ (the V1 exchange)
  - Gemini (`GOOGLE_API_KEY`)
  - the manman V1 HTTP APIs
- **Shared libraries.** `libs/python/{alembic,cli/providers/*,logging,rmq}`.

## Tests and docs

- **Tests.** `tests/BUILD.bazel` has 5 `py_test` targets with about 29 tests. They cover Slack models, block rendering, the DB util, manman util and subscribe parsing, the abstract task, and one workflow fix. There are no tests for handlers, workflow integration, or music-poll logic.
- **Docs.** `ARCHITECTURE.md` and `ENV.md` are TODO skeletons. `README.md` describes `uv sync` and `fcm bot run`, but neither exists any more. `docs/microservices-architecture.md` and `docs/slack_tokens.md` reference removed commands. The real env var list exists only in `.env.example` and the CLI providers.

## Coupling

- **FCM → manman V1.** FCM has a compile-time dependency on `//generated/py/manman:{experience,status,worker_dal}_api` and a runtime dependency on V1's `external_service_events` exchange. `manman/src/models.py:321` points back at FCM's subscribe doc. If V1 is retired or its specs change, FCM's build breaks.
- **FCM → manmanv2.** There is no integration. v2 publishes to a different exchange (`external`, routing keys `manman.#`) with different payloads. `manmanv2/product/03-roadmap.md` says `libs/go/temporal` is "already used by friendly_computing_machine", which is incorrect.
- **whagent_net.** Its brief names a "Slack user" persona that is not yet wired through FCM. That persona is the future consumer that overlaps with `/wai`.
- **Inbound breakage.** FCM breaks when these change:
  - `libs/python/cli/providers/*`, `libs/python/alembic`, and `libs/python/rmq`
  - the release tooling (`tools/release_helper_go` `FCM_APPS` and `tools/bazel/container_image.bzl`), which special-cases FCM paths

## Risks

- Two schedulers coexist, the custom task pool and Temporal. The migration between them stalled halfway.
- `google.generativeai` is deprecated and no model is pinned.
- `/wai` blocks the bolt handler on a synchronous workflow. All workflows share one queue.
- Configuration is hardcoded: the `"manman_dev"` channel type and the poll templates.
- Process-global singletons live in `bot/app.py`.
- There are no health checks, tests are thin, and the docs are skeletal or wrong.
