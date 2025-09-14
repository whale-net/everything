# CI/CD and Go Cache Fixes

## Problem
The CI/CD pipeline was experiencing Go cache warnings during post-job cleanup:
```
Warning: Cache folder path is retrieved but doesn't exist on disk: /home/runner/go/pkg/mod
Primary key was not generated. Please check the log messages above for more errors or information
```

## Solution

### 1. **Converted Shell Script to Bazel Tool**
- **Before**: `scripts/init-go-cache.sh` - standalone shell script
- **After**: Bazel rules in `tools/go.bzl` with proper integration

#### New Bazel Tools:
- `bazel run //:init-go-cache` - Initializes Go cache directories
- `bazel run //:go-env-info` - Shows Go environment information

### 2. **Fixed CI Workflow** (`.github/workflows/ci.yml`)

#### Added Proper Go Caching:
```yaml
- name: Setup Go module cache
  uses: actions/cache@v4
  with:
    path: |
      ~/go/pkg/mod
      ~/.cache/go-build
    key: ${{ runner.os }}-go-${{ hashFiles('go.mod', 'go.sum') }}
    restore-keys: |
      ${{ runner.os }}-go-
```

#### Fixed Docker Build Process:
- **Before**: Complex query with `--output=files` and complex path logic
- **After**: Simple glob pattern `bazel-bin/*/*tarball.sh`

```yaml
- name: Build and load Docker images with Bazel
  run: |
    echo "Building OCI images..."
    bazel build --config=ci $(bazel query "kind('oci_load', //...)")
    
    echo "Loading images into Docker daemon..."
    for script in bazel-bin/*/*tarball.sh; do
      if [[ -x "$script" ]]; then
        echo "Loading image from $script"
        "$script"
      fi
    done
    
    echo "Loaded Docker images:"
    docker images
```

### 3. **Enhanced Build Process**
- All three CI jobs (build, test, docker) now have proper Go cache setup
- Added debugging output to show generated tarball scripts
- Added verification that Docker images are loaded successfully

### 4. **Testing**
Created `scripts/test-ci-local.sh` to verify the build process works locally:
```bash
./scripts/test-ci-local.sh
```

## Benefits
1. **No More Go Cache Warnings**: Cache directories are created before any Go operations
2. **Better Performance**: Proper Go module caching in CI
3. **More Reliable**: Simplified Docker image loading process
4. **Bazel Integration**: Uses the same build system consistently
5. **Cross-platform**: Works in both local development and CI environments

## Files Changed
- `.github/workflows/ci.yml` - Fixed CI/CD pipeline
- `tools/go.bzl` - New Bazel rules for Go utilities
- `tools/BUILD.bazel` - Added Go tool targets
- `BUILD.bazel` - Added convenient aliases
- `tools/README.md` - Documentation for tools
- `scripts/test-ci-local.sh` - Local testing script
- Removed: `scripts/init-go-cache.sh` (converted to Bazel tool)

## Usage
- **Local development**: `bazel run //:init-go-cache`
- **CI/CD**: Automatically runs during GitHub Actions
- **Debugging**: `bazel run //:go-env-info`
- **Testing**: `./scripts/test-ci-local.sh`
