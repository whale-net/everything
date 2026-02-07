#!/bin/bash
# Monitor a GitHub PR and auto-merge when all checks pass
# Usage: monitor-pr.sh <pr-number> [repo]

set -euo pipefail

PR_NUMBER="${1:-}"
REPO="${2:-whale-net/everything}"
CHECK_INTERVAL=30
START_TIME=$(date +%s)

if [ -z "$PR_NUMBER" ]; then
    echo "❌ Error: PR number required"
    echo "Usage: $0 <pr-number> [repo]"
    exit 1
fi

# Get PR title
PR_TITLE=$(gh pr view ${PR_NUMBER} --repo ${REPO} --json title -q '.title' 2>/dev/null || echo "Unknown")

echo "🔍 Monitoring PR #${PR_NUMBER}"
echo "Repository: ${REPO}"
echo "Title: ${PR_TITLE}"
echo "Checking every ${CHECK_INTERVAL} seconds"
echo ""

ITERATION=0
while true; do
    ITERATION=$((ITERATION + 1))
    TIMESTAMP=$(date '+%Y-%m-%d %H:%M:%S')
    ELAPSED=$(($(date +%s) - START_TIME))
    ELAPSED_MIN=$((ELAPSED / 60))
    ELAPSED_SEC=$((ELAPSED % 60))

    echo "[${TIMESTAMP}] Check #${ITERATION} (elapsed: ${ELAPSED_MIN}m ${ELAPSED_SEC}s)"

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

    echo "  State: ${STATE} | Mergeable: ${MERGEABLE} | Merge State: ${MERGE_STATE}"

    # Check if already merged or closed
    if [ "$STATE" = "MERGED" ]; then
        echo ""
        echo "✅ PR already merged!"
        exit 0
    fi

    if [ "$STATE" = "CLOSED" ]; then
        echo ""
        echo "❌ PR was closed without merging"
        exit 1
    fi

    # Count check statuses (handle both CheckRun status and conclusion)
    PENDING=$(echo "$PR_JSON" | jq '[.statusCheckRollup[]? | select(.status == "IN_PROGRESS" or .status == "PENDING" or .status == "QUEUED")] | length')
    FAILED=$(echo "$PR_JSON" | jq '[.statusCheckRollup[]? | select(.conclusion == "FAILURE" or .conclusion == "ERROR")] | length')
    SUCCESS=$(echo "$PR_JSON" | jq '[.statusCheckRollup[]? | select(.conclusion == "SUCCESS")] | length')
    TOTAL=$(echo "$PR_JSON" | jq '[.statusCheckRollup[]?] | length')

    if [ "$TOTAL" -gt 0 ]; then
        echo "  Checks: ${SUCCESS}/${TOTAL} passed, ${PENDING} in progress, ${FAILED} failed"
    else
        echo "  Checks: No status checks configured"
    fi

    # Check if ready to merge
    if [ "$MERGEABLE" = "MERGEABLE" ] && [ "$MERGE_STATE" = "CLEAN" ] && [ "$FAILED" = "0" ]; then
        # Double-check all checks are done (no pending)
        if [ "$PENDING" = "0" ] || [ "$TOTAL" = "0" ]; then
            echo ""
            echo "✅ PR is ready to merge!"
            echo "🚀 Merging PR #${PR_NUMBER} with squash..."

            # Merge the PR with squash
            gh pr merge ${PR_NUMBER} --repo ${REPO} --squash --delete-branch

            if [ $? -eq 0 ]; then
                MERGE_TIME=$(date '+%Y-%m-%d %H:%M:%S')
                echo ""
                echo "✅ PR merged successfully!"
                echo "🗑️  Branch deleted"
                echo ""
                echo "**Summary:**"
                echo "- Title: ${PR_TITLE}"
                echo "- Monitoring started: $(date -d @${START_TIME} '+%Y-%m-%d %H:%M:%S')"
                echo "- Merged: ${MERGE_TIME}"
                echo "- Total time: ${ELAPSED_MIN}m ${ELAPSED_SEC}s"
                exit 0
            else
                echo "❌ Failed to merge PR"
                exit 1
            fi
        fi
    fi

    # Show why not ready
    REASON=""
    if [ "$MERGEABLE" != "MERGEABLE" ]; then
        REASON="Not mergeable (conflicts or dependencies)"
    elif [ "$MERGE_STATE" != "CLEAN" ]; then
        REASON="Merge state: ${MERGE_STATE}"
    elif [ "$FAILED" != "0" ]; then
        REASON="${FAILED} check(s) failing"
    elif [ "$PENDING" != "0" ]; then
        REASON="Waiting for ${PENDING} check(s)..."
    fi

    if [ -n "$REASON" ]; then
        echo "  ⏳ ${REASON}"
    fi

    echo ""
    sleep ${CHECK_INTERVAL}
done
