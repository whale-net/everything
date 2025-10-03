# Unpublishing Helm Charts

This document describes how to unpublish (remove) specific versions of Helm charts from the repository index.

## Overview

The unpublish workflow allows authorized users to remove specific versions of Helm charts from the repository index. This is useful for:

- **Security Issues**: Removing vulnerable chart versions
- **Broken Releases**: Removing incorrectly released charts
- **Compliance**: Meeting regulatory requirements to remove certain versions

## How It Works

1. **Permission Check**: Verifies the user has admin, maintain, or write access
2. **Download Index**: Fetches the current index.yaml from GitHub Pages
3. **Modify Index**: Removes specified versions from the index
4. **Deploy**: Pushes the updated index to GitHub Pages

**Important**: Unpublishing removes versions from the index.yaml only. The actual .tgz files remain on the gh-pages branch and are accessible via direct URL.

## Using the Workflow

### Via GitHub Actions UI

1. Navigate to **Actions** → **Unpublish Helm Charts**
2. Click **Run workflow**
3. Fill in the inputs:
   - **Chart name**: Full name of the chart (e.g., `hello-fastapi`)
   - **Versions**: Comma-separated list of versions to unpublish (e.g., `v1.0.0,v1.1.0`)
4. Click **Run workflow**

### Required Permissions

The workflow checks that the user has one of the following permission levels:
- **admin** - Full administrative access
- **maintain** - Maintain the repository  
- **write** - Write access to the repository

If you don't have the required permissions, the workflow will fail with an authorization error.

### Example Scenarios

#### Unpublish a Single Version

```yaml
Chart name: hello-fastapi
Versions: v1.0.0
```

#### Unpublish Multiple Versions

```yaml
Chart name: demo-workers
Versions: v0.1.0,v0.2.0,v0.3.0
```

## Using the CLI

For manual operations or scripting:

```bash
# Download current index
curl -o /tmp/index.yaml https://whale-net.github.io/everything/charts/index.yaml

# Unpublish versions
bazel run //tools:release -- unpublish-helm-chart /tmp/index.yaml \
  --chart hello-fastapi \
  --versions v1.0.0,v1.1.0

# Review the changes
cat /tmp/index.yaml

# Manually deploy to gh-pages (requires separate steps)
```

## What Happens After Unpublishing

### Immediate Effects

- ✅ Versions are removed from the Helm repository index
- ✅ `helm search repo` will no longer show the unpublished versions
- ✅ New `helm install` commands cannot use the unpublished versions
- ✅ GitHub Actions workflow run provides an audit trail

### What Doesn't Change

- ❌ Existing deployments using unpublished versions continue to work
- ❌ The .tgz chart files remain on the gh-pages branch
- ❌ Direct URLs to .tgz files still work (if someone knows the URL)
- ❌ Git tags for the chart versions are NOT deleted

### User Impact

After unpublishing, users should run:

```bash
helm repo update
```

to refresh their local cache and see the updated chart list.

## Complete Removal

To completely remove chart versions:

1. **Unpublish via workflow** (removes from index)
2. **Manually delete .tgz files** from gh-pages branch:
   ```bash
   git checkout gh-pages
   git rm charts/hello-fastapi-v1.0.0.tgz
   git commit -m "Remove chart file hello-fastapi-v1.0.0.tgz"
   git push origin gh-pages
   ```

## Security & Audit

### Authorization

- All unpublish requests check GitHub repository permissions
- The workflow uses GitHub's built-in permission system
- Unauthorized attempts are logged and blocked

### Audit Trail

Every unpublish operation creates:
- A GitHub Actions workflow run (visible in Actions tab)
- A deployment to GitHub Pages (visible in Environments)
- A git commit log entry (if using manual CLI method)

### Review Unpublish History

```bash
# View recent workflow runs
gh run list --workflow=unpublish-helm-charts.yml

# View specific run details
gh run view <run-id>
```

## Troubleshooting

### Permission Denied

**Error**: "User does not have sufficient permissions"

**Solution**: Contact a repository administrator to request admin, maintain, or write access.

### Chart Not Found

**Error**: "Chart 'xyz' not found in index"

**Solution**: Verify the chart name exactly matches the name in the index. Use:

```bash
helm search repo everything --versions
```

### Version Not Found

**Warning**: "No versions were removed"

**Solution**: Check that the version strings exactly match (including the 'v' prefix if present):

```bash
# Download and inspect current index
curl https://whale-net.github.io/everything/charts/index.yaml
```

### Unpublished Version Still Visible

**Issue**: Version still appears after unpublishing

**Solution**: 
1. Wait 1-2 minutes for GitHub Pages deployment to complete
2. Run `helm repo update` to refresh local cache
3. Check workflow completed successfully without errors

## Best Practices

1. **Communicate**: Notify users before unpublishing chart versions
2. **Document**: Record the reason for unpublishing in the workflow run or commit message
3. **Alternative**: Consider deprecating instead of unpublishing when possible
4. **Test**: Verify the unpublish worked by running `helm search repo` after deployment
5. **Clean Up**: If security is a concern, also delete the .tgz files from gh-pages

## Related Documentation

- [Helm Repository Documentation](../docs/HELM_REPOSITORY.md)
- [Release Workflow](../.github/workflows/release.yml)
- [Helm CLI Commands](../tools/release_helper/cli.py)
