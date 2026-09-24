---
name: plan
description: Task breakdown (krill-work fork) — converts a krill-design design session that ended in a signoff revision event (signoff_status approved) into real krill Task entities when a Milestone exists (M3/M4), by dispatching the planner persona; no GitHub tracking issue or Project board on that path. Falls back to project-manager's GitHub Project mechanics only when no krill Milestone scopes the work. Idempotent. Run after /krill-design:review (or /krill-design:loop-design-panel) approves the design, before /krill-work:implement. Also the right target for "just create the tasks, don't start work" / "plan only".
---

# plan

Turns a signed-off krill design into krill `Task` entities, by dispatching
`krill-work:planner`. Pure task breakdown — no code written, no branches
touched.

## Usage

```
/krill-work:plan <feature-set-id> --milestone-id <milestone-id>   # krill-hosted product: mints real Task entities, no GitHub
/krill-work:plan <feature-set-id>                                 # no krill Milestone: GitHub Project fallback (project-manager mechanics)
/krill-work:plan <feature-set-id> --milestone-id <milestone-id> --planner-model sonnet
```

`--planner-model <model>` — same meaning as project-manager's, default `opus`.
`--milestone-id <id>` — pass when this FeatureSet's design was scoped to a
Milestone already authored in krill (`create_milestone`, M3 — only possible
for a product actually hosted in krill, e.g. krill's own domain or an
imported one). Without it, `planner` falls back to `tools/project-manager`'s
GitHub Issues/Project mechanics entirely (CONVENTIONS.md "Work axis") — this
is a real krill capability gap (no Task container exists outside a
Milestone), not a default worth avoiding when it doesn't apply.

## Steps (Milestone path)

1. **Confirm and idempotency-check.** Call `get_feature_set_slice {id}` and
   confirm its design session's last event was `signoff` with
   `signoff_status: approved` (if you only have the design-session id,
   `get_design_session` gives you both). If not, point the user to
   `/krill-design:design`, `/krill-design:review`, or
   `/krill-design:loop-design-panel`. Call `get_milestone {id}` to confirm
   the Milestone exists and belongs to the same Product. For idempotency,
   don't trust bare `get_milestone_status {id}` alone: `planned` is
   ambiguous on this path — `/krill-design:design`/`review`/
   `loop-design-panel` all set a krill-hosted milestone to `planned` the
   moment its design signs off, before any task exists, and `planner` sets
   the same status again once it actually creates tasks (there is no
   separate status value for the two). Call
   `get_milestone_status_history {id}` and read the note on the latest
   `planned`-or-later transition instead — `planner` always names the
   created task ids in that note (`agents/planner.md` step 4); a note that
   names task ids means a prior `plan` run already created them, ask the
   user for that run's task manifest (there is no krill query to
   reconstruct it — CONVENTIONS.md) rather than re-running `planner`. A
   note that names a design-session/signoff event instead (no task ids)
   means this is genuinely the first planning pass — proceed. If the note
   is ambiguous, ask the user to confirm before dispatching `planner`.
2. **Task breakdown.** Dispatch `krill-work:planner` — via `Agent` with
   `model` set to `--planner-model` (default `opus`) — with the FeatureSet
   id and Milestone id. `planner` adds the Feature/Requirement entities to
   the milestone's `Delivers` set, creates krill Tasks with
   `create_task`/`declare_task_dependencies`, and sets the milestone's
   status — every one of these works from this dispatch today (see
   `agents/planner.md`) — and returns the task manifest.
3. **Report.** Relay `planner`'s full task manifest (every task id, title,
   starting lane, dependency edges) to the user verbatim — **this is the
   only durable record of what was just created**; nothing else can
   reconstruct it. Tell the user `/krill-work:implement <milestone-id>` is
   next, and that it needs this exact manifest.

## Steps (no-Milestone GitHub fallback)

Identical to `tools/project-manager/skills/plan/SKILL.md` — mint a tracking
issue citing `krill feature-set-id: <id>`, set up the Project board,
`gh issue list --search "krill feature-set-id: <id>"` for idempotency on a
re-run. Read that file for the full process; it is not duplicated here
since none of it changed on this path.
