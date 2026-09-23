# Friendly Computing Machine — Product Brief

This file is the canonical entry point for FCM's product scope. Start here, then follow the jump table below.

| Section | File | When to read it |
|---|---|---|
| Current state | [product/01-current-state.md](product/01-current-state.md) | Before specifying any milestone: what exists, what is dead or half-built, and what is coupled to other domains |
| Capability map | [product/02-capability-map.md](product/02-capability-map.md) | To find the `Cn` a requirement traces to, or to see what is deliberately deferred |
| Roadmap | [product/03-roadmap.md](product/03-roadmap.md) | Before designing a milestone: its outcome sentence, `Delivers`, `Must not foreclose`, and `FR budget` |

Live milestone status is **not** in this file. It is tracked as `Ledger: M<n> → <status>` comments on the `Product: friendly_computing_machine` tracking issue. See `tools/project-manager/CONVENTIONS.md` § Roadmap ledger.

---

## Vision

FCM is the Slack front door to the `everything` monorepo. For the people in the workspace, it is a community bot and an AI assistant that lives where they already talk: it runs the music poll and ad-hoc polls, answers questions with the channel's context, and eventually holds real conversations. For the monorepo's services and agents, it is the one place that holds Slack credentials. They post notifications, ask a human a question, or receive a slash command through FCM instead of each one integrating with Slack. All of this runs on free tiers: FCM uses Slack's free Bolt/Socket Mode surface, never Slack's paid agent platform. Agent reasoning lives in whagent_net, not in FCM.

## Personas

- **Community member** — a person in the Slack workspace who uses the social and AI features (`/wai`, the music poll, `/poll`, and later AI conversations and game-server status).
- **Service / agent** — another monorepo system (manmanv2, whagent_net, deploy tooling, krill agents) that uses FCM to reach people in Slack.
- **Operator** — runs the FCM deployment. This is the same person who owns the workspace and needs it to be deployable and debuggable.

---

## Load-bearing decisions

### LB1 — Services reach Slack through one FCM-owned message contract

**At risk:** C5, C6 (`Next`, M2), C9, C10 (`Later`).

**Decide now:** FCM is the only system that holds Slack tokens. When M2 opens the service-to-Slack path, it is a single FCM-owned, versioned JSON contract on RabbitMQ, defined in one place, where new fields are added and existing ones are never changed. It is not one bespoke subscriber per producer, which is what the V1 `bot/subscribe` service is. The first version of the contract has three parts, even though M2 uses only the first two:

- a logical **route** name that FCM resolves through its `slackspecialchannel` tables, so producers never hold Slack channel IDs;
- a producer-scoped **message key**, so a repeat event edits the same Slack message instead of posting a new one. This is the generic form of `manmanstatusupdate.slack_message_id`;
- an optional **correlation ID and reply route**, so a human's answer (C9) or a routed slash command (C10) comes back on the same contract.

Translating a domain's events into Slack messages, such as turning manmanv2's `external` exchange events into notifications, is the job of an adapter that emits this contract. FCM takes no compile-time dependency on another domain's generated clients. That kind of coupling is why FCM's build breaks today when manman V1 changes. M1's V1 removal therefore keeps the special-channel tables and drops only the `manman_dev` usage: those tables become the route table.

**Stays cheap:** Block Kit rendering, which producers exist, whether the manmanv2 adapter runs in FCM's deployment or next to manmanv2, rate limiting, how an operator maps routes to channels, and C10's command registry. What is expensive later: once several services publish raw channel IDs in fire-and-forget payloads, adding edit-in-place or replies means changing every producer at the same time.

### LB2 — FCM holds no agent state; whagent_net owns conversations

**At risk:** C7 (`Next`, M3), C11 (`Later`).

**Decide now:** M1 stabilizes `/wai` in its current form: a one-shot answer over channel context. `genaitext` stays an audit log, not conversation memory. No milestone adds multi-turn history, tool calling, or agent definitions to FCM. When M3 arrives, the only conversation state FCM stores is a mapping from a Slack thread (channel plus `thread_ts`) to a whagent_net session ID. Transcripts, turns, caps, and tools stay in whagent_net (its LB1 and LB5). Replies reach Slack because FCM consumes whagent_net's session event exchange (its LB7). FCM does not poll for replies or keep its own copy of the transcript.

**Stays cheap:** the `/wai` prompt chain, the Gemini SDK and model upgrade, whether `/wai` is later re-implemented as a whagent agent, and how thread replies are rendered. What is expensive later: a second conversation store in FCM would have to be migrated into whagent_net's transcript record or reconciled with it, and in the meantime Slack users would see two assistants with different memories.

### LB3 — The Slack user is the downstream principal, never FCM's service account

**At risk:** C7 (M3), C8, C9, C11 (`Later`).

**Decide now:** when FCM acts in another system for a person, it authenticates as its own service account and names the Slack user as the on-behalf-of subject. It uses whagent_net's `(iss, sub, kind)` shape (its LB2): `iss` comes from the Slack team ID, `sub` is the Slack user ID, and `kind` is human. The user sync (C3) already records each user's team, and M1 must keep doing so. FCM never starts a whagent session or a manmanv2 action under its own identity for a request that a human triggered.

**Stays cheap:** linking Slack users to Keycloak accounts, each system's authorization policy (for example, who may restart a server), and display names. What is expensive later: sessions and actions recorded under FCM's identity can never be re-attributed to the person who asked. Every "who did this" rollup and every per-person permission would then have to start with no history.

---

## Non-goals

- **manman V1 support.** FCM will not integrate with or maintain compatibility with the legacy manman V1 system. Game-server features target manmanv2 only.
- **Slack's paid AI/agent platform.** FCM uses the free Bolt/Socket Mode API. It does not use Slack-hosted agents, Slack AI, or any feature that needs a paid Slack plan.
