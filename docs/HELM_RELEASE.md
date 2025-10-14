# Helm Chart Release System

This document describes the helm chart release system integrated into the CI/CD pipeline.

## Overview

The helm chart release system is integrated into the main CI/CD release workflow. When releasing apps, you can optionally specify helm charts to release with the same version. Helm charts automatically use the app versions from the release and are tracked with git tags.

**Key Features:**
- Integrated into main `release.yml` workflow
- Helm charts use automatic per-chart versioning based on git tags
- Git tags created for each chart release (format: `helm-{chart-name}.v{version}`)
- Auto-increment support (minor/patch) using git tag history
- Optional - specify charts to release or leave empty to skip
- Charts published to GitHub Pages Helm repository
- Chart packages also uploaded as workflow artifacts (.tgz files)

## Architecture

### Components

1. **`helm_chart_metadata` rule** (`tools/release.bzl`)
   - Captures metadata about helm charts (name, version, namespace, domain, apps)
   - Allows querying charts with `bazel query "kind(helm_chart_metadata, //...)"`
   - Similar to `app_metadata` for applications

2. **`release_helm_chart` macro** (`tools/release.bzl`)
   - Convenience macro wrapping `helm_chart` and `helm_chart_metadata`
   - Makes charts discoverable and releasable through CI/CD
   - Usage:
     ```starlark
     release_helm_chart(
         name = "fastapi_chart",
         apps = ["//demo/hello_fastapi:hello_fastapi_metadata"],
         chart_name = "hello-fastapi",
         namespace = "demo",
         domain = "demo",
         chart_version = "1.0.0",
     )
     ```

3. **Helm utilities** (`tools/release_helper/helm.py`)
   - `list_all_helm_charts()` - List all releasable helm charts
   - `get_helm_chart_metadata()` - Get metadata for a specific chart
   - `resolve_app_versions_for_chart()` - Resolve app versions from git tags or use "latest"
   - `package_helm_chart_for_release()` - Build and package a chart with resolved versions

4. **CLI commands** (`tools/release_helper/cli.py`)
   - `list-helm-charts` - List all charts with metadata
   - `build-helm-chart <chart>` - Build and package a chart
   - `plan-helm-release` - Plan a helm chart release (outputs CI matrix)

5. **Workflow** (`.github/workflows/release.yml`)
   - Integrated helm chart release in main release workflow
   - Optional helm chart release after app releases
   - Runs when `helm_charts` input is non-empty

## Usage

### Releasing Apps with Helm Charts

Use the main release workflow to release apps and optionally their helm charts:

**Via GitHub UI (Actions → Release → Run workflow):**

1. **Apps**: Select apps to release (e.g., "all", "hello_python,hello_fastapi", or domain like "demo")
   - **Note**: When using "all", demo domain apps are excluded by default
2. **Version**: Specify version (e.g., "v1.0.0") or use increment options
3. **Helm charts**: Specify charts to release (e.g., "all", "hello-fastapi", or domain like "demo")
   - Leave empty to skip helm chart release
   - Charts will use the app versions from this release
   - **Note**: When using "all", demo domain charts are excluded by default
4. **Include demo domain**: Check this box to include demo domain when using "all" for apps or charts
5. **Dry run**: Test without publishing

**Example Workflows:**

**Example 1: Release production apps only (excluding demo)**
1. Apps: `all`
2. Version: `v2.0.0`
3. Helm charts: `all`
4. Include demo domain: ❌ (unchecked)
5. Result: Releases all manman apps and charts, excludes demo domain

**Example 2: Release everything including demo**
1. Apps: `all`
2. Version: `v2.0.0`
3. Helm charts: `all`
4. Include demo domain: ✅ (checked)
5. Result: Releases all apps and charts from all domains including demo

**Example 3: Release specific apps**
**Example 3: Release specific apps**
1. Release apps: `hello_fastapi`, `hello_internal_api` with version `v2.0.0`
2. Also release helm charts: `hello-fastapi` (or "demo" for all demo charts)
3. System creates git tags: `demo-hello_fastapi.v2.0.0`, `demo-hello_internal_api.v2.0.0`
4. Pushes container images with version tags
5. Builds helm charts that reference the v2.0.0 app images
6. Uploads helm charts as workflow artifacts

### Demo Domain Exclusion

By default, when using `all` for apps or helm charts, the **demo domain is excluded** from releases. This is useful for production releases where you want to avoid publishing demo/example applications and charts.

**Behavior:**
- `--apps all` → Releases all apps **except** those in the demo domain
- `--charts all` → Releases all charts **except** those in the demo domain
- `--apps all --include-demo` → Releases all apps **including** demo domain
- `--charts all --include-demo` → Releases all charts **including** demo domain

**In GitHub Actions:**
- Use the "Include demo domain" checkbox to include demo when using "all"
- Specifying apps/charts by name (e.g., "hello_python") or domain (e.g., "demo") is not affected by this flag

**Via CLI:**
```bash
# Exclude demo domain (default)
bazel run //tools:release -- plan --apps all --version v1.0.0

# Include demo domain
bazel run //tools:release -- plan --apps all --version v1.0.0 --include-demo

# Helm charts - exclude demo (default)
bazel run //tools:release -- plan-helm-release --charts all --version v1.0.0

# Helm charts - include demo
bazel run //tools:release -- plan-helm-release --charts all --version v1.0.0 --include-demo
```

### App-Only Releases

To release only apps without helm charts:
- Leave the `Helm charts` input empty or don't specify any charts
- Workflow will skip the helm chart release job

### Helm-Only Releases (Advanced)

For releasing only helm charts without apps, use the CLI directly:

```bash
# Build and package charts using latest released app versions (default behavior)
bazel run //tools:release -- build-helm-chart hello-fastapi \
  --version v1.5.0 \
  --output-dir /tmp/charts

# Or explicitly use --use-released (same as default)
bazel run //tools:release -- build-helm-chart hello-fastapi \
  --version v1.5.0 \
  --output-dir /tmp/charts \
  --use-released

# Use "latest" tag for app images (not recommended for production)
bazel run //tools:release -- build-helm-chart hello-fastapi \
  --version v1.5.0 \
  --output-dir /tmp/charts \
  --no-use-released
```

### Local Testing

```bash
# List all helm charts
bazel run //tools:release -- list-helm-charts

# Build a chart locally with released app versions (default)
bazel run //tools:release -- build-helm-chart hello-fastapi \
  --version v1.0.0 \
  --output-dir /tmp/charts

# Build a chart with "latest" tags (for local development)
bazel run //tools:release -- build-helm-chart hello-fastapi \
  --version v1.0.0 \
  --output-dir /tmp/charts \
  --no-use-released

# Plan a helm release
bazel run //tools:release -- plan-helm-release \
  --charts demo \
  --version v1.0.0 \
  --format json
```

## Version Resolution

Helm charts depend on app versions. In the CI workflow, helm charts **always use released versions** from git tags of the apps released in the same workflow run.

### How It Works

When you release apps with version `v1.0.0`:
1. Apps are released and tagged as `{domain}-{app}.v1.0.0`
2. Container images are pushed with `v1.0.0` tag
3. Helm chart build queries git tags to find `demo-hello_fastapi.v1.0.0`
4. Charts are packaged referencing these specific versions

### Local Development

For local development and testing, you can control version resolution:

**Use Released Versions (default behavior)**
- Queries git for latest tags matching `{domain}-{app_name}.v*`
- Example: For `hello_fastapi` in `demo` domain, finds `demo-hello_fastapi.v1.2.3`
- **Raises an error** if no tags found (ensures versioned charts always use semver tags)
- Flags: `--use-released` (explicit) or no flag (default)

**Use Latest Tags (for local development)**
- Uses `"latest"` for all app versions
- Suitable for development or when apps aren't formally released
- Flag: `--no-use-released`

## Version Management

### Helm Chart Versioning

Each Helm chart maintains its own independent version using git tags. Chart versions are tracked using the format: `{chart-name}.v{version}`.

**Chart Naming Convention:**
Charts are automatically prefixed with `helm-{namespace}-` to make artifacts clearly identifiable:
- Input: `chart_name = "hello-fastapi"`, `namespace = "demo"`
- Result: Chart name = `helm-demo-hello-fastapi`
- Tarball: `helm-demo-hello-fastapi-v0.2.1.tgz`
- Git tag: `helm-demo-hello-fastapi.v0.2.1`

**Git Tag Format Examples:**
- `helm-demo-hello-fastapi.v1.5.0` - hello-fastapi chart in demo namespace, version 1.5.0
- `helm-manman-manman-host.v0.2.1` - manman-host chart in manman namespace, version 0.2.1
- `helm-workers-demo-workers.v0.1.0` - demo-workers chart in workers namespace, version 0.1.0

**Auto-increment behavior:**
- The release workflow automatically determines the next chart version by:
  1. Finding the latest git tag for the chart (e.g., `helm-manman-host.v0.2.0`)
  2. Incrementing based on the selected bump type (patch or minor)
  3. Creating a new tag (e.g., `helm-manman-host.v0.2.1`)

**First release:**
- If no previous tag exists, starts with `v0.0.1` (patch) or `v0.1.0` (minor)

### Why Omit chart_version in BUILD Files?

The `chart_version` parameter in `release_helm_chart()` should typically be omitted because:

1. **Single source of truth**: Git tags are the authoritative source for released versions
2. **Prevents staleness**: Hardcoded versions in BUILD files become outdated after each release
3. **Development clarity**: The default `"0.0.0-dev"` clearly indicates non-release builds
4. **CI/CD override**: The release system always overrides the BUILD file version anyway

**When to specify chart_version:**
- Testing a specific version number locally before release
- Creating a one-off chart package outside the release system
- Development scenarios where you need a specific version for debugging

**Typical usage:**
```starlark
# Recommended: Omit chart_version (uses default "0.0.0-dev")
release_helm_chart(
    name = "my_chart",
    chart_name = "my-app",
    domain = "demo",
    namespace = "demo",
    apps = ["//demo/my_app:my_app_metadata"],
)
```

### App Versioning

Apps use a different tagging format: `{domain}-{app}.v{version}` (e.g., `demo-hello_fastapi.v1.0.0`).

This separation allows:
- Independent versioning of charts and apps
- Charts can reference multiple app versions
- Clear distinction between infrastructure (charts) and application code

## Workflow Integration

### Main Release Workflow (`.github/workflows/release.yml`)

**Inputs:**
- `apps` - Apps to release (required)
- `version` / `increment_minor` / `increment_patch` - Version selection (required, mutually exclusive)
- `helm_charts` - Charts to release (optional, empty = skip helm release)
- `dry_run` - Build but don't publish

**Jobs:**
1. `validate-inputs` - Validate version options
2. `plan-release` - Determine which apps to release
3. `release` - Build and release apps (matrix strategy)
4. `create-github-releases` - Create GitHub releases
5. `release-helm-charts` - Build helm charts (conditional, runs if `helm_charts` is non-empty)
6. `release-summary` - Generate combined summary with apps and helm charts

**Outputs:**
- App container images pushed to registry
- Git tags created for apps (format: `{domain}-{app}.v{version}`)
- Git tags created for helm charts (format: `helm-{chart-name}.v{version}`)
- Helm charts published to GitHub Pages at `https://{owner}.github.io/{repo}/charts`
- Helm chart tarballs uploaded as workflow artifacts
- GitHub releases created with release notes for apps
- Combined summary showing both apps and charts

## File Locations

- **Bazel rules:** `tools/release.bzl`
- **Python utilities:** `tools/release_helper/helm.py`
- **CLI commands:** `tools/release_helper/cli.py`
- **Workflow:** `.github/workflows/release.yml` (integrated)
- **Example charts:** `demo/BUILD.bazel`

## Chart Declaration

Convert existing `helm_chart` declarations to use `release_helm_chart`:

```starlark
# Before
load("//tools/helm:helm.bzl", "helm_chart")

helm_chart(
    name = "fastapi_chart",
    apps = ["//demo/hello_fastapi:hello_fastapi_metadata"],
    chart_name = "hello-fastapi",
    namespace = "demo",
    chart_version = "1.0.0",
)

# After
load("//tools:release.bzl", "release_helm_chart")

release_helm_chart(
    name = "fastapi_chart",
    apps = ["//demo/hello_fastapi:hello_fastapi_metadata"],
    chart_name = "hello-fastapi",
    namespace = "demo",
    domain = "demo",  # Required for release system
    # chart_version omitted - defaults to "0.0.0-dev" for local builds
    # Release system auto-versions from git tags (helm-hello-fastapi.v*)
)
```

**Important:** The `chart_version` parameter should typically be omitted:
- **Local builds**: Uses default `"0.0.0-dev"` to indicate it's a development build
- **CI/CD releases**: The release system automatically determines the version from git tags
- **Manual version**: Only specify `chart_version` if you need a specific version for local testing

This automatically creates:
- The helm chart target (`:fastapi_chart`)
- Metadata target (`:fastapi_chart_chart_metadata`)
- Makes the chart discoverable for releases

## Future Enhancements

The following features are planned for future iterations:

1. **Helm repository publishing** - Publish charts to GitHub Pages
2. **Chart version injection** - Dynamically inject app versions during build
3. **Chart signing** - Sign charts with GPG keys for verification
4. **Chart testing** - Automated chart installation and validation
5. **Multi-environment charts** - Different chart variants per environment

## Testing

Validated scenarios:
- ✅ List all helm charts
- ✅ Get chart metadata
- ✅ Resolve app versions from git tags
- ✅ Build helm chart packages
- ✅ Plan helm releases (matrix generation)
- ✅ Package charts with correct naming

Example test output:
```bash
$ bazel run //tools:release -- list-helm-charts
demo-all-types (domain: demo, namespace: demo, apps: hello_fastapi, hello_internal_api, hello_worker, hello_job)
demo-workers (domain: demo, namespace: workers, apps: hello_python, hello_go)
hello-fastapi (domain: demo, namespace: demo, apps: hello_fastapi)

$ bazel run //tools:release -- build-helm-chart hello-fastapi --version v1.0.0 --output-dir /tmp/charts
Packaging chart 'hello-fastapi' version v1.0.0
App versions: {'hello_fastapi': 'v0.0.11'}
Building bazel target: //demo:fastapi_chart
✅ Chart packaged: /tmp/charts/hello-fastapi-v1.0.0.tgz
```
