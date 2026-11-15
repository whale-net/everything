---
name: krill-help
description: Not sure which krill skill applies? Describe your situation or ask a question in plain language and this recommends the exact next skill and command — krill-product, krill-design, krill-review, krill-stakeholder-meeting, krill-loop-design-panel, krill-plan, krill-implement, krill-validate, krill-loop-plan-implement-validate, or krill-status — with the reasoning behind it. Use whenever you're unsure where a design session/plan stands or which command to run next; run /krill-status <id> yourself instead if you already know the id and just want the raw state.
---

# krill-help

Triage entry point for krill design and work pipelines. Dispatches the
`krill-help` persona so the reasoning over `references/CONVENTIONS.md` and,
when relevant, live design-session or milestone state stays out of this skill's
own context.

## Usage

```
/krill-help "I just got a feature request, what do I do?"
/krill-help "design session abc123 got a signoff, now what?"
/krill-help "how do I tell if this needs product first?"
```

## Steps

1. Call `get_skill_resources` on `krill-help` for `["references/CONVENTIONS.md", "references/agents/help.md"]`.
2. Define the `krill-help` subagent if not already defined in this conversation
   via `define_subagent`:
   - `name`: `"krill-help"`
   - `description`: `"Read-only krill triage persona"`
   - `system_prompt`: the contents of `references/agents/help.md` (with `references/CONVENTIONS.md` as reference)
   - `enable_mcp_tools`: `true`
   - `enable_write_tools`: `false`
3. Dispatch `krill-help` via `invoke_subagent` (`TypeName: "krill-help"`, `Model: "inherit"`, `Workspace: "inherit"`), passing the requester's question verbatim plus any design-session id / product id / milestone id they mentioned.
4. Relay its recommendation as-is: the exact command to run next, and its
   one-sentence reasoning. If it asked a clarifying question instead of
   recommending a command, relay that question to the requester rather than
   picking an answer for them.
