# Roadmap

Part of the [FCM product brief](../PRODUCT.md). Milestone definitions only.

Milestone status is not recorded in this file. Live status is tracked as `Ledger: M<n> → <status>` comments on the `Product: friendly_computing_machine` tracking issue; take the **last** `Ledger:` comment per milestone as current.

`Ships alongside` names non-capability work (removals, defect fixes, schema shapes) that a milestone must land for its outcome sentence to be true. Nothing in it appears in the capability map.

## M1 — Community members keep using `/wai` and the music poll on an FCM the operator can deploy and debug from accurate docs, with manman V1 gone

This is an onboarding milestone: existing behavior only, no new user-facing features.

```
Delivers: C1, C2, C3, C4
Ships alongside: removal of every manman V1 dependency (server-control actions/shortcuts/modal,
  `/test`, the V1 subscribe service, `//generated/py/manman:*` deps); removal of dead code
  (commented-out tasks, `SayHello`, deprecated direct-Gemini path, `MyClass` scaffold);
  ARCHITECTURE.md / ENV.md / README.md / docs/* rewritten to match the code; the
  `slackspecialchannel` tables kept through the V1 removal (LB1 route table)
Must not foreclose: LB1, LB2, LB3
Deliberately deferred: manmanv2 status in Slack (C6 → M2), whagent_net-backed AI (C7 → M3),
  generic notifications (C5 → M2), everything in Later
FR budget: 12
```

## M2 — A community member sees manmanv2 server status in Slack, delivered through a notification path any service can use

```
Delivers: C5, C6
Must not foreclose: LB1
Deliberately deferred: controlling servers from Slack (C8 → Later), agent questions (C9 → Later)
FR budget: 12
```

## M3 — A community member can hold a multi-turn AI conversation in Slack run by whagent_net

```
Delivers: C7
Must not foreclose: LB2, LB3
Deliberately deferred: assistant tool actions (C11 → Later), agent-initiated questions (C9 → Later)
FR budget: 12
```

## Later coverage

Every `Later` capability is protected by a load-bearing decision:

- **C8** (server control from Slack) needs LB3, so that manmanv2 can authorize the person rather than FCM.
- **C9** (ask a human) needs LB1's correlation ID and reply route, and LB3 for who answered.
- **C10** (routed slash commands) needs LB1's correlation ID and reply route.
- **C11** (assistant actions) needs LB2, because tools live in whagent_net, and LB3, because actions carry the Slack user as the on-behalf-of subject.
