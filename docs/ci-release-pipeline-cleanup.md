# CI and Release Pipeline Consolidation

## Overview
This document summarizes the consolidation performed on the CI and release pipelines to eliminate duplication, ensure consistent use of the release tool, and unify the caching infrastructure.

## Issues Identified

### 1. **Major Code Duplication**
- CI pipeline (`ci.yml`) and release pipeline (`release.yml`) had significant overlap:
  - Setup steps (Bazelisk, Python, Go, caching configurations)
  - Docker image building logic
  - Container registry authentication and tagging

### 2. **Inconsistent Docker Image Handling**
- **CI Pipeline**: Used manual Docker commands with complex tagging logic
- **Release Pipeline**: Properly used the release helper tool (`//tools:release`)
- This created two different code paths for the same functionality

### 3. **Complex Manual Docker Logic**
The CI pipeline had overly complex manual Docker operations:
```yaml
# Complex manual approach (BEFORE)
bazel build --config=ci $(bazel query "kind('oci_load', //...)")
for script in bazel-bin/*/*tarball.sh; do
  # Manual tarball loading...
done
# Manual tagging and pushing with complex name manipulation
```

### 3. **Inconsistent Caching Infrastructure**
- **CI Pipeline**: Used sophisticated multi-layered caching via `bazel-contrib/setup-bazel@0.15.0`
- **Release Pipeline**: Used simpler custom caching via `./.github/actions/setup-bazelisk-cache`
- This created different cache behaviors and performance characteristics between pipelines

## Solution Implemented

### **1. Unified Release Tool Usage**
Updated the CI pipeline to consistently use the release helper tool:

```yaml
# Simplified approach using release tool (AFTER)
APPS=$(bazel run //tools:release -- list)
for app in $APPS; do
  bazel run //tools:release -- build "$app"
done

# For publishing (main branch only)
for app in $APPS; do
  bazel run //tools:release -- release "$app" --version "latest" --commit "${{ github.sha }}"
done
```

### **2. Consolidated Caching Infrastructure**
Updated the release pipeline to use the same multi-layered caching as CI:

```yaml
# Unified caching approach (AFTER)
- name: Setup Bazel with Multi-Layered Caching
  uses: bazel-contrib/setup-bazel@0.15.0
  with:
    bazelisk-cache: true
    repository-cache: true
    disk-cache: "monorepo"
    bazelrc: |
      import %workspace%/.github/.bazelrc.ci
```

### **Key Changes Made**

1. **Replaced Manual Docker Logic**: 
   - Removed complex OCI tarball script handling
   - Replaced with simple `bazel run //tools:release -- build` commands

2. **Centralized Image Publishing**:
   - Removed manual Docker tagging and pushing
   - Uses release tool's built-in registry handling

3. **Consistent App Discovery**:
   - Both pipelines now use `bazel run //tools:release -- list` to discover apps
   - No more hardcoded app lists or complex queries

4. **Simplified PR Builds**:
   - Uses release tool to build images
   - Simpler artifact saving logic

## Benefits

### **1. Reduced Duplication**
- Eliminated ~50 lines of duplicated Docker logic
- Single source of truth for image building and publishing

### **2. Better Maintainability**
- All image operations now go through the release tool
- Changes to image handling only need to be made in one place (`release_helper.py`)

### **3. Consistent Behavior**
- CI and release pipelines now use identical image building logic
- Reduced risk of discrepancies between environments

### **4. Simplified Debugging**
- Easier to debug image issues since there's only one code path
- Release tool provides consistent logging and error handling

### **5. Unified Caching Performance**
- Both pipelines now benefit from the same sophisticated multi-layered caching
- Consistent cache hit rates and build performance across CI and release contexts
- Reduced cache storage duplication between pipeline types

## File Changes

### **Modified Files**
- `.github/workflows/ci.yml` - Simplified Docker job to use release tool
- `.github/workflows/release.yml` - Updated to use same multi-layered caching as CI pipeline

### **Caching Consolidation**
- **Before**: Release pipeline used custom `setup-bazelisk-cache` action with simpler caching
- **After**: Release pipeline uses `bazel-contrib/setup-bazel@0.15.0` with identical multi-layered caching as CI
- **Result**: Both pipelines now use the same sophisticated caching infrastructure for consistency and performance

## Usage Examples

### **Building Images Locally**
```bash
# List all apps
bazel run //tools:release -- list

# Build image for specific app
bazel run //tools:release -- build hello_python

# Build and publish with version
bazel run //tools:release -- release hello_python --version v1.0.0 --commit abc123
```

### **CI Pipeline Behavior**

#### **Pull Request Builds**
- Builds all app images using release tool
- Saves images as artifacts for testing
- No publishing to registry

#### **Main Branch Builds**
- Builds all app images using release tool
- Publishes with `latest` tag and commit SHA tag
- Uses the same release tool logic as formal releases

## Future Improvements

### **Potential Optimizations**
1. **Parallel Builds**: The release tool could support building multiple apps in parallel
2. **Change Detection**: CI could use release tool's change detection to build only affected apps
3. **Caching**: Release tool could implement smarter caching of built images

### **Monitoring**
- Both pipelines now use identical image building logic
- Any issues will surface in both CI and release contexts
- Release tool logs provide consistent debugging information

## Validation

### **Testing Commands**
```bash
# Test locally (matches CI behavior)
bazel run //tools:release -- list
bazel run //tools:release -- build hello_python
bazel run //tools:release -- build hello_go

# Test release dry run
bazel run //tools:release -- release hello_python --version v1.0.0 --dry-run
```

The cleanup ensures that both CI and release pipelines use the sophisticated, battle-tested release tool infrastructure, eliminating technical debt and improving maintainability.
