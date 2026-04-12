# Work on Issue Skill

End-to-end workflow from a GitHub issue (or description) to a merged PR.

## Quick Start

```bash
/work-on-issue 123
/work-on-issue "Add user authentication to the API"
```

## Workflow

| Phase | What Happens | User Input Required |
|-------|--------------|---------------------|
| **Discovery** | Fetch issue or parse description | Issue number or text |
| **Safety** | Check git status | Confirm if dirty state |
| **Branch** | Create feature branch from main | Auto-generated name |
| **Planning** | Enter plan mode | Confirm plan mode |
| **Implementation** | Execute plan, commit often | Behavioral decisions only |
| **Testing** | Run tests, fix errors | Approve behavior changes |
| **PR Creation** | Push and create draft PR | Review PR details |
| **Iteration** | Handle feedback, update PR | Additional changes |

## When to Use

Use when starting work on a GitHub issue or new feature. Skip for quick one-line changes or when already mid-way through work on an existing branch.

## Related Skills

- `/test-bazel` — Run Bazel tests (used automatically)
- `/release` — Create releases after merge
- `/merge-pr-when-ready` — Auto-merge when CI passes
