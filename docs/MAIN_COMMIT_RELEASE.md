# Main Commit Release Mode

## Overview

The main commit release mode allows you to publish the latest main build using the existing filter logic to determine which apps have changed. This mode publishes images with the `latest` tag only, reducing GHCR storage costs.

## Breaking Changes

### CI Workflow Changes

**BREAKING CHANGE**: The CI workflow (`ci.yml`) no longer publishes commit hash tags to GHCR on main builds.

**Before:**
```yaml
# Published tags: latest, abc123def
bazel run //tools:release -- release-multiarch "$APP" --version "latest" --commit "${{ github.sha }}"
```

**After:**
```yaml
# Published tags: latest only
bazel run //tools:release -- release-multiarch "$APP" --version "latest"
```

This change reduces GHCR storage costs by eliminating per-commit tags that accumulate over time.

## Usage

### GitHub Actions Workflow

The release workflow now supports a new `main_commit` option:

```yaml
name: Release
on:
  workflow_dispatch:
    inputs:
      apps:
        description: 'Apps to release'
      main_commit:
        description: 'Publish latest main build with "latest" tag'
        type: boolean
        default: false
```

**Steps to use:**

1. Go to Actions → Release → Run workflow
2. Select apps to release (or "all")
3. Check the "main_commit" checkbox
4. Run workflow

**What happens:**
- Uses existing filter logic to detect changed apps
- Publishes only `latest` tag (no commit hash)
- Skips git tags, release notes, and GitHub releases
- Skips helm chart releases

### CLI Usage

```bash
# Plan a main commit release
bazel run //tools:release -- plan \
  --event-type workflow_dispatch \
  --apps all \
  --main-commit \
  --include-demo

# This will:
# - Detect changed apps using filter logic
# - Set version to "latest"
# - Output release matrix for CI
```

## Version Modes

The release system now supports 4 mutually exclusive version modes:

1. **Specific version**: `--version v1.0.0`
   - Use a specific semantic version
   - Creates git tags and GitHub releases

2. **Increment minor**: `--increment-minor`
   - Auto-increment minor version (e.g., v1.0.0 → v1.1.0)
   - Creates git tags and GitHub releases

3. **Increment patch**: `--increment-patch`
   - Auto-increment patch version (e.g., v1.0.0 → v1.0.1)
   - Creates git tags and GitHub releases

4. **Main commit** (NEW): `--main-commit`
   - Use "latest" tag
   - Uses filter logic to detect changed apps
   - No git tags, no GitHub releases
   - No helm chart releases

## Behavior Comparison

| Feature | Regular Release | Main Commit Release |
|---------|----------------|---------------------|
| Version tag | Semantic (e.g., v1.0.0) | latest |
| Commit hash tag | Yes (via --commit) | No |
| Git tags | Yes | No |
| GitHub releases | Yes | No |
| Helm charts | Yes | No (skipped) |
| Filter logic | Optional | Always enabled |

## Examples

### Example 1: Release all changed apps from main

```bash
# Via GitHub Actions
# 1. Go to Actions → Release → Run workflow
# 2. Set apps = "all"
# 3. Check "main_commit"
# 4. Run

# Via CLI
bazel run //tools:release -- plan \
  --event-type workflow_dispatch \
  --apps all \
  --main-commit \
  --format github
```

### Example 2: Release specific changed apps

```bash
# Via GitHub Actions
# 1. Go to Actions → Release → Run workflow
# 2. Set apps = "hello-fastapi,status-service"
# 3. Check "main_commit"
# 4. Run

# Only apps that have changes will be released
```

### Example 3: Regular versioned release (unchanged)

```bash
# Via GitHub Actions
# 1. Go to Actions → Release → Run workflow
# 2. Set apps = "all"
# 3. Set version = "v1.2.3"
# 4. Run

# Creates git tags, GitHub releases, and supports helm charts
```

## When to Use Each Mode

### Use Main Commit Mode When:
- You want to publish latest builds to GHCR for testing/staging
- You don't need version tracking via git tags
- You want to reduce GHCR storage costs
- You don't need helm chart releases

### Use Regular Release Modes When:
- You're releasing a production version
- You need version tracking via git tags
- You want GitHub releases with release notes
- You need helm chart releases
- You want commit hash tags for traceability

## Implementation Details

### Filter Logic

Main commit mode uses the same filter logic as CI builds:

1. Auto-detect previous tag (via `get_previous_tag()`)
2. Detect changed apps using `detect_changed_apps(base_commit)`
3. Filter requested apps to only include changed apps
4. Publish with `latest` tag

### Validation

The workflow validates that:
- Only one version mode is selected
- Helm charts cannot be specified with main_commit mode
- Main commit mode uses "latest" version

### Skipped Operations

When using main_commit mode, these operations are skipped:
- Git tag creation (`Create git tag for release` step)
- Release notes generation (`Generate release notes for app` step)
- GitHub release creation (`create-github-releases` job)
- Helm chart releases (validation prevents helm_charts input)
- Commit hash tagging (no --commit flag passed)

## Migration Guide

### If You Were Using Commit Hash Tags

If your deployment process relied on commit hash tags (e.g., pulling `app:abc123def`), you have two options:

1. **Switch to latest tags**: Update deployments to use `:latest` tag
   ```bash
   # Before
   docker pull ghcr.io/owner/demo-app:abc123def
   
   # After
   docker pull ghcr.io/owner/demo-app:latest
   ```

2. **Use versioned releases**: Use the regular release workflow with semantic versions
   ```bash
   # Deploy specific versions
   docker pull ghcr.io/owner/demo-app:v1.2.3
   ```

### If You Were Using CI Builds for Testing

CI builds now only publish `:latest` tags. Update your testing/staging deployments:

```yaml
# staging/production deployments
image: ghcr.io/owner/demo-app:latest
imagePullPolicy: Always  # Important for latest tags
```

## Future Considerations

The main commit mode could be extended to:
- Support a custom base commit for change detection
- Allow optional commit hash suffix (e.g., `latest-abc123`)
- Support publishing to different registries for staging vs production
