# Helm Chart Repository on GitHub Pages

This repository automatically publishes Helm charts to GitHub Pages, creating a public Helm chart repository.

## Overview

The Helm chart repository is hosted at:
```
https://whale-net.github.io/everything/charts
```

Charts are versioned and published automatically during the release workflow, maintaining a complete history of all chart versions in the repository index.

## For Users: Installing Charts

### Add the Repository

```bash
helm repo add everything https://whale-net.github.io/everything/charts
helm repo update
```

### Search for Charts

```bash
# List all available charts
helm search repo everything

# Search for specific chart
helm search repo everything/hello-fastapi

# Show all versions of a chart
helm search repo everything/hello-fastapi --versions
```

### Install a Chart

```bash
# Install latest version
helm install my-release everything/hello-fastapi

# Install specific version
helm install my-release everything/hello-fastapi --version v1.0.0

# With custom values
helm install my-release everything/hello-fastapi \
  --set apps.hello_fastapi.replicas=3 \
  --set ingress.className=nginx
```

### Upgrade a Chart

```bash
# Upgrade to latest version
helm upgrade my-release everything/hello-fastapi

# Upgrade to specific version
helm upgrade my-release everything/hello-fastapi --version v1.1.0
```

## For Maintainers: Publishing Charts

### Chart Versioning

Charts use **independent versioning** - each chart maintains its own semantic version based on its changes:

- Chart versions are stored as git tags: `<chart-name>.v1.2.3` (e.g., `demo-hello-fastapi.v1.2.3`)
- Chart names in tags have the `helm-` prefix removed for clarity (since they're in a Helm repo)
- Versions auto-increment based on changes (patch by default)
- Versions are independent from app/release versions

Version bump types:
- **Patch** (v1.2.3 → v1.2.4): Bug fixes, minor updates (default)
- **Minor** (v1.2.3 → v1.3.0): New features, backwards-compatible changes
- **Major** (v1.2.3 → v2.0.0): Breaking changes

#### Example Version Flow

```bash
# First release of hello-fastapi chart (internally helm-demo-hello-fastapi)
git tag demo-hello-fastapi.v1.0.0
git push origin demo-hello-fastapi.v1.0.0

# Make some updates, release with patch bump (auto)
bazel run //tools:release -- build-helm-chart helm-demo-hello-fastapi --auto-version
# Creates v1.0.1 from v1.0.0

# Add new feature, use minor bump
bazel run //tools:release -- build-helm-chart helm-demo-hello-fastapi --auto-version --bump minor
# Creates v1.1.0 from v1.0.1

# Meanwhile, demo-workers chart has its own version
git tag workers-demo-workers.v0.5.0
bazel run //tools:release -- build-helm-chart helm-workers-demo-workers --auto-version
# Creates v0.5.1 - independent from hello-fastapi
```

After building, tag the new chart versions:
```bash
git tag demo-hello-fastapi.v1.1.0
git push origin demo-hello-fastapi.v1.1.0
```

### Automatic Publishing

Charts are automatically published during the release workflow when the `helm_charts` input is specified:

```yaml
# In GitHub Actions workflow dispatch
helm_charts: "hello-fastapi,demo-workers"  # Specific charts
helm_charts: "demo"                        # All charts in demo domain
helm_charts: "all"                         # All charts
```

The workflow will:
1. Determine the appropriate version for each chart automatically
2. Build charts with their independent versions
3. Package charts as versioned tarballs (e.g., `hello-fastapi-v1.2.3.tgz`)
4. Update the Helm repository index (`index.yaml`)
5. Push changes to the `gh-pages` branch
6. Automatically deploy to GitHub Pages

### Manual Publishing

You can also manually publish charts using the release helper tool:

```bash
# Package chart with auto-versioning (reads from git tags, increments patch version)
bazel run //tools:release -- build-helm-chart hello-fastapi \
  --output-dir /tmp/charts \
  --use-released

# Package chart with auto-versioning (minor bump)
bazel run //tools:release -- build-helm-chart hello-fastapi \
  --output-dir /tmp/charts \
  --bump minor

# Package chart with explicit version (disable auto-versioning)
bazel run //tools:release -- build-helm-chart hello-fastapi \
  --version v2.0.0 \
  --no-auto-version \
  --output-dir /tmp/charts

# Publish to GitHub Pages
bazel run //tools:release -- publish-helm-repo /tmp/charts \
  --owner whale-net \
  --repo everything
```

### Generate Index Locally

```bash
# Generate index.yaml for charts
bazel run //tools:release -- generate-helm-index /tmp/charts \
  --base-url https://whale-net.github.io/everything/charts

# Merge with existing index
bazel run //tools:release -- generate-helm-index /tmp/charts \
  --base-url https://whale-net.github.io/everything/charts \
  --merge-with /path/to/existing/index.yaml
```

## Architecture

### Workflow Integration

```
Release Workflow
    │
    ├─→ Build Apps (docker images)
    │
    └─→ Release Helm Charts
         ├─→ Build charts with version
         ├─→ Package as .tgz files
         ├─→ Generate/merge index.yaml
         └─→ Push to gh-pages branch
              │
              └─→ GitHub Pages Workflow
                   └─→ Deploy to Pages
```

### Concurrency Control

The release workflow includes concurrency control to prevent race conditions:

```yaml
concurrency:
  group: helm-repo-publish-${{ github.ref }}
  cancel-in-progress: false
```

This ensures that multiple releases don't try to push to `gh-pages` simultaneously, which would cause conflicts.

### Chart Versioning

Charts are versioned using the release version:

- **Chart Version**: Matches the release version (e.g., `v1.0.0`)
- **File Naming**: `<chart-name>-<version>.tgz` (e.g., `hello-fastapi-v1.0.0.tgz`)
- **History**: All versions are maintained in the index, allowing users to install any previous version

### Repository Structure

The `gh-pages` branch contains:

```
gh-pages/
├── index.yaml                      # Helm repository index
├── hello-fastapi-v1.0.0.tgz       # Chart version 1.0.0
├── hello-fastapi-v1.1.0.tgz       # Chart version 1.1.0
├── demo-workers-v1.0.0.tgz        # Another chart
└── README.md                       # Repository documentation
```

The `index.yaml` file is the critical component that Helm uses to discover charts:

```yaml
apiVersion: v1
entries:
  hello-fastapi:
    - name: hello-fastapi
      version: v1.1.0
      urls:
        - https://whale-net.github.io/everything/charts/hello-fastapi-v1.1.0.tgz
      created: "2025-09-30T10:00:00Z"
      digest: sha256:...
    - name: hello-fastapi
      version: v1.0.0
      urls:
        - https://whale-net.github.io/everything/charts/hello-fastapi-v1.0.0.tgz
      created: "2025-09-29T10:00:00Z"
      digest: sha256:...
```

## GitHub Pages Configuration

### Enable GitHub Pages

1. Go to repository **Settings** → **Pages**
2. Set **Source** to: Deploy from a branch
3. Set **Branch** to: `gh-pages` / `/ (root)`
4. Click **Save**

The `pages.yml` workflow handles deployment automatically when the `gh-pages` branch is updated.

### Required Permissions

The release workflow needs these permissions (already configured):

```yaml
permissions:
  contents: write  # To push to gh-pages
  pages: write     # To deploy Pages
  id-token: write  # For Pages deployment
```

## Troubleshooting

### Charts Not Appearing

1. Check that the release workflow completed successfully
2. Verify the `gh-pages` branch exists and contains `.tgz` files
3. Check GitHub Pages is enabled in repository settings
4. Wait 1-2 minutes for Pages deployment to complete

### Helm Repo Update Fails

```bash
# Clear Helm cache and retry
helm repo remove everything
helm repo add everything https://whale-net.github.io/everything/charts
helm repo update
```

### Version Conflicts

If you see "already exists" errors:
- The version already exists in the repository
- Either use a new version number or remove the old version from `gh-pages`

### Index Merge Issues

The system automatically merges with existing index. If issues occur:
1. Check the `gh-pages` branch for corrupted `index.yaml`
2. Regenerate index: `helm repo index . --url <base-url>`

## Testing

### Local Testing

Before publishing, test charts locally:

```bash
# Build chart
bazel build //demo:fastapi_chart

# Test with helm
cd bazel-bin/demo/fastapi_chart_chart/hello-fastapi
helm lint .
helm template test . --debug
helm install test . --dry-run
```

### Validation Commands

```bash
# Verify repository is accessible
curl -I https://whale-net.github.io/everything/charts/index.yaml

# Download and inspect index
curl https://whale-net.github.io/everything/charts/index.yaml

# Verify specific chart is available
helm search repo everything/hello-fastapi --versions
```

## Best Practices

1. **Version Semantics**: Use semantic versioning (v1.0.0, v1.1.0, etc.)
2. **Testing**: Always test charts locally before releasing
3. **Documentation**: Update chart README.md files with release notes
4. **Dry Runs**: Use `dry_run: true` to test the release process
5. **Chart History**: Don't delete old chart versions unless necessary

## Security Considerations

- Charts are publicly accessible (GitHub Pages is public)
- Use appropriate RBAC in chart templates
- Don't include secrets in chart default values
- Review chart templates for security best practices
- Consider signing charts for production use (future enhancement)

## Future Enhancements

Potential improvements:

- **Chart Signing**: Sign charts with GPG keys for verification
- **Provenance Files**: Generate and publish provenance (.prov) files
- **CDN Integration**: Use CloudFlare or similar for faster downloads
- **Chart Testing**: Automated testing with `helm test` before publishing
- **Multi-Repository**: Support multiple chart repositories (stable, dev, etc.)
- **Change-based Version Bumps**: Automatically determine bump type (major/minor/patch) based on the nature of changes

## References

- [Helm Chart Repository Guide](https://helm.sh/docs/topics/chart_repository/)
- [GitHub Pages Documentation](https://docs.github.com/en/pages)
- [Helm Best Practices](https://helm.sh/docs/chart_best_practices/)
