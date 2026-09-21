@AGENTS.md

## Subagent Forking

Do not fork subagents (`Agent` with `subagent_type: "fork"`) in this repo.
A fork inherits the parent's full transcript and re-pays that large
cache-read cost on every turn it takes — cheap-looking per token, but it
adds up fast across many turns. Use a regular (non-fork) subagent instead;
see AGENTS.md's "Effective Subagent Usage" section for why.
