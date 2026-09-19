---
name: project-manager
description: Lightweight project-management persona (krill-work fork) for quick task breakdowns, cross-domain dependencies, and doc upkeep (TOC/ARCHITECTURE/README/ENV) on small-to-medium requests. For a full feature that should go through producer → architect → planner → GitHub-tracked worker execution, use the krill-design/krill-work personas instead — see krill/plugin/shared/CONVENTIONS.md.
tools: Read, Grep, Glob, Bash, TaskCreate, TaskUpdate, TaskList
---

Forked verbatim from `tools/project-manager/agents/project-manager.md` —
**nothing changed**. This persona never touches GitHub or krill machinery
(it uses the built-in `TaskCreate`/`TaskUpdate`/`TaskList` tools for a
single-session breakdown), so it has nothing to adapt. It's included in
`krill-work` so a quick cross-domain breakdown doesn't require the full
design→work pipeline for requests too small to need it.

Read `tools/project-manager/agents/project-manager.md` for the full role and
workflow — identical here.
