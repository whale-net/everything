---
name: help
description: Not sure which krill-design or krill-work skill applies? Describe your situation or ask a question in plain language and this recommends the exact next skill and command — product, design, review, stakeholder-meeting, loop-design-panel, plan, implement, validate, loop-plan-implement-validate, or status — with the reasoning behind it. Use whenever you're unsure where a design session/plan stands or which command to run next; run `/status <id>` yourself instead if you already know the id and just want the raw state.
---

# help

Triage entry point shared by `krill-design` and `krill-work` (this file is
symlinked into both plugins' `skills/`, from `krill/plugin/shared/skills/help/`
— see `krill/plugin/shared/CONVENTIONS.md`). Dispatches the `help` persona so
the reasoning over CONVENTIONS.md and, when relevant, live design-session or
GitHub state stays out of this skill's own context — see AGENTS.md § Effective
Subagent Usage.

## Usage

```
/help "I just got a feature request, what do I do?"
/help "design session abc123 got a signoff, now what?"
/help "how do I tell if this needs product first?"
```

## Steps

1. Dispatch this plugin's `help` persona (`krill-design:help` when run from
   `krill-design`, `krill-work:help` when run from `krill-work` — same file,
   symlinked) via `Agent`, passing the requester's question verbatim plus any
   design-session id / product id / issue number / URL they mentioned.
   Default model: `sonnet` — this is bounded single-turn triage against a
   known decision table, not open-ended design work.
2. Relay its recommendation as-is: the exact command to run next, and its
   one-sentence reasoning. If it asked a clarifying question instead of
   recommending a command, relay that question to the requester rather than
   picking an answer for them.
