---
name: work-on-issue
description: Start an issue-driven workflow with branch creation, planning, implementation, testing, and PR creation
---

# Work on Issue

This skill provides a comprehensive workflow for tackling GitHub issues from start to finish. It guides you through creating a branch, planning the solution, implementing changes, running tests, and creating a pull request.

## Usage

```bash
/work-on-issue <issue-number>
/work-on-issue 123

# Or with a description instead of an issue number
/work-on-issue "Add user authentication to the API"
```

## Process Overview

The workflow follows these phases:

1. **Input & Discovery** - Parse issue or description
2. **Safety Checks** - Verify uncommitted/unpushed changes
3. **Branch Creation** - Create feature branch from main
4. **Planning** - Enter plan mode to design solution
5. **Implementation** - Execute the plan with frequent commits
6. **Testing** - Verify with Bazel tests
7. **PR Creation** - Push branch and create draft PR
8. **Iteration** - Handle review feedback

## Phase 1: Input & Discovery

### Parse Input

Extract either:
- **Issue Number**: Integer like `123` or `#123`
- **Description**: Free-form text describing the work

### Fetch Issue Details (if number provided)

Use GitHub MCP to get issue details:

```python
# Get issue details
mcp__github__issue_read(
    method="get",
    owner="whale-net",
    repo="everything",
    issue_number=123
)
```

Extract and display:
```markdown
📋 **Issue #123**: Add user authentication

**Description:**
Implement JWT-based authentication for API endpoints...

**Labels:** enhancement, api, security
**Assignees:** @username
```

### Store Context

Keep track of:
- Original issue number (if provided)
- Issue title
- Issue description
- Labels (help identify scope: bug, feature, refactor, etc.)

## Phase 2: Safety Checks

**CRITICAL**: Before creating a new branch, verify clean working state.

### Check Git Status

```bash
# Check for uncommitted changes
git status --porcelain

# Check for unpushed commits on current branch
CURRENT_BRANCH=$(git rev-parse --abbrev-ref HEAD)
UNPUSHED=$(git log origin/"$CURRENT_BRANCH".."$CURRENT_BRANCH" --oneline 2>/dev/null | wc -l)
```

### Warn User if Dirty State

If uncommitted changes or unpushed commits exist, use `AskUserQuestion`:

```
Question: "⚠️ You have uncommitted changes and/or unpushed commits on branch '$CURRENT_BRANCH'. What would you like to do?"
Header: "Uncommitted Work"
Options:
  - "Commit and push current work first" - I'll guide you through committing
  - "Stash changes and continue" - Stash for later
  - "Continue anyway (not recommended)" - Proceed without cleaning up
  - "Cancel" - Stop the workflow
```

**If "Commit and push" selected:**
1. Show `git status` output
2. Ask which files to stage
3. Create commit with meaningful message
4. Push to remote
5. Then continue with workflow

**If "Stash" selected:**
```bash
git stash push -m "WIP: Stashed before working on issue #123"
echo "✅ Changes stashed. You can restore them later with: git stash pop"
```

**If "Cancel" selected:**
Exit the workflow gracefully.

### Verify on Main or Ask to Switch

```bash
CURRENT_BRANCH=$(git rev-parse --abbrev-ref HEAD)

if [[ "$CURRENT_BRANCH" != "main" ]]; then
  # Ask if user wants to switch to main first
  # Use AskUserQuestion
fi
```

## Phase 3: Branch Creation

### Generate Branch Name

Create semantic branch name from issue:

**Pattern**: `<type>/<issue-number>-<short-description>`

**Types**:
- `feat/` - New features
- `fix/` - Bug fixes
- `refactor/` - Code refactoring
- `docs/` - Documentation
- `test/` - Test additions/fixes
- `chore/` - Maintenance tasks

**Example**: `feat/123-user-authentication`

Auto-detect type from:
1. Issue labels (if available)
2. Keywords in title ("fix", "add", "refactor", etc.)
3. Default to `feat/` if unclear

### Create Branch

```bash
# Fetch latest main
git fetch origin main

# Create and checkout new branch from origin/main
git checkout -b feat/123-user-authentication origin/main
```

Display confirmation:
```markdown
✅ Created branch: `feat/123-user-authentication`
📍 Based on: `origin/main` (commit abc1234)
```

## Phase 4: Planning Phase

**IMPORTANT**: Always offer to enter plan mode unless the issue is trivial (e.g., typo fix, simple config change).

### Determine if Planning Needed

Skip planning for:
- Simple typo fixes
- Single-line config changes
- Obvious documentation updates

Otherwise, **strongly recommend planning**.

### Offer Plan Mode

Use `AskUserQuestion`:

```
Question: "Would you like to enter plan mode to design the solution before implementing?"
Header: "Planning"
Options:
  - "Yes, create a plan (Recommended)" - Enter plan mode
  - "No, I have clear requirements" - Skip to implementation
```

### Enter Plan Mode (if selected)

Use the `EnterPlanMode` tool:

```python
EnterPlanMode()
```

**In plan mode:**

1. **Explore codebase** - Find relevant files, patterns, existing implementations
2. **Understand context** - Read related code, tests, documentation
3. **Ask clarifying questions** - Use `AskUserQuestion` if requirements unclear:

```
Question: "Where should the authentication middleware be placed?"
Header: "Architecture"
Options:
  - "In api/middleware/ following existing pattern"
  - "Create new auth/ module"
  - "Integrate into existing auth system"
```

4. **Design solution** - Create step-by-step implementation plan
5. **Exit plan mode** - Use `ExitPlanMode` when ready

**Plan should include:**
- Files to modify/create
- Key functions/classes to implement
- Test strategy
- Potential risks or dependencies

## Phase 5: Implementation

### Execute Plan

Work through plan steps systematically.

**For each step:**

1. **Read relevant files** - Understand current state
2. **Make changes** - Implement the step
3. **Commit immediately** - Don't wait to commit multiple steps

### Commit Frequently

**CRITICAL**: Commit after EACH meaningful step.

**Good commit points:**
- Added a new function
- Modified an existing module
- Updated configuration
- Added tests for a feature
- Fixed a bug

**Commit message format:**
```bash
git add <files>
git commit -m "feat(auth): Add JWT token validation middleware

- Implement token validation logic
- Add error handling for expired tokens
- Include unit tests

Related to #123"
```

**Pattern**: `<type>(<scope>): <short description>`

**Types**: feat, fix, refactor, test, docs, chore

**Always include**: `Related to #<issue-number>`

### Show Progress

After each commit, display:
```markdown
✅ **Step 3/7 complete**: Added JWT middleware
📝 Committed: `feat(auth): Add JWT token validation middleware`
```

## Phase 6: Testing

**CRITICAL**: Run tests frequently during implementation and always before creating PR.

### Test Strategy

Use `/test-bazel` skill or run specific targets:

**Option 1: Use test-bazel skill**
```bash
/test-bazel
```

**Option 2: Run specific test targets**
```bash
# Test specific package
bazel test //manman/api/auth:all

# Test affected targets only (faster)
bazel test //manman/api/...
```

### After Each Significant Change

Run relevant tests:
```bash
# Modified auth middleware? Test auth
bazel test //manman/api/auth:middleware_test

# Modified API routes? Test integration
bazel test //manman/api:integration_test
```

### Handle Test Failures

**For common, obvious errors (e.g., import errors, syntax errors):**
- Fix automatically
- Commit the fix
- Re-run tests

**For failures requiring behavior changes or unclear root cause:**
1. Show the failure
2. Analyze what failed
3. **Pause and ask user** with `AskUserQuestion`:

```
Question: "Test failed: 'test_token_validation_rejects_expired'. The test expects 401 but got 403. How should we handle this?"
Header: "Test Failure"
Options:
  - "Change code to return 401 (match test expectation)"
  - "Update test to expect 403 (current behavior is correct)"
  - "Let me investigate further"
```

### Pre-PR Test Run

Before creating PR, run full test suite on affected areas:

```bash
# Run all tests in modified directories
bazel test //manman/api/... //manman/auth/...
```

Display results:
```markdown
🧪 **Test Results:**
✅ 47 tests passed
❌ 2 tests failed
⏭️  3 tests skipped

**Failed Tests:**
- //manman/api:integration_test - Connection timeout
- //manman/auth:token_test - Assertion error

Would you like me to investigate the failures?
```

## Phase 7: PR Creation

### Pre-PR Checklist

Before creating PR, verify:

✅ All planned changes implemented
✅ Tests passing (or failures explained)
✅ Code committed
✅ Branch ready to push

### Push Branch

```bash
# Push branch to remote
git push -u origin feat/123-user-authentication
```

### Create Draft PR

Use GitHub MCP to create draft PR:

```python
# First, read the PR template if it exists
# Check for .github/pull_request_template.md or .github/PULL_REQUEST_TEMPLATE/

# Then create the PR
mcp__github__create_pull_request(
    owner="whale-net",
    repo="everything",
    title="feat: Add user authentication (#123)",
    head="feat/123-user-authentication",
    base="main",
    draft=True,
    body="""
## Summary
Implements JWT-based authentication for API endpoints as described in #123.

### Changes
- Added JWT token validation middleware
- Implemented login/logout endpoints
- Added authentication tests
- Updated API documentation

### Testing
- ✅ Unit tests: 12/12 passing
- ✅ Integration tests: 5/5 passing
- ⚠️  Known issue: Connection timeout in one test (investigating)

### Related Issue
Closes #123

---
🤖 Generated with [Claude Code](https://claude.com/claude-code)
"""
)
```

**PR Title Pattern**: `<type>: <description> (#<issue-number>)`

**Draft PR Body Sections:**
1. **Summary** - What was implemented
2. **Changes** - Bullet list of modifications
3. **Testing** - Test results and coverage
4. **Related Issue** - Links to issue(s)

### Display PR Link

```markdown
✅ **Draft PR Created!**

🔗 **PR Link**: https://github.com/whale-net/everything/pull/456

**Next Steps:**
1. Review the changes in the PR
2. Address any feedback
3. Mark as ready for review when complete

The PR is currently in **draft** mode. Let me know if you'd like to make changes or mark it ready for review.
```

## Phase 8: Iteration

### Handle User Feedback

User may request changes after PR creation:

**Common requests:**
- "Add tests for edge case X"
- "Refactor function Y"
- "Update documentation"
- "Fix linting errors"

### Continue on Same Branch

**IMPORTANT**: Stay on the same branch and continue the workflow.

**For each iteration:**

1. Make requested changes
2. Run tests
3. Commit changes
4. Push to same branch (updates PR automatically)

```bash
# Make changes...
git add <files>
git commit -m "test: Add edge case tests for token expiration"
git push
```

Display:
```markdown
✅ **Changes pushed to PR #456**

**Latest commit**: `test: Add edge case tests for token expiration`

The PR will update automatically. You can view it here:
https://github.com/whale-net/everything/pull/456
```

### Mark Ready for Review

When user indicates PR is ready:

```python
# Update PR to mark as ready (not draft)
mcp__github__update_pull_request(
    owner="whale-net",
    repo="everything",
    pullNumber=456,
    draft=False
)
```

```markdown
✅ **PR marked as ready for review!**

The PR is now visible to reviewers. You can request specific reviewers or wait for automatic assignment.
```

## Safety Guidelines

### Always Confirm Risky Actions

Use `AskUserQuestion` before:

- Creating a branch when there are uncommitted changes
- Force pushing
- Deleting branches
- Making architectural changes

### Pause for Behavioral Decisions

**DO NOT** automatically:
- Change API contracts without confirmation
- Modify test expectations to make tests pass
- Remove functionality
- Change database schemas

**DO** automatically:
- Fix syntax errors
- Add missing imports
- Format code
- Fix obvious typos

### Communicate Clearly

After each phase, show:
- What was accomplished
- Current status
- Next steps

## Error Handling

### Branch Already Exists

```bash
# If branch exists, ask user
if git show-ref --verify --quiet refs/heads/feat/123-user-auth; then
    # Use AskUserQuestion to ask what to do
fi
```

Options:
- Switch to existing branch
- Create new branch with different name
- Delete old branch and recreate
- Cancel

### Issue Not Found

If issue number invalid:
```markdown
❌ **Issue #999 not found**

Please verify the issue number or provide a description instead:
/work-on-issue "Description of the work"
```

### Tests Keep Failing

If tests fail after 3 attempts to fix:
```markdown
⚠️ **Tests still failing after multiple attempts**

**Options:**
1. Continue anyway and note in PR
2. Pause and investigate manually
3. Mark specific tests as TODO and create follow-up issue

Which would you prefer?
```

### Push Rejected

If push fails (e.g., branch protection, remote changes):
```bash
# Show error
echo "❌ Push failed: $ERROR_MESSAGE"

# Suggest solution
echo "Suggested fix:"
echo "  git pull --rebase origin feat/123-user-auth"
echo "  git push"
```

## Best Practices

### Commit Messages

**Good:**
```
feat(auth): Add JWT middleware (#123)

Implements token validation with expiry checking.
Includes error handling for malformed tokens.
```

**Bad:**
```
WIP
fix stuff
updates
```

### Branch Naming

**Good:**
- `feat/123-add-user-auth`
- `fix/456-null-pointer-in-login`
- `refactor/789-simplify-token-logic`

**Bad:**
- `feature-branch`
- `temp`
- `alex-work`

### Test Coverage

Always test:
- Happy path
- Error cases
- Edge cases (empty input, null, etc.)
- Integration points

### PR Descriptions

Include:
- What changed
- Why it changed
- How to test it
- Related issues
- Breaking changes (if any)

## Example Workflow

```bash
# User starts workflow
/work-on-issue 123

# 1. Fetch issue details
📋 Issue #123: Add user authentication
Labels: enhancement, api

# 2. Safety check
⚠️ You have uncommitted changes. Commit them first? (Yes)

# 3. Create branch
✅ Created branch: feat/123-user-authentication

# 4. Plan mode
Enter plan mode to design solution? (Yes)
[Plan mode: explore, design, create plan]
✅ Plan created with 7 steps

# 5. Implementation
✅ Step 1/7: Add JWT library dependency
✅ Step 2/7: Create middleware module
✅ Step 3/7: Implement token validation
...
✅ Step 7/7: Update API documentation

# 6. Testing
🧪 Running tests...
✅ 15/15 tests passed

# 7. Create PR
✅ Pushed branch to remote
✅ Created draft PR #456
🔗 https://github.com/whale-net/everything/pull/456

Ready for your review!
```

## Notes

- Works with both issue numbers and free-form descriptions
- Strongly encourages plan mode for non-trivial changes
- Commits frequently (after each step)
- Tests continuously during development
- Creates draft PRs to signal work-in-progress
- Handles iterations on the same branch
- Pauses for user input on behavioral decisions
- Fixes common errors automatically
- Provides clear status updates throughout

## Files

- `SKILL.md` - This documentation (you are here)
