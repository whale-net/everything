# The task payload document (FR4, FR10, NFR4, issue #2721)

`krill/work.Payload` is the one typed, self-describing document every M4
verb that hands a task to an Agent returns (LB7) — today just the
by-task-id fetch (`GET /tasks/{id}`, `get_task`), later also #2722's claim
verb. It is not a second, independently derived projection over the spec
axis: `Payload.Slice` is `slice.Document` embedded verbatim, and
`work.Assembler.Assemble` obtains it by calling
`slice.Querier.GetMilestoneDeliversSlice` (`krill/slice/query.go`) — a new
sixth `Querier` method alongside FR5-FR9's five (see "The scoped-slice
query" above) that resolves a `milestone_ref` row's own `delivers`
associations (never `must_not_foreclose` — a guardrail, not deliverable
content) into a `Document` via the existing `GetEntitySetSlice`, mirroring
`GetDeliveryBreakdown`/`GetBacklog`'s own composition rather than adding a
bespoke join. This is the live, MCP-exposed per-milestone `Delivers` read
the "Open items" list below used to flag as still open — narrower than
that item asked for (`Delivers` only, no `Must not foreclose`, and reached
through the task payload rather than a standalone `GET
/milestones/{id}/slice` route), so a future task may still want the wider
version. `krill/work` itself never reads `feature`, `requirement`,
`load_bearing_decision`, or `entity_milestone` directly — only through
`slice.Querier` and `store.TaskStore`.

`Assemble(ctx, scopeID, taskID)` loads the `task` row (`store.TaskStore.
GetTaskByID`, which takes no scope argument since task ids are globally
unique surrogates), rejects a `scopeID` mismatch identically to an unknown
`taskID` (`store.ErrNotFound`, NFR1 — mirrors `api/handlers/pointer.go`'s
own cross-scope check), then attaches the work-axis fields: lane state,
lane sequence, attempt count, and the declared dependency list
(`store.TaskStore.ListDependencies`, issue #2720) as `[]work.TaskDep` —
always a non-nil slice, so a task with no dependencies serializes as `[]`,
never `null`. Each `TaskDep.DependsOnTaskID` is named that way rather than
a bare `TaskID` specifically so it is never misread as the payload's own
`Task.ID` when the two appear side by side in the same document.

