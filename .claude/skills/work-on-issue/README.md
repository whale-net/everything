# Work on Issue Skill

A comprehensive workflow skill that guides you from issue to pull request.

## Quick Start

```bash
# Start from a GitHub issue
/work-on-issue 123

# Start from a description
/work-on-issue "Add user authentication to the API"
```

## What It Does

This skill provides an end-to-end workflow:

1. **Fetch issue details** from GitHub (if issue number provided)
2. **Safety checks** - Warns about uncommitted/unpushed changes
3. **Create feature branch** from main with semantic naming
4. **Enter plan mode** to design the solution (recommended)
5. **Implement changes** with frequent commits after each step
6. **Run tests** continuously using `/test-bazel` or specific targets
7. **Fix common errors** automatically, pause for behavioral decisions
8. **Push branch** and create draft PR
9. **Handle iterations** on the same branch for review feedback

## Key Features

### Safety First
- Checks for uncommitted changes before starting
- Verifies unpushed commits
- Confirms risky actions before executing
- Pauses for user input on behavioral changes

### Structured Workflow
- Semantic branch naming (`feat/123-short-description`)
- Frequent commits after each meaningful change
- Continuous testing during development
- Draft PR creation to signal WIP

### Smart Testing
- Uses `/test-bazel` skill for comprehensive testing
- Runs specific targets for faster feedback
- Automatically fixes common errors (imports, syntax)
- Pauses and asks user for test failures requiring behavioral changes

### Iteration Support
- Continues work on same branch
- Pushes updates automatically update PR
- Marks PR ready when user confirms
- Clear status updates throughout

## Workflow Phases

| Phase | What Happens | User Input Required |
|-------|--------------|---------------------|
| **Discovery** | Fetch issue or parse description | Issue number or text |
| **Safety** | Check git status | Confirm if dirty state |
| **Branch** | Create feature branch | Auto-generated name |
| **Planning** | Enter plan mode | Confirm plan mode |
| **Implementation** | Execute plan, commit often | Behavioral decisions |
| **Testing** | Run tests, fix errors | Approve behavior changes |
| **PR Creation** | Push and create draft PR | Review PR details |
| **Iteration** | Handle feedback, update PR | Additional changes |

## Examples

### Working on a bug fix

```bash
/work-on-issue 456

# Skill fetches issue #456 (bug report)
# Creates branch: fix/456-null-pointer-login
# Enters plan mode (you approve)
# Implements fix with commits
# Runs tests
# Creates draft PR
```

### Working from description

```bash
/work-on-issue "Refactor token validation to use async/await"

# No issue to fetch, uses description
# Creates branch: refactor/token-validation-async
# Enters plan mode
# ... workflow continues
```

## Commit Strategy

The skill commits **after each meaningful step**:

```
✅ Step 1: Add JWT dependency
   → git commit -m "feat(auth): Add JWT library dependency (#123)"

✅ Step 2: Create middleware module
   → git commit -m "feat(auth): Create token validation middleware (#123)"

✅ Step 3: Add tests
   → git commit -m "test(auth): Add token validation tests (#123)"
```

## Testing Strategy

Tests are run:
- After each risky change
- Before creating PR
- When requested by user

**Automatic fixes:**
- Import errors
- Syntax errors
- Formatting issues

**Asks user before:**
- Changing test expectations
- Modifying API behavior
- Removing functionality

## When to Use

Use this skill when:
- Starting work on a GitHub issue
- Beginning a new feature or bug fix
- You want a structured workflow from start to PR
- You want frequent commits and continuous testing

**Don't use when:**
- Making a quick one-line change
- Fixing a simple typo
- Working on an existing branch (just continue manually)

## Tips

- Let the skill enter plan mode for non-trivial work
- Trust the automatic error fixes for common issues
- Provide clear answers when asked behavioral questions
- Review the draft PR before marking ready
- Continue using the skill for iterations after PR creation

## Comparison to Manual Workflow

| Manual | With /work-on-issue |
|--------|---------------------|
| Remember to check git status | Automatic safety checks |
| Think of branch name | Semantic name auto-generated |
| Maybe forget to commit often | Commits after each step |
| Run tests when you remember | Continuous testing |
| Manually create PR | Automatic draft PR creation |
| Write PR description from scratch | Auto-generated with issue link |

## Related Skills

- `/test-bazel` - Run comprehensive Bazel tests (used automatically)
- `/release` - Create releases after PR is merged
- `/merge-pr-when-ready` - Auto-merge when CI passes
