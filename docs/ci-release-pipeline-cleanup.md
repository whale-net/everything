# CI and Release Pipeline Cleanup

## Overview
This document summarizes the cleanup performed on the CI and release pipelines to eliminate duplication and ensure consistent use of the release tool.

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

## Solution Implemented

### **Unified Approach Using Release Tool**
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

## File Changes

### **Modified Files**
- `.github/workflows/ci.yml` - Simplified Docker job to use release tool

### **Unchanged Files**
- `.github/workflows/release.yml` - Already using release tool correctly
- `tools/release_helper.py` - No changes needed, already well-designed
- `tools/release.bzl` - No changes needed

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
