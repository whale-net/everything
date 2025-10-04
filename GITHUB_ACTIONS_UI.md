# GitHub Actions UI - Release Workflow Inputs

## New Workflow Input Layout

When you navigate to **Actions → Release → Run workflow**, you will see the following inputs:

### Input Fields

1. **Apps** (text input)
   - Description: Comma-separated list of apps to release (e.g., hello_python,hello_go) or "all" for all apps. Leave empty to skip app release.
   - Default: (empty)
   - Examples: 
     - `all` - release all production apps (excludes demo by default)
     - `demo` - release all demo domain apps
     - `hello_python,hello_go` - release specific apps

2. **Version** (text input)
   - Description: Release version (e.g., v1.0.0) - leave empty when using increment options
   - Default: (empty)
   - Examples: `v1.0.0`, `v2.1.5`

3. **Auto-increment minor version** (checkbox)
   - Description: Auto-increment minor version (resets patch to 0) - applies to both apps and helm charts
   - Default: ❌ unchecked

4. **Auto-increment patch version** (checkbox)
   - Description: Auto-increment patch version - applies to both apps and helm charts
   - Default: ❌ unchecked

5. **Dry run** (checkbox)
   - Description: Dry run - build but do not publish
   - Default: ❌ unchecked

6. **Helm charts** (text input)
   - Description: Helm charts to release (e.g., hello-fastapi,demo-workers) or "all" or domain name (e.g., "demo"). Leave empty to skip helm chart release.
   - Default: (empty)
   - Examples:
     - `all` - release all production charts (excludes demo by default)
     - `demo` - release all demo domain charts
     - `hello-fastapi` - release specific chart

7. **Include demo domain** (checkbox) ⭐ **NEW**
   - Description: Include demo domain when using "all" for apps or helm charts
   - Default: ❌ unchecked
   - Purpose: When checked, `all` includes demo domain apps/charts

## Usage Scenarios

### Scenario 1: Production Release (Default - Demo Excluded)
```
Apps:                 all
Version:              v2.0.0
Increment minor:      ❌
Increment patch:      ❌
Dry run:              ❌
Helm charts:          all
Include demo domain:  ❌ (unchecked)
```
**Result**: Releases all production apps and charts (manman domain), excludes demo domain

### Scenario 2: Full Release Including Demo
```
Apps:                 all
Version:              v2.0.0
Increment minor:      ❌
Increment patch:      ❌
Dry run:              ❌
Helm charts:          all
Include demo domain:  ✅ (checked)
```
**Result**: Releases all apps and charts including demo domain

### Scenario 3: Demo-Only Release
```
Apps:                 demo
Version:              v1.0.0
Increment minor:      ❌
Increment patch:      ❌
Dry run:              ❌
Helm charts:          demo
Include demo domain:  ❌ (not needed - domain specified explicitly)
```
**Result**: Releases only demo domain apps and charts

### Scenario 4: Specific Apps Release
```
Apps:                 hello_python,hello_go
Version:              v1.5.0
Increment minor:      ❌
Increment patch:      ❌
Dry run:              ❌
Helm charts:          (empty)
Include demo domain:  ❌ (not needed - specific apps)
```
**Result**: Releases only hello_python and hello_go apps

## Key Points

1. **Default Behavior Changed**: `all` now excludes demo domain by default
2. **New Checkbox**: "Include demo domain" must be checked to include demo when using `all`
3. **Not Affected**: Specific app/chart names and domain names are not affected by the checkbox
4. **Safe by Default**: Production releases won't accidentally include demo apps/charts
5. **Explicit Intent**: Users must explicitly check the box to include demo in releases
