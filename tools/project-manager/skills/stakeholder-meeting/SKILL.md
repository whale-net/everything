---
name: stakeholder-meeting
description: Runs a stakeholder meeting round on a plan — dispatches one stakeholder persona per persona named in the specification, collects guidance, non-blocking feedback, and numbered blockers, and posts consolidated minutes. Blockers route the plan back through the producer/architect loop. Use after architect sign-off, or against an approved root plan issue.
---

# stakeholder-meeting

Convenes every persona named in a plan's specification for one round of feedback, and reports whether the plan is cleared or blocked. See `tools/project-manager/CONVENTIONS.md` § Stakeholder meeting.

Callable directly, or automatically by `/project-manager:design --stakeholder-meeting`.

## Usage

```
/project-manager:stakeholder-meeting <discussion-url>          # after architect sign-off, before human review
/project-manager:stakeholder-meeting <root-issue-number>       # after the root plan is labeled plan:approved
/project-manager:stakeholder-meeting <target> --personas "Operator,Release engineer"   # only these personas
/project-manager:stakeholder-meeting <target> --add-persona "On-call SRE"              # spec personas plus extras
```

## Steps

1. **Resolve the target and read the plan.**
   - Discussion URL/number → `gh discussion view <target> --comments` for sign-off status and the working-draft gist link (CONVENTIONS.md § Working draft); the gist content is authoritative for the spec itself, not any comment. If no `Architect sign-off: approved` comment is present, say so and ask the user whether to hold the meeting anyway — a meeting on an unreconciled draft usually just re-raises what architect is about to ask.
   - Issue number → `gh issue view <n> --json title,body,url,labels` plus `gh issue view <n> --comments`. Warn if it is not labeled `plan:approved`.

2. **Determine the round number.** Count existing `Stakeholder meeting round <N>: <url>` link comments on the target; this meeting is round `N+1` (first meeting is round 1).

3. **Enumerate the personas.** Take the **Personas** section of the current spec, falling back to the personas named in the user stories if that section is absent. Apply `--personas` (replaces the list) or `--add-persona` (appends). Deduplicate personas that are the same actor under two names. If the spec names no personas, stop and report that — a spec with no personas is a producer gap, not a meeting.

4. **Open the meeting.** Create a new Discussion for this round — `gh discussion create --title "Stakeholder meeting round <N>: <feature>" --category "Ideas" --body-file <tmpfile>` — whose body is the agenda: the personas attending, the spec revision under review (link the working-draft gist for a Discussion target, or note the issue body revision for a root plan Issue target), and the three response sections each stakeholder must return. Capture the new discussion's URL, then post exactly one link comment on the target (`gh discussion comment <target> --body "Stakeholder meeting round <N>: <meeting-discussion-url>"` / `gh issue comment <n> --body "Stakeholder meeting round <N>: <meeting-discussion-url>"`) — this is the only trace of the meeting the target ever gets, keeping its history free of the full transcript.

5. **Collect feedback.** Dispatch one `project-manager:stakeholder` subagent **per persona, in parallel** — one dispatch per persona, never one agent covering several. Each dispatch gets: the persona name, the plan target URL/number (to read the spec from — unchanged), the meeting discussion URL (to post feedback to), the round number, and the working-draft gist link (Discussion target) or issue body (root plan Issue target) as the authoritative spec. Each posts its own `Stakeholder feedback — <persona> (round <N>)` comment **on the meeting discussion** with **Guidance**, **Feedback**, and **Blockers** (or the literal `Blockers: none`).

6. **Post minutes.** Once every stakeholder has reported, post one comment **on the meeting discussion** titled `Stakeholder meeting minutes (round <N>)` containing:
   - A table: persona | blockers raised | one-line summary of their position.
   - **Consolidated blockers** — deduplicated across personas, numbered `SB-<N>.<n>` (e.g. `SB-1.2` = round 1, blocker 2), each carrying the personas that raised it, the FR/NFR/story it attaches to, and the outcome that would resolve it. Two personas describing the same break become one entry naming both.
   - **Consolidated guidance and non-blocking feedback** — grouped by theme, each item marked non-blocking. These do not gate the plan.
   - **Outcome** — exactly one of `Stakeholder meeting: blocked (<k> blockers)` or `Stakeholder meeting: cleared`, as the final line, so later rounds and `/project-manager:status` can grep it.

7. **Route the outcome.**
   - **Cleared** — report to the user, including the meeting discussion URL. If invoked from `/project-manager:design`, control returns there for the hand-off to `/project-manager:review`. Non-blocking feedback still stands: tell the user which items producer should fold in versus record as out of scope.
   - **Blocked, discussion target** — dispatch `project-manager:producer` (Mode 2) with the target discussion URL and the consolidated blocker list (read from the meeting discussion's minutes comment), instructing it to answer each `SB-<N>.<n>` in the target discussion and post an updated draft, then dispatch `project-manager:architect` for a fresh reconciliation round. Once architect re-signs off, re-run this skill for round `N+1` (a fresh meeting discussion). Cap at 3 meeting rounds; if blockers persist, stop and summarize the standing disagreement for the user rather than looping.
   - **Blocked, approved root issue target** — the spec of record is already published, so do not silently edit it. Report the blockers to the user (read from the meeting discussion's minutes) and confirm before proceeding; on confirmation, run the same producer/architect loop in the plan's intake discussion (its URL is the first line of the root issue body), then have producer amend the root plan issue body and post `Amended after stakeholder meeting round <N>: <summary>` as an issue comment.

8. **Do not create task issues or a Project board.** A blocker changes the plan; it does not become a task. Scope notes are only appropriate once a Project board exists — before that, non-blocking feedback belongs in the draft or in **Out of scope** with a stated reason.
