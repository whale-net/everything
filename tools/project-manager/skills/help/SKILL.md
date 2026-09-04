---
name: help
description: Not sure which project-manager skill applies? Describe your situation or ask a question in plain language and this recommends the exact next skill and command — product, design, review, plan, implement, validate, loop-plan-implement-validate, status, or stakeholder-meeting — with the reasoning behind it. Use whenever you're unsure where a plan/product stands or which command to run next; run `/project-manager:status <n>` yourself instead if you already know the issue number and just want the raw state.
---

# help

Triage entry point for the pipeline. Dispatches the `project-manager:help` persona so the (often multi-step) reasoning over CONVENTIONS.md and, when relevant, live GitHub state stays out of this skill's own context — see AGENTS.md § Effective Subagent Usage.

## Usage

```
/project-manager:help "I just got a feature request, what do I do?"
/project-manager:help "plan #123's board is all Done, now what?"
/project-manager:help "how do I tell if this needs product first?"
```

## Steps

1. Dispatch `project-manager:help` via `Agent`, passing the requester's question verbatim plus any issue/discussion/Project number or URL they mentioned. Default model: `sonnet` — this is bounded single-turn triage against a known decision table, not open-ended design work.
2. Relay its recommendation as-is: the exact command to run next, and its one-sentence reasoning. If it asked a clarifying question instead of recommending a command, relay that question to the requester rather than picking an answer for them.
