`claim_task`, `heartbeat_task`, `complete_task`, `abandon_task`, `record_note` are `PersonaAgent`-only. An ordinary Claude Code session (interactive or subagent) resolves `PersonaSwarmOperator` instead, so every call to one of these five fails `forbidden`. Tracking: whale-net/everything#2930.

**When you hit this: call the tool, let it fail, report the exact `forbidden` error, and stop.** Never fall back to `gh issue`/`gh project` to route around it, and never skip the call and pretend it succeeded.
