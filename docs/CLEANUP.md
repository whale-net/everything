# Tag, Release, and GHCR Package Cleanup Tool

Unified cleanup tool for managing Git tags, GitHub Releases, and GitHub Container Registry (GHCR) packages with intelligent retention policies.

## Overview

As the repository grows, the number of tags, releases, and container images increases over time. Old releases from superseded versions accumulate, making it harder to navigate and manage releases. This tool provides an automated way to prune old releases **atomically** while maintaining a sensible retention policy.

## Features

- 🎯 **Intelligent Retention** - Keeps recent versions while cleaning old ones
- 🔒 **Safety First** - Dry-run by default, confirmation prompts, age thresholds
- 🔄 **Atomic Cleanup** - Deletes Git tags, GitHub Releases, and GHCR packages together
- 💪 **Error Resilient** - Continues on partial failures, reports all errors
- ⚙️ **Flexible Configuration** - Customizable retention policies via CLI flags
- 📦 **Multi-App Support** - Handles multiple apps/charts independently

## What Gets Deleted

When you delete a tag (e.g., `v1.0.0`), the tool automatically removes:

1. **Git Tag** - The lightweight or annotated tag from the repository
2. **GitHub Release** - The release page associated with that tag (if it exists)
3. **GHCR Packages** - All container images tagged with that version (optional)

This ensures complete cleanup without leaving orphaned releases or stale container images.

## Retention Policy

The tool implements intelligent pruning rules:

1. **Latest Patch Only** - Keeps only the latest patch of each minor version
2. **Keep Last N Minors** - Keeps the last N minor versions (default: 2)
3. **Major Version Protection** - Always keeps the latest minor version of each major version (when multiple majors exist)
4. **Age Threshold** - Only prunes tags older than a threshold (default: 14 days)
5. **Minimum Version Requirement** - Requires at least N versions to apply retention

### Example

Given these tags for `demo-hello-python`:

```
v2.0.0 (5 days old)   → KEEP (latest minor, recent)
v1.2.5 (10 days old)  → KEEP (last 2 minor versions, latest of major 1)
v1.2.4 (15 days old)  → DELETE (old patch, not latest of v1.2.x)
v1.1.3 (20 days old)  → DELETE (outside retention window)
v1.0.1 (30 days old)  → DELETE (outside retention window)
```

**Result with default settings (keep last 2 minor versions, prune >14 days old):**
- **Kept:** v2.0.0, v1.2.5
- **Deleted:** v1.2.4, v1.1.3, v1.0.1

### Special Rules

- **v1.2.5 will never be deleted** as long as it remains the latest minor version in major version 1 (even if v2.0.6 is added, v1.2.5 stays unless v1.3.0 is created)
- **Old patches are always deleted** if older than threshold (e.g., v1.2.4 is deleted even though v1.2.5 is kept)
- **Recent tags are always kept** regardless of retention window (safety measure)

## Usage

### Local Usage

```bash
# Preview what would be deleted (recommended first step)
bazel run //tools:release -- cleanup-releases

# Show help and all options
bazel run //tools:release -- cleanup-releases --help

# Actually delete old releases (prompts for confirmation)
bazel run //tools:release -- cleanup-releases --no-dry-run

# Custom retention policy
bazel run //tools:release -- cleanup-releases \
  --keep-minor-versions 3 \
  --min-age-days 30

# Delete tags only (keep GHCR packages)
bazel run //tools:release -- cleanup-releases \
  --no-delete-packages --no-dry-run

# Delete packages only (keep Git tags) - NOT RECOMMENDED
bazel run //tools:release -- cleanup-releases \
  --delete-packages --no-dry-run
```

### GitHub Actions Workflow

The cleanup tool can be run via GitHub Actions:

1. Go to **Actions** tab in GitHub
2. Select **"Cleanup Old Releases"** workflow
3. Click **"Run workflow"** button
4. Configure parameters:
   - `keep_minor_versions`: Number of recent minor versions to keep (default: 2)
   - `min_age_days`: Minimum age in days for deletion (default: 14)
   - `dry_run`: Preview changes without executing (default: true)
   - `delete_packages`: Also delete GHCR packages (default: true)
5. Click **"Run workflow"** to execute

**Recommended workflow:**
1. First run with `dry_run=true` to preview changes
2. Review the output in the workflow summary
3. If satisfied, run again with `dry_run=false` to actually delete

## CLI Options

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `--keep-minor-versions` | integer | 2 | Number of recent minor versions to keep |
| `--min-age-days` | integer | 14 | Minimum age in days for deletion |
| `--dry-run / --no-dry-run` | boolean | true | Preview changes without executing |
| `--delete-packages / --no-delete-packages` | boolean | true | Also delete corresponding GHCR packages |

## Requirements

### Permissions

## Permissions Required

- **Git Tags:** `contents:write` permission
- **GitHub Releases:** `contents:write` permission (same as tags)
- **GHCR Packages:** `packages:write` permission

### Environment Variables

- `GITHUB_TOKEN`: GitHub token with appropriate permissions
- `GITHUB_REPOSITORY_OWNER`: Repository owner (auto-detected in GitHub Actions)
- `GITHUB_REPOSITORY`: Repository name (auto-detected in GitHub Actions)

## Architecture

### Components

1. **GHCR Client** (`tools/release_helper/ghcr.py`)
   - Interfaces with GitHub Container Registry API
   - Lists, searches, and deletes package versions
   - Handles pagination and error recovery

2. **GitHub Release Client** (`tools/release_helper/github_release.py`)
   - Interfaces with GitHub Releases API
   - Finds and deletes releases by tag name
   - Maps tags to release IDs

3. **Cleanup Orchestrator** (`tools/release_helper/cleanup.py`)
   - Coordinates atomic deletion of tags, releases, and packages
   - Implements retention algorithm
   - Maps tags to releases and GHCR packages
   - Generates cleanup plans and execution results

4. **CLI Command** (`tools/release_helper/cli.py`)
   - Provides user-friendly interface
   - Validates inputs and permissions
   - Displays progress and results

### Data Flow

```
┌─────────────────┐
│  CLI Command    │
└────────┬────────┘
         │
         ▼
┌─────────────────┐
│  ReleaseCleanup │ (Orchestrator)
└────────┬────────┘
         │
    ┌────┼────┐
    ▼    ▼    ▼
┌─────┐┌────┐┌──────┐
│ Git ││GH  ││ GHCR │
│Tags ││Rel ││Client│
└─────┘└────┘└──────┘
```

### Cleanup Process (Atomic Deletion)

1. **Planning Phase**
   - Query all Git tags
   - Apply retention algorithm to identify tags to delete
   - Find corresponding GitHub Releases (if any)
   - Find corresponding GHCR packages (if enabled)
   
2. **Execution Phase** (runs in order for atomicity)
   - Delete GitHub Releases first (prevents orphaned releases)
   - Delete Git tags second (removes the tag reference)
   - Delete GHCR packages last (container images)

1. **Planning Phase**
   - Fetch all Git tags
   - Apply retention algorithm to identify deletions
   - Map tags to GHCR package versions
   - Generate cleanup plan

2. **Execution Phase**
   - Delete Git tags (one by one with error handling)
   - Delete GHCR packages (one by one with error handling)
   - Collect results and errors
   - Generate summary report

3. **Reporting Phase**
   - Display deleted tags and packages
   - Show any errors encountered
   - Provide summary statistics

## Safety Features

- ✅ **Dry-run mode** - Preview changes without executing
- ✅ **Age threshold** - Prevents accidental deletion of recent tags
- ✅ **Confirmation prompt** - Requires explicit user approval (local usage)
- ✅ **Conservative parsing** - Skips malformed tags
- ✅ **Latest minor per major protection** - Always keeps latest minor version in each major
- ✅ **Multi-app support** - Handles multiple apps/charts independently
- ✅ **Error recovery** - Continues on partial failures, reports all errors
- ✅ **Audit trail** - Logs all operations and results

## Tag Format Support

Works with both app and Helm chart tags:

- **App tags:** `domain-appname.vX.Y.Z` (e.g., `demo-hello-python.v1.2.3`)
- **Helm chart tags:** `helm-namespace-chartname.vX.Y.Z` (e.g., `helm-demo-app.v1.0.0`)

## GHCR Package Mapping

The tool automatically maps Git tags to GHCR packages:

1. Parse tag to extract package name: `demo-hello-python.v1.2.3` → `demo-hello-python`
2. Extract version: `v1.2.3`
3. Search GHCR for matching package versions
4. Match versions by tag (e.g., find version with tag `v1.2.3`)
5. Include all platform variants (e.g., `v1.2.3-amd64`, `v1.2.3-arm64`)

## Troubleshooting

### No tags found for deletion

**Cause:** All tags are either too recent or within the retention window.

**Solution:** 
- Lower `--min-age-days` threshold
- Increase `--keep-minor-versions` if you want to delete more

### Permission denied errors

**Cause:** `GITHUB_TOKEN` lacks required permissions.

**Solution:** 
- Ensure token has `contents:write` for tags
- Ensure token has `packages:write` for GHCR
- In GitHub Actions, check workflow permissions

### GHCR package not found

**Cause:** Package name parsing failed or package doesn't exist.

**Solution:**
- Check tag format matches expected pattern
- Verify GHCR package exists with correct name
- Use `--no-delete-packages` to skip GHCR cleanup

### Partial failures

**Cause:** Some deletions succeeded, others failed.

**Solution:**
- Review error messages in output
- Re-run cleanup to retry failed deletions
- Check GitHub API rate limits

## Testing

### Run Tests

```bash
# Run all cleanup tests
bazel test //tools/release_helper:test_cleanup

# Run GHCR client tests
bazel test //tools/release_helper:test_ghcr

# Run with verbose output
bazel test //tools/release_helper:test_cleanup --test_output=streamed
```

### Test Coverage

- ✅ Retention algorithm with various scenarios
- ✅ Latest minor per major version protection
- ✅ Multi-app and Helm chart tag support
- ✅ Error handling and edge cases
- ✅ Dry-run vs real execution
- ✅ Tag to package mapping
- ✅ GHCR API interactions

## Files

```
tools/release_helper/
├── ghcr.py                    # GHCR client (324 lines)
├── cleanup.py                 # Cleanup orchestration (410 lines)
├── cli.py                     # CLI integration (modified)
├── test_ghcr.py              # GHCR tests (26 tests)
├── test_cleanup.py           # Cleanup tests (21 tests, all passing)
└── BUILD.bazel               # Build configuration

.github/workflows/
└── cleanup-releases.yml      # GitHub Actions workflow
```

## Best Practices

1. **Always dry-run first** - Preview changes before executing
2. **Start conservative** - Use default retention settings initially
3. **Monitor results** - Check workflow outputs and error logs
4. **Coordinate with team** - Communicate before large cleanups
5. **Test in staging** - Try on test repositories first
6. **Regular cleanup** - Run periodically to prevent accumulation
7. **Keep packages in sync** - Always delete tags and packages together

## Future Enhancements

- [ ] Scheduled cleanup (cron-based GitHub Actions)
- [ ] Slack/email notifications for cleanup results
- [ ] Metrics dashboard showing cleanup trends
- [ ] Support for custom tag patterns
- [ ] Rollback mechanism for accidental deletions
- [ ] Integration with release notes generation

## References

- GitHub Issue: [#241 - Tag cleanup](https://github.com/whale-net/everything/issues/241)
- GHCR API: [GitHub Packages REST API](https://docs.github.com/en/rest/packages)
- Git Tags: [Git Tagging Documentation](https://git-scm.com/book/en/v2/Git-Basics-Tagging)
