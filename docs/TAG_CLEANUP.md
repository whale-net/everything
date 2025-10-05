# GitHub Tag Cleanup Tool

## Overview

The `cleanup-old-tags` command helps clean up old Git tags on GitHub based on semantic versioning and age criteria.

## Rules

The tool follows these rules when identifying tags to prune:

1. **Keep the last N minor versions** (default: 2) - All patches of these versions are kept
2. **Keep the latest patch of each older minor version** - Even if they're old
3. **Only prune tags older than minimum age** (default: 14 days)

## Usage

### Dry Run (Recommended First)

See what would be deleted without actually deleting:

```bash
bazel run //tools:release -- cleanup-old-tags --dry-run
```

### Delete Old Tags

Actually delete the identified tags:

```bash
bazel run //tools:release -- cleanup-old-tags
```

**⚠️ WARNING**: This will permanently delete tags from GitHub. The command will ask for confirmation before proceeding.

### Custom Parameters

```bash
# Keep last 3 minor versions and prune tags older than 30 days
bazel run //tools:release -- cleanup-old-tags \
  --keep-minor-versions 3 \
  --min-age-days 30

# Dry run with custom parameters
bazel run //tools:release -- cleanup-old-tags \
  --keep-minor-versions 3 \
  --min-age-days 30 \
  --dry-run
```

## Examples

### Example Scenario

Given these tags for `demo-hello_python`:

- `demo-hello_python.v2.0.0` (5 days old)
- `demo-hello_python.v1.2.5` (10 days old)
- `demo-hello_python.v1.2.4` (15 days old)
- `demo-hello_python.v1.1.3` (20 days old)
- `demo-hello_python.v1.1.2` (25 days old)
- `demo-hello_python.v1.0.1` (30 days old)

With default settings (keep last 2 minor versions, prune >14 days old):

**Kept:**
- `demo-hello_python.v2.0.0` - In last 2 minor versions (v2.0)
- `demo-hello_python.v1.2.5` - In last 2 minor versions (v1.2)
- `demo-hello_python.v1.2.4` - In last 2 minor versions (v1.2)
- `demo-hello_python.v1.1.3` - Latest patch of v1.1
- `demo-hello_python.v1.0.1` - Latest patch of v1.0

**Pruned:**
- `demo-hello_python.v1.1.2` - Older patch of v1.1, >14 days old

### Multiple Apps

The tool handles multiple apps independently:

```
demo-hello_python: keeps v2.0.x and v1.2.x completely
demo-hello_go: keeps v3.0.x and v2.5.x completely
helm-demo-chart: keeps v1.5.x and v1.4.x completely
```

## Requirements

- GitHub token with `contents:write` permission (set as `GITHUB_TOKEN` environment variable)
- Repository owner and name (auto-detected from environment or can be specified)

## Environment Variables

The command uses these environment variables:

- `GITHUB_TOKEN` - GitHub personal access token or workflow token
- `GITHUB_REPOSITORY_OWNER` - Repository owner (auto-detected in GitHub Actions)
- `GITHUB_REPOSITORY` - Full repository name (auto-detected in GitHub Actions)

## Safety Features

1. **Dry run by default** - Use `--dry-run` to preview changes
2. **Age threshold** - Only considers tags older than minimum age
3. **Confirmation prompt** - Asks for confirmation before actual deletion
4. **Conservative parsing** - Skips tags that don't follow expected format
5. **Keeps latest patches** - Always keeps the latest patch of each minor version

## Tag Format

The tool works with tags in these formats:

- App tags: `domain-appname.vX.Y.Z` (e.g., `demo-hello_python.v1.2.3`)
- Helm chart tags: `helm-namespace-chartname.vX.Y.Z` (e.g., `helm-demo-app.v1.0.0`)

Tags that don't follow this format are ignored.

## Best Practices

1. **Always test with --dry-run first**
2. **Start with conservative settings** (higher min-age-days, more kept minor versions)
3. **Run periodically** (e.g., monthly) to keep tags manageable
4. **Monitor the output** to ensure expected tags are being pruned

## Troubleshooting

### No tags identified for pruning

This is normal if:
- All tags are recent (younger than min-age-days)
- You only have tags from the last 2 minor versions
- All older minor versions only have their latest patch

### Permission denied

Ensure your `GITHUB_TOKEN` has `contents:write` permission. In GitHub Actions workflows, add:

```yaml
permissions:
  contents: write
```

### Tags not being deleted

Check that:
- Tags exist on the remote (GitHub)
- You answered "yes" to the confirmation prompt
- Your token has sufficient permissions
