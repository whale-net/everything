# Manual Rebuild Workflow

The manual rebuild workflow allows you to manually trigger a rebuild and optionally re-publish container images for selected apps. This is useful when:

- A commit fails and CI doesn't refresh the latest build
- You need to rebuild images without making code changes
- You want to test the build pipeline for specific apps

## How to Use

### Via GitHub UI

1. Go to **Actions** → **Manual Rebuild** → **Run workflow**
2. Configure the inputs:
   - **Apps**: Specify which apps to rebuild (see patterns below)
   - **Include demo domain**: Include demo apps when using "all"
   - **Skip tests**: Skip running tests (faster, but less safe)
   - **Publish**: Publish images to registry (only works on latest main commit)

### Input Patterns

The `apps` input supports multiple formats:

- **`all`**: Rebuild all apps (excludes demo domain by default)
- **Domain name** (e.g., `demo`, `manman`): Rebuild all apps in that domain
- **CSV of app names** (e.g., `hello_python,hello_go,experience_api`): Rebuild specific apps

### Examples

**Rebuild all non-demo apps (dry run):**
```yaml
apps: all
include_demo: false
skip_tests: false
publish: false
```

**Rebuild all demo apps and publish:**
```yaml
apps: demo
include_demo: true (not needed when specifying domain directly)
skip_tests: false
publish: true  # Only works on latest main commit
```

**Rebuild specific apps quickly (no tests):**
```yaml
apps: hello_python,hello_fastapi
skip_tests: true
publish: false
```

**Full rebuild and publish for production apps:**
```yaml
apps: experience_api,worker_dal_api,status_api
skip_tests: false
publish: true  # Only works on latest main commit
```

## Workflow Stages

### 1. Validation
- Checks that workflow is running on `main` branch
- Verifies if running on the latest main commit
- If `publish` is `true` but not on latest main, the workflow fails

### 2. Build
- Builds all targets to verify compilation
- Uses Bazel remote cache for speed

### 3. Test (Optional)
- Runs all tests (can be skipped with `skip_tests: true`)
- Includes container architecture tests to verify cross-compilation
- Uploads test results as artifacts

### 4. Plan Rebuild
- Uses the release tool to determine which apps to rebuild
- Supports the same app selection logic as the release workflow
- Generates a matrix for parallel rebuilds

### 5. Rebuild
- Builds Docker images for selected apps using the release tool
- Runs in parallel for all selected apps
- If `publish` is `true` and on latest main:
  - Publishes multi-architecture images to GitHub Container Registry
  - Updates `latest` tag
  - Creates commit-specific tag (e.g., `app:abc123def`)

### 6. Summary
- Reports overall status
- Shows which apps were rebuilt
- Indicates whether images were published

## Publishing Behavior

Publishing is **idempotent** and only occurs when:

1. `publish` input is set to `true`
2. Workflow is running on the `main` branch
3. The commit is the **latest** commit on main

This ensures that only the most recent code is published to the registry.

### Why Latest Commit Check?

The latest commit check prevents:
- Publishing stale images from old commits
- Race conditions when multiple commits are pushed quickly
- Accidentally overwriting newer images with older builds

If you need to publish from a specific commit, merge it to main first, then run the workflow immediately.

## Security

- Registry authentication uses `GITHUB_TOKEN` (automatically available)
- Only users with workflow dispatch permissions can run this workflow
- Publishing requires being on the latest main commit

## Comparison with Other Workflows

| Feature | CI Workflow | Release Workflow | Manual Rebuild |
|---------|-------------|------------------|----------------|
| **Trigger** | Automatic (PR/Push) | Manual | Manual |
| **App Selection** | Changed apps only | User-specified | User-specified |
| **Versioning** | `latest` tag | Semantic versions | `latest` tag |
| **Git Tags** | No | Yes (per version) | No |
| **GitHub Releases** | No | Yes | No |
| **Use Case** | Validation | Version releases | Rebuild/refresh |

## Troubleshooting

### "Cannot publish when not on latest main commit"

This error means you're trying to publish from an outdated commit. To fix:

1. Ensure you're on the latest main branch
2. Run `git pull origin main` to get the latest changes
3. Run the workflow again

### "Build failed - compilation is broken"

This indicates actual code issues. Check the build logs for compilation errors.

### "Tests failed"

Tests are failing. Review test logs to identify the issue. You can use `skip_tests: true` for a quick rebuild if tests are temporarily broken, but this is not recommended for production publishing.

### Images not appearing in registry

Check that:
- `publish` was set to `true`
- Workflow ran on latest main commit
- Rebuild job completed successfully
- You have permission to view the registry (private repositories require authentication)

## Related Workflows

- **CI Workflow** (`.github/workflows/ci.yml`): Automatic builds on PR/push
- **Release Workflow** (`.github/workflows/release.yml`): Version-tagged releases with semantic versioning

## Architecture

The manual rebuild workflow reuses the same infrastructure as the CI and release workflows:

- **Release tool** (`//tools:release`): Discovers apps, plans builds, generates matrices
- **Bazel**: Cross-compilation and multi-architecture support
- **OCI Image Index**: Multi-platform container images (amd64 + arm64)
- **GitHub Container Registry**: Image storage and distribution

This ensures consistency across all workflows and reduces maintenance burden.
