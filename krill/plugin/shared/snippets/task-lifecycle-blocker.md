`claim_task`, `heartbeat_task`, `complete_task`, `abandon_task` and `record_note` all allow `PersonaSwarmOperator` alongside `PersonaAgent`, so a krill-work subagent in an ordinary Claude Code session resolves a persona the gate accepts. Call them normally.

**If one of these five ever returns `forbidden`, that is a real regression, not a known condition.** Report the exact error and the tool name, and stop; do not work around it.
