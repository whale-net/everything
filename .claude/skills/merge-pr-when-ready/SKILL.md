---
name: merge-pr-when-ready
description: Monitor a PR and auto-merge when all checks pass
model: haiku
---

# Merge PR When Ready

This skill monitors a GitHub Pull Request and automatically merges it when all checks pass and it becomes mergeable.

## Usage

```bash
/merge-pr-when-ready <pr-number>
/merge-pr-when-ready 326
```

## What It Does

1. **Validates PR** - Checks that the PR exists and is open
2. **Monitors Status** - Polls every 30 seconds for:
   - Mergeable status (no conflicts)
   - CI/CD check status (all passing)
   - Review requirements (if configured)
3. **Auto-Merges** - Squash merges the PR when ready
4. **Reports Progress** - Shows status updates during monitoring

## Implementation

This skill uses the **Haiku model** for cost-efficient monitoring since it's a simple polling task that doesn't require complex reasoning.

Uses **GitHub MCP tools** when available for API access, falls back to `gh` CLI.

## Process

### Step 1: Validate Input

Extract PR number from arguments. If no argument provided, ask user for PR number.

### Step 2: Get Initial Status

Fetch PR details using GitHub MCP:

```python
# Use mcp__github__pull_request_read with method="get"
mcp__github__pull_request_read(
    method="get",
    owner="whale-net",
    repo="everything",
    pullNumber=326
)
```

Display summary:
```
Monitoring PR #326: "Fix database migrations"
- Current State: OPEN
- Mergeable: MERGEABLE
- Merge State: BLOCKED (checks pending)
- Checks: 3 in progress, 0 failed
```

### Step 3: Confirm Monitoring

Use `AskUserQuestion` to confirm:

```
Question: "Start monitoring this PR for auto-merge?"
Header: "Confirm"
Options:
  - "Yes, monitor and auto-merge when ready (Recommended)" - Start monitoring
  - "No, cancel" - Exit without monitoring
```

### Step 4: Execute Monitoring Script

Run the monitoring script from the skill directory:

```bash
# Path to the monitoring script (part of this skill)
SCRIPT_PATH="/home/alex/whale_net/everything/.claude/skills/merge-pr-when-ready/monitor-pr.sh"

# Execute the script with PR number
"${SCRIPT_PATH}" 326
```

The script (`monitor-pr.sh`) handles:
- Polling every 30 seconds
- Parsing check status (IN_PROGRESS, SUCCESS, FAILURE)
- Detecting when PR is ready (mergeable + clean + all checks passed)
- Auto-merging with squash
- Deleting source branch
- Progress reporting

**IMPORTANT**:
- The script is version-controlled in the skill directory
- Run directly (not in background) for real-time updates
- Uses `gh` CLI for reliability (GitHub MCP may not have merge permissions)

## Success Response

When merge completes successfully, the script outputs:

```
✅ PR merged successfully!
🗑️  Branch deleted

**Summary:**
- Title: Fix database migrations
- Monitoring started: 2026-02-07 22:10:15
- Merged: 2026-02-07 22:15:30
- Total time: 5m 15s
```

## Error Handling

The script handles:

1. **PR not found**: Exit with error message
2. **PR already merged**: Show success message, exit
3. **PR closed**: Show message, exit
4. **Checks fail**: Show failed checks and exit
5. **Network errors**: Error and exit (user can re-run)

## Notes

- Uses **Haiku model** for cost efficiency
- Monitors every 30 seconds (API rate limit friendly)
- Auto-deletes source branch after merge
- Uses squash merge by default
- Shows progress in real-time
- Handles edge cases gracefully

## Files

- `SKILL.md` - This documentation
- `monitor-pr.sh` - Monitoring and merge script

## Safety

- ✅ Confirms with user before starting monitoring
- ✅ Only merges when all checks pass
- ✅ Respects merge state (no conflicts)
- ✅ Shows clear status updates
- ✅ Fails safely if checks fail
