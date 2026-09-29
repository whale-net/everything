# validate

*Runs whole-system validation for a krill-work plan (fork of project-manager's validate) — dispatches system-validator to exercise the merged result in Tilt against the FeatureSet's Requirements (read live from krill), then routes any findings to planner for follow-up tasks. On the Milestone path this never touches GitHub Issues/Projects.*


Only meaningful once every task in the manifest (Milestone path) or every
task item (no-Milestone path) is `Done`.

## Usage

```
the `validate` workflow <milestone-id>     # Milestone path — needs the task manifest plan/implement produced
the `validate` workflow 123                # no-Milestone GitHub fallback: the tracking issue number
```

## Steps (Milestone path)

1. Confirm every task in the manifest (CONVENTIONS.md "No task-discovery
   query exists" — you must already have it) is `Done`: `get_task {id}` on
   each, don't trust a stale snapshot.
2. Build the integration branch from every task's own tip (most will
   already be on `main` via continuous merge) right before dispatching
   system-validator — same mechanic as project-manager's `validate` step 2.
3. Spawn a subagent (Cline: new_task) with the `krill-system-validator` custom mode as its mode with the FeatureSet id (and
   Milestone id). It calls `get_feature_set_slice {id}` for the live
   Requirements, brings the system up via Tilt, exercises it, and reports
   findings as `record_note` scope-notes (expected to hit the known
   `record_note` blocker — see `the system-validator custom mode in .roomodes`).
4. **If everything passed:** re-dispatch `mergepush` as a no-op
   re-confirmation, verify every PR is `MERGED`. Call `set_milestone_status
   {milestone_id, status: "shipped"}` and, per-item,
   `mark_delivered_item_shipped` for each delivered Feature/Requirement —
   both work from this dispatch today. Report the plan fully validated with
   the full PR list.
5. **If there are findings:** spawn a subagent (Cline: new_task) with the `krill-planner` custom mode as its mode with the
   finding text `system-validator` reported (not note ids, since
   `record_note` itself is blocked — the one sanctioned body-passing
   exception in CONVENTIONS.md "Subagent dispatch: ids, not bodies"; switch
   to note ids once it's unblocked) plus the Milestone id to run its
   findings-handling process.
   Report the new task manifest additions and point to
   the `implement` workflow <milestone-id>`.

## Steps (no-Milestone GitHub fallback)

Identical to `tools/project-manager/workflows/validate.md` — `<n>` is
the GitHub tracking issue, findings become task issues on the Project,
`Ledger: M<k> → shipped` posts on the product tracking issue. Read that
file for the full process; it is not duplicated here since none of it
changed on this path.
