# The derived open-question view (FR6, FR7, issue #2545)

`krill/store/open_questions.go` adds `RevisionEventStore.ListOpenQuestions`
and `krill/api/handlers/open_questions.go` adds its HTTP surface: `GET
/design-sessions/{id}/open-questions` (ungated, optional
`?blocking=true` filter). Both cover FR6 and FR7.

**Derived, never a second table.** FR6 is explicit and non-negotiable: the
current open-question set is a **last-event-wins window query** over
`revision_event.open_questions_delta`, the same "if a table needs an
SCD2-shaped view over it, derive one with a window function" carve-out
`AGENTS.md`'s SCD2 section states for an append-only log. There is no
mutable question-status table anywhere and no `resolved` boolean column
-- a later contributor who finds the query in `open_questions.go` awkward
should widen the query, not add a table. `ListOpenQuestions` runs one SQL
statement: unnest every touching event's `open_questions_delta->'opened'`/
`->'resolved'` into `(question_id, seq_no, action)` rows, rank each
question id's rows by `seq_no` descending, and keep only the rank-1 rows
whose action is `opened`. An event that neither opens nor resolves
anything contributes zero rows to the unnest, so the read never
reconstructs `entity_deltas`, `verified_against`, or any other column for
an event that never mentions a question.

**Question identity: `opened` is an object, `resolved` is a bare id.**
`OpenQuestionsDelta.Opened` is `[]OpenQuestionOpened{QuestionID, Blocking,
Text}`; `Resolved` is `[]string`. A question's blocking flag and text are
established once, at the event that opens it, and never restated by the
event that resolves it -- restating them on resolve would require exactly
the mutable row FR6/NFR1 forbid. Re-opening a previously resolved question
id is allowed (last-event-wins handles it: the most recent `opened` entry
wins for that question id, including its blocking/text), and is covered by
`TestListOpenQuestions_ReopenAfterResolve_LatestOpenedFlagsWin`.

**Nil slices are normalized to `[]` before every write.** A nil
Go slice marshals to JSON `null`, which is a JSONB *scalar* -- distinct
from SQL `NULL` -- that `jsonb_array_elements` rejects with "cannot
extract elements from a scalar". `RevisionEventStore.Append` defaults a
nil `Opened`/`Resolved` to an empty slice before marshaling so every row
this store writes carries `[]`, never JSON `null`; `ListOpenQuestions`'
and `validateResolvedQuestionsOpened`'s queries also guard with a
`jsonb_typeof(...) = 'array'` check, as defense in depth against any row
written by a path that does not go through `Append`.

**FR6's stateful validation lives in `Append`, not in Go-only
validation.** A `resolved` entry naming a question id never opened
anywhere in the session is a 400 (`store.ErrInvalidRevisionEvent`), not a
silently-ignored no-op -- `validateResolvedQuestionsOpened`
(`open_questions.go`) checks this inside `Append`'s own transaction,
after its row lock on the owning `design_session`, against the union of
the session's already-committed `opened` question ids and the new
event's own `Opened` entries (so opening and resolving the same question
id within one event is allowed). This check is deliberately **not**
against `ListOpenQuestions`' currently-open subset: re-resolving an
already-resolved question must stay a no-op, never a 400.

**FR7: resolution needs no new write path.** Resolving a question is an
ordinary `answer` (or `ruling`) revision event appended through issue
#2543's endpoint, whose `open_questions_delta.resolved` names the
question. `TestFR7_ResolutionIsARevisionEventCarryingBothIdentities`
(`api/handlers/open_questions_test.go`) is the proof: it appends a
resolving event with acting != on-behalf-of, then asserts the question
disappears from `ListOpenQuestionsHandler`'s output and that
`GetDesignSessionHandler` shows the resolution as a `revision_event`
carrying both identity triples distinctly -- the resolution and who made
it (and on whose behalf) are part of the same append-only record, with no
mutable question row to carry them instead.

