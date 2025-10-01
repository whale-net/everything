# Release Workflow Changes - Support Independent App and Helm Releases

## Summary

The release workflow now supports three operational modes:
1. **App-only release**: Release applications without Helm charts
2. **Helm-only release**: Release Helm charts without applications
3. **Combined release**: Release both applications and Helm charts together

## Changes Made

### 1. Input Parameters
- **`apps`**: Changed from required (`default: 'all'`) to optional (`default: ''`)
  - Leave empty to skip app release
  - Provide app names or "all" to release apps
  
- **`helm_charts`**: Remains optional (`default: ''`)
  - Leave empty to skip helm release
  - Provide chart names, "all", or domain name to release charts

### 2. Validation Logic
Added new validation step to ensure at least one release type is specified:
```yaml
- name: Validate at least one release target
  # Ensures either apps or helm_charts (or both) are specified
```

### 3. Job Dependencies

#### Before (Sequential):
```
validate-inputs → plan-release → release → create-github-releases
                                    ↓
                              release-helm-charts → release-summary
```

#### After (Conditional Branches):
```
validate-inputs → plan-release (if apps != '') → release → create-github-releases
       ↓                                                            ↓
       └─────────────→ release-helm-charts (if helm_charts != '') ─┴→ release-summary
```

### 4. Conditional Job Execution

#### `plan-release` job
- **Condition**: `if: github.event.inputs.apps != ''`
- Skipped when only releasing Helm charts

#### `release` job
- **Condition**: `if: needs.plan-release.result == 'success' && ...`
- Skipped when `plan-release` is skipped

#### `create-github-releases` job
- **Condition**: `if: needs.plan-release.result == 'success' && needs.release.result == 'success' && ...`
- Only runs when both plan and release succeed

#### `release-helm-charts` job
- **New dependencies**: `needs: [validate-inputs, plan-release]`
- **Condition**: 
  ```yaml
  if: |
    github.event.inputs.helm_charts != '' && 
    (needs.plan-release.result == 'success' || needs.plan-release.result == 'skipped')
  ```
- Can now run independently when apps are not being released

#### `release-summary` job
- **Updated dependencies**: `needs: [validate-inputs, plan-release, release, create-github-releases, release-helm-charts]`
- **Condition**: `if: always() && needs.validate-inputs.result == 'success'`
- Handles reporting for whichever jobs actually ran

### 5. Version Determination

The workflow now intelligently determines the version for Helm charts:

```bash
# In plan-helm-chart and build-helm-charts steps:
if plan-release job succeeded:
  Use version from plan-release.outputs.version (app release version)
else:
  Use version from github.event.inputs.version
  If empty, use "auto" (auto-versioning based on git tags)
```

This allows Helm charts to:
- Share the same version as apps when releasing together
- Use their own independent version when releasing alone

## Usage Examples

### Release Apps Only
```yaml
apps: "hello_python,hello_go"
helm_charts: ""  # or leave empty
version: "v1.2.0"  # or use increment options
```

### Release Helm Charts Only
```yaml
apps: ""  # or leave empty
helm_charts: "hello-fastapi,demo-workers"
version: "v2.0.0"  # or use increment options
```

### Release Both Apps and Helm Charts
```yaml
apps: "all"
helm_charts: "all"
version: "v1.5.0"  # Both will use this version
```

### Release Helm Charts with Auto-Versioning
```yaml
apps: ""
helm_charts: "all"
version: ""  # Helm charts will auto-version based on git tags
increment_patch: true  # Each chart increments its own patch version
```

## Benefits

1. **Flexibility**: Can release apps and Helm charts independently or together
2. **Efficiency**: No need to run app builds when only updating Helm charts
3. **Version Control**: Helm charts can maintain independent versioning or sync with app versions
4. **Validation**: Clear error messages when neither target is specified
5. **Backward Compatible**: Existing workflows that specify both continue to work as before

## Testing Scenarios

1. ✅ Release apps only with explicit version
2. ✅ Release apps only with version increment
3. ✅ Release Helm charts only with explicit version
4. ✅ Release Helm charts only with auto-versioning
5. ✅ Release both apps and Helm charts together (shared version)
6. ❌ Release with neither apps nor Helm charts specified (validation error)
7. ❌ Release with multiple version options (validation error)
