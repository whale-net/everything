---
name: work-on-issue
description: Start an issue-driven workflow with branch creation, planning, implementation, testing, and PR creation
---

# Work on Issue

End-to-end workflow for tackling GitHub issues from start to PR.

## Usage

```bash
/work-on-issue 123
/work-on-issue "Add user authentication to the API"
```

## Phases

### Phase 1: Input & Discovery

Accept either an issue number (`123` or `#123`) or a free-form description.

If an issue number is given, fetch it via GitHub MCP and display its title, description, and labels. Labels help determine scope (bug, feature, refactor, etc.).

### Phase 2: Safety Checks

Before creating a branch, check for uncommitted changes and unpushed commits. If the working state is dirty, ask the user how to proceed:

- **Commit and push** current work first (guide through staging/committing)
- **Stash** with `git stash push -m "WIP: before issue #<n>"` and continue
- **Continue anyway** (not recommended)
- **Cancel**

Also confirm if not currently on `main` and offer to switch.

### Phase 3: Branch Creation

Generate a semantic branch name: `<type>/<issue-number>-<short-description>`

Types: `feat/`, `fix/`, `refactor/`, `docs/`, `test/`, `chore/`

Auto-detect type from issue labels or title keywords; default to `feat/` if unclear.

```bash
git fetch origin main
git checkout -b feat/123-user-authentication origin/main
```

If the branch already exists, ask: switch to it, create with a different name, recreate it, or cancel.

### Phase 4: Planning

For non-trivial work, ask the user whether to enter plan mode before implementing. Skip for typo fixes, single-line config changes, or obvious doc updates.

In plan mode:
1. Explore relevant files, patterns, and existing implementations
2. Read related code, tests, and documentation
3. Ask clarifying questions if requirements are ambiguous
4. Create a step-by-step implementation plan covering: files to modify/create, key functions, test strategy, and potential risks

### Phase 5: Implementation

Work through the plan step by step. **Commit after each meaningful step** — do not batch changes.

Commit message format: `<type>(<scope>): <short description>\n\nRelated to #<issue-number>`

**DO NOT** automatically change API contracts, modify test expectations to force a pass, remove functionality, or alter database schemas. Ask the user first.

**DO** automatically fix syntax errors, missing imports, formatting, and obvious typos.

### Phase 6: Testing

Run tests frequently and always before creating the PR. Use the `/test-bazel` skill or target specific packages with `bazel test //path/to/...`.

For common errors (import failures, syntax errors): fix automatically, commit, re-run.

For failures requiring behavioral changes or with unclear root cause: show the failure, explain the analysis, and ask the user how to proceed.

If tests fail after 3 fix attempts, ask the user: continue and note in PR, pause for manual investigation, or mark as TODO with a follow-up issue.

### Phase 7: PR Creation

Verify all changes are committed and tests are passing (or failures are explained), then push and create a **draft PR** via GitHub MCP.

Check for a PR template (`.github/pull_request_template.md`) before writing the body.

PR title: `<type>: <description> (#<issue-number>)`

PR body sections: Summary, Changes, Testing, Related Issue (`Closes #<n>`).

If push is rejected, suggest `git pull --rebase origin <branch> && git push`.

### Phase 8: Iteration

Stay on the same branch. For each round of feedback: make changes, run tests, commit, and push (the PR updates automatically).

When the user confirms the PR is ready, use GitHub MCP to mark it as non-draft.
