# GitHub Container Registry (GHCR) Setup

## Issue Resolution

The CI and release workflows have been updated to fix the "unauthorized" error when pushing Docker images to GitHub Container Registry (GHCR).

## Background

Both the CI workflow (`.github/workflows/ci.yml`) and release workflow (`.github/workflows/release.yml`) push Docker images to GHCR. The `GITHUB_TOKEN` provided by GitHub Actions has limited permissions and lacks the `write:packages` scope required for pushing to GHCR.

## Required Setup

To enable Docker image publishing to GHCR, repository administrators need to create a Personal Access Token (PAT) with the required permissions.

### Step 1: Create a Personal Access Token

1. Go to GitHub Settings → Developer settings → Personal access tokens → Tokens (classic)
2. Click "Generate new token (classic)"
3. Set an expiration date (recommended: 90 days or less for security)
4. Select the following scopes:
   - `write:packages` - Required for pushing images to GHCR
   - `read:packages` - Required for reading existing packages

### Step 2: Add the Secret to Repository

1. Go to your repository Settings → Secrets and variables → Actions
2. Click "New repository secret"
3. Name: `GHCR_PAT`
4. Value: Paste the PAT created in Step 1
5. Click "Add secret"

## Backwards Compatibility

The workflow maintains backwards compatibility by falling back to `GITHUB_TOKEN` if `GHCR_PAT` is not available. However, `GITHUB_TOKEN` has limited permissions and may not work for pushing to GHCR in all cases.

## Verification

After setting up the `GHCR_PAT` secret, test both workflows to ensure Docker images can be successfully pushed to GHCR:

1. **CI Workflow**: Push to the main branch to trigger automatic image pushes
2. **Release Workflow**: Create a release or use the manual dispatch to test the release process

## Security Notes

- Use the minimum required permissions for the PAT
- Set a reasonable expiration date
- Rotate the token regularly
- Monitor usage in GitHub's audit logs