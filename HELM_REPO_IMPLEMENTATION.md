# Helm Chart Repository Implementation Summary

**Date**: September 30, 2025
**Branch**: 20250930-3

## Overview

This implementation adds full Helm chart repository support using GitHub Pages, enabling versioned chart publishing with automatic index management and concurrency control to prevent race conditions.

## Changes Made

### 1. Extended Helm Utilities (`tools/release_helper/helm.py`)

Added functions for Helm repository management:

- **`package_chart_with_version()`**: Packages a chart directory into a versioned tarball using `helm package`
- **`generate_helm_repo_index()`**: Generates `index.yaml` for a chart repository
- **`merge_helm_repo_index()`**: Merges new charts with existing index to preserve history
- **`publish_helm_repo_to_github_pages()`**: Publishes charts to GitHub Pages via `gh-pages` branch

Key features:
- Automatic version injection into Chart.yaml
- Intelligent index merging to preserve chart history
- Orphan branch creation if `gh-pages` doesn't exist
- Comprehensive error handling

### 2. Release Helper CLI Commands (`tools/release_helper/cli.py`)

Added three new commands:

```bash
# Publish charts to GitHub Pages
bazel run //tools:release -- publish-helm-repo <charts-dir> \
  --owner <org> --repo <name>

# Generate Helm repository index
bazel run //tools:release -- generate-helm-index <charts-dir> \
  --base-url <url> [--merge-with <existing-index>]

# Existing build command now supports versioning
bazel run //tools:release -- build-helm-chart <chart-name> \
  --version <version> --output-dir <dir> [--use-released]
```

### 3. Release Workflow Updates (`.github/workflows/release.yml`)

#### Global Concurrency Control

Added workflow-level concurrency to prevent race conditions:

```yaml
concurrency:
  group: helm-repo-publish-${{ github.ref }}
  cancel-in-progress: false
```

This ensures only one workflow can publish to `gh-pages` at a time.

#### Enhanced Helm Chart Release Job

- **Permissions**: Added `contents: write`, `pages: write`, `id-token: write`
- **Helm Installation**: Added `azure/setup-helm` action
- **Git Configuration**: Configures bot user for gh-pages commits
- **Versioned Packaging**: Charts are now packaged with version in filename
- **GitHub Pages Publishing**: New step publishes to gh-pages branch
- **Enhanced Summary**: Shows repository URL and usage instructions

#### Chart Naming Convention

Charts are now named with versions:
- Old: `hello-fastapi.tgz`
- New: `hello-fastapi-v1.0.0.tgz`

This allows maintaining multiple versions in the repository.

### 4. GitHub Pages Workflow (`.github/workflows/pages.yml`)

New workflow for automatic deployment:

- Triggers on pushes to `gh-pages` branch
- Uses official GitHub Pages actions
- Includes concurrency control for deployments
- Provides deployment summary with usage instructions

### 5. Documentation (`docs/HELM_REPOSITORY.md`)

Comprehensive documentation covering:

- **User Guide**: How to add and use the repository
- **Maintainer Guide**: How to publish charts
- **Architecture**: Workflow integration and versioning strategy
- **Troubleshooting**: Common issues and solutions
- **Security**: Considerations and best practices
- **Future Enhancements**: Planned improvements

## Usage

### For Users

```bash
# Add the repository
helm repo add everything https://whale-net.github.io/everything
helm repo update

# Install a chart
helm install my-release everything/hello-fastapi

# Install specific version
helm install my-release everything/hello-fastapi --version v1.0.0
```

### For Maintainers

```bash
# Release charts via GitHub Actions
# Set helm_charts input to: "hello-fastapi,demo-workers" or "all"

# Or manually:
bazel run //tools:release -- build-helm-chart hello-fastapi \
  --version v1.2.0 --output-dir /tmp/charts --use-released

bazel run //tools:release -- publish-helm-repo /tmp/charts \
  --owner whale-net --repo everything
```

## Key Benefits

1. **Version History**: All chart versions maintained in repository
2. **No Race Conditions**: Concurrency control prevents conflicts
3. **Automatic Publishing**: Integrated with existing release workflow
4. **Public Access**: Anyone can use the charts via GitHub Pages
5. **Standard Helm Workflow**: Uses official Helm tooling throughout

## Technical Details

### Concurrency Strategy

Two levels of concurrency control:

1. **Workflow Level**: Only one release can publish to gh-pages
2. **Job Level**: GitHub Pages deployment has its own concurrency group

This prevents:
- Multiple workflows updating gh-pages simultaneously
- Multiple Pages deployments interfering with each other

### Chart Versioning

- Version is set in Chart.yaml during packaging
- Tarball filename includes version: `<name>-<version>.tgz`
- Index.yaml tracks all versions with URLs and digests
- Users can install any published version

### Branch Strategy

- **Main Branch**: Contains source code and build definitions
- **gh-pages Branch**: Contains published charts and index
  - Orphan branch (no history from main)
  - Updated by release workflow
  - Deployed by Pages workflow

## Testing Recommendations

1. **Initial Setup**:
   ```bash
   # Enable GitHub Pages in repository settings
   # Source: gh-pages branch, / (root) directory
   ```

2. **First Release**:
   ```bash
   # Use dry_run to test without publishing
   # Verify charts build correctly with versions
   # Check workflow logs for any issues
   ```

3. **Verify Repository**:
   ```bash
   # After first successful publish
   curl https://whale-net.github.io/everything/index.yaml
   helm repo add everything https://whale-net.github.io/everything
   helm search repo everything
   ```

## Dependencies

### Python Packages

No new Python package dependencies (uses subprocess to call helm CLI)

### Required Tools

- **Helm CLI**: Installed via `azure/setup-helm@v4` action
- **Git**: Already available in GitHub Actions runners

### Helm CLI Commands Used

- `helm package`: Package chart directories
- `helm repo index`: Generate/update index.yaml

## Migration Notes

### From Current Setup

No breaking changes:
- Existing chart build process unchanged
- Artifact uploads still work
- New publishing is opt-in via workflow completion

### Enabling for Existing Repository

1. Merge this branch
2. Enable GitHub Pages in settings
3. Run release workflow with `helm_charts` input
4. Verify deployment at `https://whale-net.github.io/everything`

## Security Considerations

### Permissions Required

- `contents: write`: To push to gh-pages branch
- `pages: write`: To deploy GitHub Pages
- `id-token: write`: For GitHub Pages OIDC

### Public Accessibility

- GitHub Pages sites are always public
- Charts will be accessible to anyone
- Don't include sensitive data in charts
- Consider this when setting default values

### Recommendations

- Review chart templates for security issues
- Use RBAC in generated Kubernetes manifests
- Don't include secrets in default values
- Add chart signing in future (provenance files)

## Future Enhancements

1. **Chart Signing**: GPG signatures for verification
2. **Provenance Files**: Generate .prov files for charts
3. **Automated Testing**: Run `helm test` before publishing
4. **Multiple Repositories**: Separate stable/dev repos
5. **CDN Integration**: CloudFlare for faster downloads
6. **Chart Deprecation**: Mark old versions as deprecated
7. **Release Notes**: Include in chart annotations

## Files Changed

- `tools/release_helper/helm.py`: +290 lines (new functions)
- `tools/release_helper/cli.py`: +130 lines (new commands)
- `.github/workflows/release.yml`: +60 lines (concurrency + publishing)
- `.github/workflows/pages.yml`: +50 lines (new file)
- `docs/HELM_REPOSITORY.md`: +400 lines (new file)

Total: ~930 lines added

## References

- [Helm Chart Repository Guide](https://helm.sh/docs/topics/chart_repository/)
- [GitHub Pages Documentation](https://docs.github.com/en/pages)
- [GitHub Actions Concurrency](https://docs.github.com/en/actions/using-workflows/workflow-syntax-for-github-actions#concurrency)
