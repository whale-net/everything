# Capability map

Part of [`whagent_net/PRODUCT.md`](../PRODUCT.md). One line per capability, phrased as *a persona can do a thing*; no FRs live here. Milestones cite these ids in `Delivers` ([`03-roadmap.md`](03-roadmap.md)); an FR in a milestone plan cites the capability it serves. New capabilities are appended to `Later` with the next free `Cn` via an amendment (CONVENTIONS.md § Amendments), never renumbered.

### Now
C1 — An operator can start a session for a named agent, send it turns, and stop it, from Claude Code or any gRPC client.
C2 — An operator can read any session's full transcript — user turns, model messages, tool calls and results — while it runs and after it ends.
C3 — An operator can see a session's current state (running, awaiting input, done, stopped, failed, capped) and, for a finished session, why it ended.
C4 — A session survives worker restarts and deploys — including deploys that change the agent loop — resuming where it was with its transcript intact rather than starting over.
C5 — An operator can choose the model an agent uses and override it for a single session, from the models the configured provider (OpenRouter) serves.
C6 — A session stops itself on reaching its turn cap or cost cap and ends as "capped", distinct from "done"; defaults are 100 turns / $1, overridable per agent. Cost is always counted — estimated and marked as such when the provider reports none (LB6 is the source of truth for how).
C7 — A session can call tools on a domain-owned MCP server (first: `audience_score_system`'s) through the shared tool contract, seeing only the tools that server chooses to expose to it.
C8 — A signed-in human or a service account can run exactly the agents they are permitted to run: each agent names the Keycloak role it requires, and the caller's OIDC roles are the check.
C9 — Every tool call a session makes carries a whagent-net-signed claim of which agent is acting and on whose behalf, so a domain can verify it and its audit log can answer "which agent, for which user, did X."
C10 — A headless service can start and drive sessions as a service account, with the same permission checks as a human.
C11 — A tool call that mutates state is never applied twice when a turn is retried after a failure.
C12 — A consumer-domain developer can make their MCP server usable by a session by satisfying one published tool contract — verifying the on-behalf-of claim and mapping it to the domain's own user record — without changes to whagent-net.

### Next
C13 — An operator can pick an agent, start a session, and converse with it from a standalone web UI.
C14 — An on-call viewer can open any session in the web UI and watch it update live, without being able to steer it.
C15 — An operator can list sessions and find one by agent, state, who started it (human or service), and when.
C16 — A viewer can see how many turns and how much cost a session has used against its caps, and whether any of that cost is estimated.
C17 — A programmatic client can follow a session's events live as they happen, without needing message-bus credentials.
C18 — An operator can read a finished session's transcript long after it ended, once it has aged out of hot storage.
C27 — An operator can connect their Claude Code MCP client to whagent-net by signing in once through the browser, instead of manually copying a Keycloak token into their MCP client config.

### Later
C19 — A consumer-domain developer can embed the session component in their own Go web UI under their existing sign-in, with live updates driven from the domain's own event-bus subscription.
C20 — An ASS Creator / Analyst can run a research agent inside ASS's web UI that acts as them against ASS's MCP tools.
C21 — A session can start, drive, and read child sessions via the MCP surface, and the parent/child link is visible on both.
C22 — An operator can restrict which tools an agent may call per MCP server on the whagent-net side, independent of what the server exposes.
C23 — An operator can tighten or widen a running session's tool set mid-flight, with the change recorded in the session's history.
C24 — A viewer can see exactly what the model was shown on a given turn, to debug an agent's behaviour.
C25 — An operator can see cost and turn usage rolled up across sessions, per agent and per user.
C26 — A Slack user can start an agent and receive its replies in a Slack thread.
