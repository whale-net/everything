---
name: release
description: Interactive release builder for apps and helm charts
---

# Release Builder

This skill guides you through creating a release for apps and/or helm charts in the Everything monorepo.

## Quick Start Examples

**Release ManManV2 apps and chart with patch bump:**
```bash
gh workflow run release.yml --ref "main" \
  -f apps="manmanv2" \
  -f helm_charts="manmanv2-control-services" \
  -f increment_patch=true \
  -f dry_run=false \
  -f include_demo=false
```

**Release only ManManV2 apps with minor bump:**
```bash
gh workflow run release.yml --ref "main" \
  -f apps="manmanv2" \
  -f increment_minor=true \
  -f dry_run=false \
  -f include_demo=false
```

**Release only a helm chart:**
```bash
gh workflow run release.yml --ref "main" \
  -f helm_charts="manmanv2-control-services" \
  -f increment_patch=true \
  -f dry_run=false \
  -f include_demo=false
```

## CRITICAL: GitHub Workflow Parameter Format

The release workflow (`release.yml`) expects specific parameter formats:

### Apps Parameter (`-f apps="..."`)
Accepts one of:
- **Domain/namespace name**: `"manmanv2"`, `"demo"`, `"friendly_computing_machine"`
- **Comma-separated app names**: `"hello-python,hello-go"`
- **"all"**: Release all apps (excludes demo unless include_demo=true)

### Helm Charts Parameter (`-f helm_charts="..."`)
**CRITICAL**: Chart names should be WITHOUT the "helm-" prefix!
- ✅ CORRECT: `"manmanv2-control-services"`
- ❌ WRONG: `"helm-manmanv2-control-services"`

Accepts one of:
- **Chart name without helm- prefix**: `"manmanv2-control-services"`, `"demo-hello-fastapi"`
- **Domain name**: `"demo"`, `"manmanv2"`
- **Comma-separated chart names**: `"manmanv2-control-services,demo-hello-fastapi"`
- **"all"**: Release all charts (excludes demo unless include_demo=true)

## Process

**If user provides all parameters upfront** (e.g., "release manmanv2 namespace, image and helm chart, patch, no extra"):
- Skip all interactive prompts
- Directly trigger the workflow with the specified parameters
- Provide a simple confirmation message

**If user doesn't provide all parameters** (e.g., just "release"), follow the interactive process:
1. **Select release targets** - Choose which apps/charts to release
2. **Choose version strategy** - Specify version or auto-increment
3. **Review release plan** - See exactly what will be released
4. **Confirm** - Final approval before execution
5. **Execute** - Trigger the release via GitHub workflow

**IMPORTANT**: The GitHub workflow handles all discovery and planning. Do NOT run bazel commands locally to discover apps or plan releases - just trigger the workflow with the appropriate parameters.

## User Interaction

**IMPORTANT**: The workflow always runs on main branch (via `--ref "main"`), regardless of your local branch. You can trigger releases from any branch.

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
Ask which chart(s) to release:
```
Question: "Which helm chart(s) would you like to release?"
Header: "Charts"
Options: (multiSelect: true)
  - "manmanv2-control-services" - ManManV2 control services chart
  - "manman-host-services" - ManMan host services chart
  - "friendly-computing-machine-bot-services" - FCM bot services chart
  - "demo" - All demo charts
  - "all" - All production charts (excludes demo by default)
```
**NOTE**: Chart names in the workflow are WITHOUT the "helm-" prefix!

**For Apps (when "Apps Only" or "Both" is selected):**
Ask which app(s) to release:
```
Question: "Which app(s) would you like to release?"
Header: "Apps"
Options: (multiSelect: true)
  - "manmanv2" - All ManManV2 apps (recommended)
  - "demo" - All demo domain apps
  - "friendly_computing_machine" - All FCM apps
  - "all" - All apps (excludes demo by default)
  - "Specify individual apps" - Provide comma-separated app names
```
**NOTE**: Using domain names (like "manmanv2") is recommended over individual app names.

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
- **Chart**: manmanv2-control-services
  - **Namespace**: manmanv2
  - **New Version**: (auto-incremented minor)

## Version Strategy
- Auto-increment minor version

## Options
- Dry run: No
- Include demo: No

## Actions That Will Be Taken
1. GitHub workflow will build helm chart with referenced apps
2. Create git tag: helm-manmanv2-control-services.v<new-version>
3. Push chart to https://charts.whalenet.dev/
4. Create GitHub release with chart artifacts

⚠️  **WARNING**: This will create a public release and cannot be easily undone.
```

**Example for Both Apps and Charts:**
```markdown
# Release Plan Summary

## Apps
- manmanv2 (all apps in namespace)

## Helm Charts
- manmanv2-control-services

## Version Strategy
- Auto-increment minor version (applies to both apps and charts)

## Options
- Dry run: No
- Include demo: No

## Actions That Will Be Taken
1. Build and push app images to ghcr.io/whale-net
2. Create git tags for each app
3. Build helm chart with versioned app references
4. Create git tag for chart
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
  -f helm_charts="manmanv2-control-services" \
  -f increment_minor=true \
  -f dry_run=false \
  -f include_demo=false
```
**NOTE**: Chart name is WITHOUT "helm-" prefix!

**For Apps Only (using domain name):**
```bash
gh workflow run release.yml \
  --ref "main" \
  -f apps="manmanv2" \
  -f increment_patch=true \
  -f dry_run=false \
  -f include_demo=false
```
**NOTE**: Using domain name "manmanv2" releases all apps in that namespace!

**For Both Apps and Helm Charts:**
```bash
gh workflow run release.yml \
  --ref "main" \
  -f apps="manmanv2" \
  -f helm_charts="manmanv2-control-services" \
  -f increment_minor=true \
  -f dry_run=false \
  -f include_demo=false
```

**After triggering, DO NOT run additional commands.** The user can monitor the workflow themselves if they want to.

**Version flags (use exactly one):**
- `-f increment_patch=true` - Bump patch version (v1.2.3 → v1.2.4)
- `-f increment_minor=true` - Bump minor version (v1.2.3 → v1.3.0)
- `-f version="v1.5.0"` - Use specific version

## Safety Checks

**ALWAYS perform these checks before execution:**

1. ✅ **Correct parameter format** - Chart names WITHOUT "helm-" prefix, apps use domain names
2. ✅ **Version intentionality verified** - User explicitly confirmed version bump type
3. ✅ **Impact explained** - User understands what will be published
4. ✅ **Final confirmation** - User gave explicit approval (unless user provides all params upfront)
5. ✅ **Workflow runs on main** - Always use `--ref "main"` to execute workflow on main branch

## Error Handling

If user seems unsure, offer to:
- Show current deployed versions
- Explain version semantics (major.minor.patch)
- Run dry-run first
- Cancel and investigate further

## Response Format

After triggering, provide a simple confirmation:
```markdown
✅ Release workflow triggered!

**Apps**: manmanv2 (all apps in namespace)
**Charts**: manmanv2-control-services
**Version**: Auto-increment patch

The GitHub Actions workflow will handle the rest.
```

**DO NOT** automatically fetch the workflow URL or monitor it - let the user do that if they want.

## Notes

- **Everything happens in GitHub Actions** - No local bazel commands needed
- The workflow handles all discovery, planning, building, and publishing
- Images are pushed to ghcr.io/whale-net
- Helm charts are published to https://charts.whalenet.dev/
- Chart names in workflow parameters are WITHOUT "helm-" prefix
- Use domain names for apps (like "manmanv2") rather than individual app names
- Dry runs are recommended for first-time releases

## Quick Reference: Available Domains

- **manmanv2** - ManManV2 apps and control services chart
- **manman** - ManMan v1 host services
- **friendly_computing_machine** - FCM bot services
- **demo** - Demo/example apps and charts
