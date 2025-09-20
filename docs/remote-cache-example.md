# Example: Using Remote Cache in CI

This example shows how to configure a workflow to use Bazel remote cache for improved build performance.

## Workflow Configuration

```yaml
name: CI with Remote Cache
on:
  push:
    branches: [main]
  pull_request:
    branches: [main]

jobs:
  test:
    name: Test with Remote Cache
    runs-on: ubuntu-latest
    steps:
    - name: Checkout code
      uses: actions/checkout@v4
      with:
        fetch-depth: 0
        
    # Setup build environment - remote cache auto-configured from secrets
    - name: Setup Build Environment
      uses: ./.github/actions/setup-build-env
      with:
        cache-suffix: 'test'
        
    - name: Run tests
      run: |
        bazel test //...
```

## Required Secrets

Set these secrets in your GitHub repository settings:

- `BAZEL_REMOTE_CACHE_URL`: HTTP URL of the remote cache server (e.g., `https://cache.example.com/bazel-cache`)
- `BAZEL_REMOTE_CACHE_USER`: Username for the remote cache HTTP authentication (optional)
- `BAZEL_REMOTE_CACHE_PASSWORD`: Password for the remote cache HTTP authentication (optional)

## Cache Server Requirements

The remote cache server should:
- Support HTTP PUT/GET operations for Bazel's REST cache protocol
- Support basic HTTP authentication (if using credentials)
- Be accessible from your CI environment

Popular options include:
- [bazel-remote](https://github.com/buchgr/bazel-remote)
- [BuildBuddy](https://www.buildbuddy.io/)
- [Buildfarm](https://github.com/bazelbuild/bazel-buildfarm)

## Benefits

- **Faster CI**: Cache hits eliminate redundant compilation
- **Cross-CI sharing**: Multiple workflows share the same cache
- **Developer productivity**: Local builds can also use the remote cache
- **Cost reduction**: Reduced CI compute time and resources