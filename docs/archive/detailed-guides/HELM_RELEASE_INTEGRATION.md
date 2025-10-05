# Helm Release Integration Summary

## Changes Made

### 1. Simplified Workflow Structure
- **REMOVED**: Separate `.github/workflows/helm-release.yml` workflow
- **UPDATED**: `.github/workflows/release.yml` now handles both apps and helm charts in a single workflow

### 2. Updated Workflow Inputs

**Before:**
```yaml
release_helm_charts: boolean (checkbox to enable)
helm_charts: string (charts to release if enabled)
```

**After:**
```yaml
helm_charts: string (charts to release, empty = skip helm release)
```

**Behavior:**
- Leave `helm_charts` empty → Skip helm chart release (app-only release)
- Specify charts (e.g., "all", "demo", "hello-fastapi") → Release those charts after apps

### 3. Integrated Summary
- `release-summary` job now includes both app and helm chart release information
- Shows helm chart status, version, and artifact download links
- Removed duplicate summary from `release-helm-charts` job

### 4. Updated Documentation
- `docs/HELM_RELEASE.md` now describes the integrated workflow
- Removed references to separate helm-release workflow
- Clarified that helm charts always use released app versions in CI

## Usage

### Release Apps + Helm Charts

Navigate to: **Actions → Release → Run workflow**

**Example 1: Release apps and charts together**
- Apps: `hello_fastapi,hello_internal_api`
- Version: `v2.0.0`
- Helm charts: `demo` (or `all` or `hello-fastapi,hello-internal-api`)
- Result: Apps released at v2.0.0, charts packaged referencing v2.0.0

**Example 2: Release only apps (no helm charts)**
- Apps: `hello_python`
- Version: `v1.5.0`
- Helm charts: *(leave empty)*
- Result: Only app released, no helm charts

**Example 3: Release all apps and all charts (excluding demo)**
- Apps: `all`
- Version: `v3.0.0`
- Helm charts: `all`
- Include demo domain: ❌ (unchecked)
- Result: All production apps and charts released (demo domain excluded)

**Example 4: Release all apps and all charts (including demo)**
- Apps: `all`
- Version: `v3.0.0`
- Helm charts: `all`
- Include demo domain: ✅ (checked)
- Result: All apps and all charts released together including demo domain

## Demo Domain Exclusion (New Feature)

When using `all` for apps or helm charts, the demo domain is now **excluded by default**. This ensures production releases don't accidentally include demo/example applications and charts.

**To include demo domain:**
- Check the "Include demo domain" checkbox in the GitHub Actions workflow
- Or use `--include-demo` flag when calling CLI commands directly

**Behavior:**
- Specific app/chart names (e.g., `hello_python`) are not affected
- Domain names (e.g., `demo`, `manman`) are not affected
- Only the special `all` keyword triggers the exclusion logic

## Benefits

1. **Simpler UX**: One workflow, one place to release everything
2. **Fewer clicks**: No need to enable checkbox, just specify charts
3. **Better defaults**: Empty = skip (not "all")
4. **Atomic releases**: Apps and their charts released together with same version
5. **Cleaner UI**: Single workflow run shows everything

## Workflow Job Flow

```
validate-inputs
       ↓
  plan-release
       ↓
    release (apps) ────────────────┐
       ↓                           │
create-github-releases             │
       ↓                           ↓
release-helm-charts (if charts specified)
       ↓
release-summary (combined)
```

## Testing

Validated:
- ✅ Workflow YAML syntax is valid
- ✅ All CLI commands work correctly
- ✅ Chart building and packaging functional
- ✅ Version resolution from git tags working

Ready for production use!
