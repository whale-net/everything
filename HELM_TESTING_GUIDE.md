# Helm Chart Release System - Testing Guide

## Testing the GitHub Pages Publishing Fix

The helm chart release system has been fixed to properly publish to GitHub Pages. Here's how to test the changes:

### 1. Manual Workflow Dispatch Test

1. Go to your repository on GitHub
2. Navigate to **Actions** → **Helm Chart Release**
3. Click **"Run workflow"**
4. Set the parameters:
   - **Domain**: `manman` (or `all` for all domains)
   - **Chart version**: `1.0.0`
   - **Publish to Pages**: ✅ **Enable this**

### 2. Expected Workflow Behavior

The workflow should now:
- ✅ Discover helm chart targets properly
- ✅ Filter by domain if specified
- ✅ Build charts with templates included
- ✅ Consolidate artifacts correctly using Chart.yaml detection
- ✅ Package charts with proper Helm structure
- ✅ Generate repository index with correct GitHub Pages URLs
- ✅ Deploy to GitHub Pages successfully

### 3. Verification Steps

After a successful workflow run:

1. **Check GitHub Pages**: Visit `https://[username].github.io/everything/index.yaml`
2. **Verify Chart Repository**: The index should list available charts
3. **Test Chart Installation**: 
   ```bash
   helm repo add everything https://[username].github.io/everything
   helm repo update
   helm search repo everything
   ```

### 4. Key Fixes Applied

- **Fixed artifact consolidation**: Now finds Chart.yaml files correctly
- **Fixed URL construction**: Uses proper GitHub repository variables
- **Added missing permissions**: Workflow now has `pages: write` permissions
- **Enhanced Bazel rule**: Includes Helm templates in generated charts
- **Added domain filtering**: Supports selective chart building
- **Improved debugging**: Better error messages and validation

### 5. Expected Chart Structure

Each generated chart now includes:
- `Chart.yaml` - Helm chart metadata
- `values.yaml` - Configuration with domain and image settings
- `templates/` - Kubernetes manifests (_helpers.tpl, deployment.yaml, service.yaml)

The charts should be valid Helm packages that can be installed in Kubernetes clusters.