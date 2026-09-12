-- Renames agent_definition's `domain` column (007_agent_definition_domain)
-- to `scope` and drops its NOT NULL constraint.
--
-- Why: "domain" collided with this repo's much more common business-domain
-- vocabulary (AGENTS.md's Domains table -- manmanv2, audience_score_system,
-- etc.) while actually naming a narrower concept -- the one grant-scope
-- whagent_net/grantkey.ForScope derives a delegated-grant key from. "scope"
-- names that concept directly without borrowing an already-overloaded word.
--
-- Why nullable: 007 made this column required so every agent definition had
-- exactly one domain to scope its delegated grant to. That forced every
-- agent definition into delegated-grant scoping even when an operator just
-- wants to run an agent with its own configured tool_set and no
-- cross-domain grant involved at all. A NULL scope now means exactly that:
-- no delegated-grant key is ever derived or checked for this agent
-- definition (whagent_net/grantkey.ForScope is simply never called), but
-- the agent still runs with whatever tool_set is configured for it.
--
-- No data loss: every existing row's domain value carries over unchanged
-- under the new column name; no row's value is dropped or blanked.
ALTER TABLE agent_definition
    RENAME COLUMN domain TO scope;

ALTER TABLE agent_definition
    ALTER COLUMN scope DROP NOT NULL;
