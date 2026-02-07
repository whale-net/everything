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

## Process

### Step 1: Validate Input

Extract PR number from arguments:
- If no argument provided, ask user for PR number
- Validate PR exists and is open
- Show initial PR status

### Step 2: Get Initial Status

Fetch PR details to show user what will be monitored:

```bash
gh pr view <PR_NUMBER> --repo whale-net/everything --json number,title,state,mergeable,mergeStateStatus,statusCheckRollup
```

Display summary:
```
Monitoring PR #326: "Fix database migrations"
- Current State: OPEN
- Mergeable: UNKNOWN (checks pending)
- Checks: 3 pending, 0 failed
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

### Step 4: Monitor PR Status

Create a monitoring loop that checks every 30 seconds:

```bash
#!/bin/bash
PR_NUMBER=$1
REPO="whale-net/everything"
CHECK_INTERVAL=30

echo "🔍 Monitoring PR #${PR_NUMBER}..."
echo "Checking every ${CHECK_INTERVAL} seconds"
echo ""

ITERATION=0
while true; do
    ITERATION=$((ITERATION + 1))
    TIMESTAMP=$(date '+%Y-%m-%d %H:%M:%S')
    echo "[${TIMESTAMP}] Check #${ITERATION}"

    # Fetch PR status
    PR_JSON=$(gh pr view ${PR_NUMBER} --repo ${REPO} \
        --json state,mergeable,mergeStateStatus,statusCheckRollup 2>&1)

    if [ $? -ne 0 ]; then
        echo "❌ Error fetching PR: ${PR_JSON}"
        exit 1
    fi

    # Parse status
    STATE=$(echo "$PR_JSON" | jq -r '.state')
    MERGEABLE=$(echo "$PR_JSON" | jq -r '.mergeable')
    MERGE_STATE=$(echo "$PR_JSON" | jq -r '.mergeStateStatus')

    echo "  State: ${STATE}"
    echo "  Mergeable: ${MERGEABLE}"
    echo "  Merge State: ${MERGE_STATE}"

    # Check if already merged or closed
    if [ "$STATE" = "MERGED" ]; then
        echo "✅ PR already merged!"
        exit 0
    fi

    if [ "$STATE" = "CLOSED" ]; then
        echo "❌ PR was closed without merging"
        exit 1
    fi

    # Count check statuses
    PENDING=$(echo "$PR_JSON" | jq '[.statusCheckRollup[]? | select(.state == "PENDING")] | length')
    FAILED=$(echo "$PR_JSON" | jq '[.statusCheckRollup[]? | select(.state == "FAILURE" or .state == "ERROR")] | length')
    SUCCESS=$(echo "$PR_JSON" | jq '[.statusCheckRollup[]? | select(.state == "SUCCESS")] | length')
    TOTAL=$(echo "$PR_JSON" | jq '[.statusCheckRollup[]?] | length')

    if [ "$TOTAL" -gt 0 ]; then
        echo "  Checks: ${SUCCESS}/${TOTAL} passed, ${PENDING} pending, ${FAILED} failed"
    else
        echo "  Checks: No status checks required"
    fi

    # Check if ready to merge
    if [ "$MERGEABLE" = "MERGEABLE" ] && [ "$MERGE_STATE" = "CLEAN" ] && [ "$FAILED" = "0" ]; then
        # Double-check all checks are done (no pending)
        if [ "$PENDING" = "0" ] || [ "$TOTAL" = "0" ]; then
            echo ""
            echo "✅ PR is ready to merge!"
            echo "🚀 Merging PR #${PR_NUMBER}..."

            # Merge the PR with squash
            gh pr merge ${PR_NUMBER} --repo ${REPO} --squash --delete-branch

            if [ $? -eq 0 ]; then
                echo "✅ PR merged successfully!"
                echo "🗑️  Branch deleted"
                exit 0
            else
                echo "❌ Failed to merge PR"
                exit 1
            fi
        fi
    fi

    # Show why not ready
    if [ "$MERGEABLE" != "MERGEABLE" ]; then
        echo "  ⏳ Not mergeable yet (may have conflicts or dependencies)"
    elif [ "$MERGE_STATE" != "CLEAN" ]; then
        echo "  ⏳ Merge state not clean: ${MERGE_STATE}"
    elif [ "$FAILED" != "0" ]; then
        echo "  ❌ ${FAILED} check(s) failing - cannot merge"
    elif [ "$PENDING" != "0" ]; then
        echo "  ⏳ Waiting for ${PENDING} check(s) to complete"
    fi

    echo "  Next check in ${CHECK_INTERVAL}s..."
    echo ""
    sleep ${CHECK_INTERVAL}
done
```

### Step 5: Execute Monitoring

Run the monitoring script:

```bash
# Save script to scratchpad
SCRIPT_PATH="${SCRATCHPAD_DIR}/monitor-pr-${PR_NUMBER}.sh"

# Write the monitoring script
cat > "${SCRIPT_PATH}" << 'SCRIPT_EOF'
[... monitoring script from above ...]
SCRIPT_EOF

chmod +x "${SCRIPT_PATH}"

# Execute the script
exec "${SCRIPT_PATH}"
```

**IMPORTANT**: The skill should run the monitoring script directly (not in background) so the user can see real-time updates.

## Success Response

When merge completes successfully:

```markdown
✅ **PR #326 merged successfully!**

**Details:**
- Title: Fix database migrations
- Checks: All passed
- Merged: 2026-02-07 22:15:30 UTC
- Branch: fix/migration-duplicate-index (deleted)

**Timeline:**
- Monitoring started: 22:10:15
- Checks completed: 22:15:20
- Merged: 22:15:30
- Total time: 5 minutes 15 seconds

The PR has been squash merged and the source branch has been deleted.
```

## Error Handling

Handle these scenarios:

1. **PR not found**: Exit with error message
2. **PR already merged**: Show success message, exit
3. **PR closed**: Show message, exit
4. **Checks fail**: Stop monitoring, show failed checks
5. **Network errors**: Retry with exponential backoff
6. **Timeout**: After 30 minutes, ask user if they want to continue

## Notes

- Uses **Haiku model** for cost efficiency (simple polling task)
- Monitors every 30 seconds (GitHub API rate limit friendly)
- Auto-deletes source branch after merge
- Uses squash merge by default
- Shows progress in real-time
- Handles edge cases (already merged, closed, etc.)

## Examples

```bash
# Monitor and merge PR #326
/merge-pr-when-ready 326

# If no argument, will prompt for PR number
/merge-pr-when-ready
```

## Safety

- ✅ Confirms with user before starting monitoring
- ✅ Only merges when all checks pass
- ✅ Respects required reviews if configured
- ✅ Shows clear status updates
- ✅ Fails safely if checks fail
