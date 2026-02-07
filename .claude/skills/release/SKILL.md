---
name: release
description: Interactive release builder for apps and helm charts
---

# Release Builder

This skill guides you through creating a release for apps and/or helm charts in the Everything monorepo.

## Process

You will:
1. **Select release targets** - Choose which apps/charts to release
2. **Choose version strategy** - Specify version or auto-increment
3. **Review release plan** - See exactly what will be released
4. **Confirm** - Final approval before execution
5. **Execute** - Trigger the release (GitHub workflow or local)

## Discovery Phase

First, discover available apps and helm charts:

```bash
# Find all helm charts
bazel query 'kind("helm_chart", //...)' 2>/dev/null | grep -v "_chart_metadata" | grep -v "_chart$" | sort

# Find all release apps (optional - usually release charts which include apps)
bazel query 'attr("tags", "release-metadata", //...)' 2>/dev/null | sort
```

Parse the output to extract:
- **Domains**: demo, manman, friendly_computing_machine, etc.
- **Chart names**: Extract from targets like `//manman:manmanv2_chart`
- **Chart metadata**: Read chart metadata files to get details

## User Interaction

### Step 0: Branch Verification

**IMPORTANT**: You should be on the main branch locally to ensure accurate discovery of apps and charts.

```bash
# Check current branch
CURRENT_BRANCH=$(git rev-parse --abbrev-ref HEAD)

if [[ "$CURRENT_BRANCH" != "main" ]]; then
  echo "⚠️  WARNING: Not on main branch (currently on: $CURRENT_BRANCH)"
  echo ""
  echo "It's recommended to switch to main to ensure app/chart names are correct:"
  echo "  git checkout main"
  echo "  git pull origin main"
  echo ""
  echo "The workflow will run on main regardless, but local discovery should match."

  # Ask user if they want to continue anyway
  read -p "Continue anyway? (y/N) " -n 1 -r
  echo
  if [[ ! $REPLY =~ ^[Yy]$ ]]; then
    echo "Release cancelled. Please switch to main and try again."
    exit 1
  fi
else
  echo "✅ On main branch - proceeding with release"
fi
```

### Step 1: Select Release Type

Use `AskUserQuestion` to ask what to release:

```
Question: "What would you like to release?"
Options:
  - "Helm Charts Only" - Release helm charts (includes app images referenced by charts)
  - "Apps Only" - Release standalone app images without helm charts
  - "Both Apps and Charts" - Release both apps and helm charts in the same workflow
```

**Note:** When "Both" is selected, you need to collect both apps and helm charts to release in subsequent steps.

### Step 2: Select Charts/Apps

Based on the user's selection, present available options:

**For Helm Charts (or Both):**
First discover available helm charts:
```bash
# Use the release helper to list available charts
bazel run --config=ci //tools:release -- plan-helm-release --charts "all" --version "v0.1.0" --include-demo --format github 2>&1 | grep -E "charts="
```

Then ask:
```
Question: "Which helm chart(s) would you like to release?"
Header: "Charts"
Options: (multiSelect: true)
  - "helm-manman-host-services" - ManMan host services chart
  - "helm-manmanv2-control-services" - ManManV2 control services chart
  - "helm-fcm" - FCM chart
  - "all" - All production charts (excludes demo by default)
```

**For Apps (when "Apps Only" or "Both" is selected):**
Discover available apps:
```bash
# Use the release helper to list available apps
bazel run --config=ci //tools:release -- plan --event-type "workflow_dispatch" --apps "all" --include-demo --increment-patch --format github 2>&1 | grep -E "apps="
```

Then ask:
```
Question: "Which app(s) would you like to release?"
Header: "Apps"
Options: (multiSelect: true)
  - List discovered apps in domain-app format (e.g., "manman-control-api")
  - "all" - All apps (excludes demo by default)
  - "demo" - All demo domain apps
  - "manman" - All manman domain apps
```

### Step 3: Version Strategy

**CRITICAL**: Always verify version upgrade intentionality.

```
Question: "How should we determine the version?"
Header: "Version"
Options:
  - "Auto-increment patch (vX.Y.Z → vX.Y.Z+1)" - For bug fixes
  - "Auto-increment minor (vX.Y.Z → vX.Y+1.0)" - For new features
  - "Specify exact version" - For major releases or custom versions
```

If "Specify exact version" selected, follow up with:
```
Question: "What version should we release?"
Note: Format should be vX.Y.Z (e.g., v1.2.3)
```

**Version Verification**: Before proceeding, check current latest version:

```bash
# For each chart, find latest git tag
git tag -l "helm-manman-control-services.v*" | sort -V | tail -1

# Show to user:
"Current version: v0.5.2
Proposed version: v0.5.3 (patch increment)
This will create a NEW release. Continue?"
```

### Step 4: Additional Options

```
Question: "Additional release options?"
Options: (multiSelect: true)
  - "Dry run (build but don't publish)" - Test the release
  - "Include demo domain" - Also release demo apps/charts
```

### Step 5: Review Release Plan

**CRITICAL**: Show complete summary and require explicit confirmation.

Display a clear summary based on what's being released:

**Example for Helm Charts:**
```markdown
# Release Plan Summary

## Helm Charts
- **Chart**: helm-manmanv2-control-services
  - **Namespace**: manmanv2
  - **Current Version**: v0.2.1
  - **New Version**: v0.3.0 (minor increment)

## Version Strategy
- Auto-increment minor version

## Options
- Dry run: No
- Include demo: No

## Actions That Will Be Taken
1. Build helm chart with referenced apps
2. Create git tag: helm-manmanv2-control-services.v0.3.0
3. Push chart to https://charts.whalenet.dev/
4. Create GitHub release with chart artifacts

⚠️  **WARNING**: This will create a public release and cannot be easily undone.
```

**Example for Both Apps and Charts:**
```markdown
# Release Plan Summary

## Apps
- manman-control-api (v0.3.0)
- manman-event-processor (v0.3.0)

## Helm Charts
- helm-manmanv2-control-services (v0.3.0)

## Version Strategy
- Auto-increment minor version (applies to both apps and charts)

## Options
- Dry run: No
- Include demo: No

## Actions That Will Be Taken
1. Build and push app images to ghcr.io/whale-net
2. Create git tags for each app: manman-control-api.v0.3.0, manman-event-processor.v0.3.0
3. Build helm chart with versioned app references
4. Create git tag for chart: helm-manmanv2-control-services.v0.3.0
5. Push chart to https://charts.whalenet.dev/
6. Create GitHub releases with artifacts

⚠️  **WARNING**: This will create a public release and cannot be easily undone.
```

Then ask for final confirmation:
```
Question: "Proceed with this release?"
Header: "Confirm"
Options:
  - "Yes, proceed with release (Recommended)" - Only if everything looks correct
  - "No, cancel" - Go back to modify
  - "Show more details" - See full release configuration
```

### Step 6: Execute Release

Once confirmed, trigger the release via GitHub workflow.

**IMPORTANT**: Always use `--ref "main"` to ensure the workflow runs against the main branch code. You can trigger this from any local branch, but the workflow itself will execute on main (no hotfix flow is supported).

**For Helm Charts Only:**
```bash
gh workflow run release.yml \
  --ref "main" \
  -f helm_charts="helm-manmanv2-control-services" \
  -f increment_minor=true \
  -f dry_run=false \
  -f include_demo=false
```

**For Apps Only:**
```bash
gh workflow run release.yml \
  --ref "main" \
  -f apps="manman-control-api,manman-event-processor" \
  -f increment_patch=true \
  -f dry_run=false \
  -f include_demo=false
```

**For Both Apps and Helm Charts:**
```bash
gh workflow run release.yml \
  --ref "main" \
  -f apps="manman-control-api,manman-event-processor" \
  -f helm_charts="helm-manmanv2-control-services" \
  -f increment_minor=true \
  -f dry_run=false \
  -f include_demo=false
```

**Monitor the workflow:**
```bash
echo "Release triggered! Monitor progress at:"
gh run list --workflow=release.yml --limit 1 --json url --jq '.[0].url'
```

**Version flags (use exactly one):**
- `-f increment_patch=true` - Bump patch version (v1.2.3 → v1.2.4)
- `-f increment_minor=true` - Bump minor version (v1.2.3 → v1.3.0)
- `-f version="v1.5.0"` - Use specific version

## Safety Checks

**ALWAYS perform these checks before execution:**

1. ✅ **On main branch locally** - Ensures app/chart discovery matches what will be released
2. ✅ **Version intentionality verified** - User explicitly confirmed version bump type
3. ✅ **Current version shown** - User saw what version exists
4. ✅ **Impact explained** - User understands what will be published
5. ✅ **Final confirmation** - User gave explicit approval
6. ✅ **Workflow runs on main** - Always use `--ref "main"` to execute workflow on main branch

## Error Handling

If user seems unsure, offer to:
- Show current deployed versions
- Explain version semantics (major.minor.patch)
- Run dry-run first
- Cancel and investigate further

## Response Format

After triggering:
```markdown
✅ Release initiated!

**Workflow**: https://github.com/whale-net/everything/actions/runs/12345
**Chart**: helm-manman-control-services
**Version**: v0.3.0

Monitor the workflow for:
- ✓ Build completion
- ✓ Image pushes
- ✓ GitHub release creation
- ✓ Helm chart publication

The release will be available at:
https://github.com/whale-net/everything/releases/tag/helm-manman-control-services.v0.3.0
```

## Notes

- Release builds happen in GitHub Actions, not locally
- Images are pushed to ghcr.io/whale-net
- Helm charts are published to GitHub Pages
- Use `gh run watch` to monitor progress
- Dry runs are recommended for first-time releases
