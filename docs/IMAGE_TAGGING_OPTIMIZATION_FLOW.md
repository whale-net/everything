# Image Tagging Optimization Flow

## High-Level Flow

```
┌─────────────────────────────────────────────────────────────────┐
│                     Release Request                              │
│            (app, version, commit_sha)                           │
└─────────────────────┬───────────────────────────────────────────┘
                      │
                      ▼
            ┌─────────────────────┐
            │ Commit SHA provided? │
            └──────────┬──────────┘
                       │
           ┌───────────┴───────────┐
           │                       │
          YES                     NO
           │                       │
           ▼                       ▼
┌──────────────────────┐   ┌──────────────────────┐
│ Check if commit      │   │ Always build         │
│ image exists         │   │ and push             │
└──────────┬───────────┘   └──────────────────────┘
           │
           ▼
    ┌──────────────┐
    │ Image exists?│
    └──────┬───────┘
           │
    ┌──────┴──────┐
    │             │
   YES           NO
    │             │
    ▼             ▼
┌─────────────┐ ┌─────────────┐
│ Re-tag      │ │ Build and   │
│ existing    │ │ push        │
│ image       │ │             │
└─────┬───────┘ └─────────────┘
      │
      │ (if fails)
      └─────────┐
                │
                ▼
        ┌───────────────┐
        │ Fallback:     │
        │ Build and push│
        └───────────────┘
```

## Detailed Flow with Examples

### Scenario 1: First Release (No Optimization)

```
User: Release hello_python v1.0.0 (commit: abc123)
  │
  ├─ Check: Does ghcr.io/owner/demo-hello_python:abc123 exist?
  │   └─ NO ❌
  │
  ├─ Action: Build image
  │   ├─ Build platform-specific images (amd64, arm64)
  │   └─ Create OCI image index
  │
  └─ Action: Push with all tags
      ├─ ghcr.io/owner/demo-hello_python:abc123
      ├─ ghcr.io/owner/demo-hello_python:v1.0.0
      └─ ghcr.io/owner/demo-hello_python:latest

⏱️  Duration: ~5-10 minutes (full build)
```

### Scenario 2: Re-release Same Commit (Optimized)

```
User: Release hello_python v1.0.1 (commit: abc123)
  │
  ├─ Check: Does ghcr.io/owner/demo-hello_python:abc123 exist?
  │   └─ YES ✅
  │
  └─ Action: Re-tag existing image
      ├─ Source: ghcr.io/owner/demo-hello_python:abc123
      ├─ Target: ghcr.io/owner/demo-hello_python:v1.0.1
      └─ Target: ghcr.io/owner/demo-hello_python:latest

⏱️  Duration: ~5-10 seconds (re-tag only)
💰 Savings: 99% faster!
```

### Scenario 3: Optimization with Fallback

```
User: Release hello_python v1.0.2 (commit: abc123)
  │
  ├─ Check: Does ghcr.io/owner/demo-hello_python:abc123 exist?
  │   └─ YES ✅
  │
  ├─ Action: Attempt re-tag
  │   └─ FAILED ❌ (network error)
  │
  └─ Fallback: Build and push
      ├─ Build image
      └─ Push with all tags

⏱️  Duration: ~5-10 minutes (full build)
🛡️  Reliability: Always succeeds
```

## Technical Implementation

### Check Image Exists

```python
def check_image_exists_in_registry(image_ref: str) -> bool:
    """Uses 'docker manifest inspect' to check without downloading."""
    result = subprocess.run(
        ["docker", "manifest", "inspect", image_ref],
        capture_output=True,
        check=False
    )
    return result.returncode == 0
```

### Re-tag Existing Image

```python
def tag_existing_image(source_tag: str, target_tags: List[str]) -> None:
    """Uses 'docker buildx imagetools' to create new manifest references."""
    for target_tag in target_tags:
        subprocess.run(
            ["docker", "buildx", "imagetools", "create", 
             "--tag", target_tag, source_tag],
            check=True
        )
```

### Optimization Decision Logic

```python
# Check if we can optimize
should_rebuild = True
if commit_sha:
    commit_tag_ref = tags.get("commit")
    if commit_tag_ref and check_image_exists_in_registry(commit_tag_ref):
        print("✅ Found existing image - will re-tag")
        should_rebuild = False

# Execute based on decision
if should_rebuild:
    build_image(bazel_target)
    push_image_with_tags(bazel_target, all_tags)
else:
    try:
        tag_existing_image(commit_tag_ref, version_and_latest_tags)
    except Exception:
        # Fallback to rebuild
        build_image(bazel_target)
        push_image_with_tags(bazel_target, all_tags)
```

## Cost Savings Example

### Without Optimization (10 releases of same commit)
```
Release 1: Build (10 min) + Push (1 min) = 11 min
Release 2: Build (10 min) + Push (1 min) = 11 min
Release 3: Build (10 min) + Push (1 min) = 11 min
...
Release 10: Build (10 min) + Push (1 min) = 11 min

Total: 110 minutes
```

### With Optimization (10 releases of same commit)
```
Release 1: Build (10 min) + Push (1 min) = 11 min  (first time)
Release 2: Re-tag (10 sec) = 0.17 min              (optimized)
Release 3: Re-tag (10 sec) = 0.17 min              (optimized)
...
Release 10: Re-tag (10 sec) = 0.17 min             (optimized)

Total: ~12.5 minutes
💰 Savings: 88.6% time reduction!
```

## Testing Strategy

### Unit Tests
- ✅ `test_check_image_exists_in_registry` - Verifies image existence checking
- ✅ `test_tag_existing_image_success` - Verifies successful re-tagging
- ✅ `test_tag_existing_image_fallback` - Verifies fallback when buildx unavailable
- ✅ `test_tag_and_push_image_reuses_existing_commit_image` - End-to-end optimization
- ✅ `test_tag_and_push_image_builds_when_commit_image_missing` - Fallback to build
- ✅ `test_tag_and_push_image_fallback_on_tagging_failure` - Error handling

### Integration Tests
The optimization is transparent to existing workflows and requires no changes:
- GitHub Actions workflows automatically pass `--commit ${{ github.sha }}`
- The release helper handles optimization automatically
- All existing release commands work unchanged

## Monitoring

### Success Indicators
Look for these log messages in release workflows:

**Optimization Active:**
```
✅ Found existing image for commit abc123d: ghcr.io/owner/demo-hello_python:abc123def
Optimizing: Re-tagging existing image instead of rebuilding
✅ Tagged with v1.0.0
✅ Tagged with latest
Successfully tagged hello_python v1.0.0 from existing commit image
```

**Normal Build (First Release):**
```
No existing image found for commit abc123d, will build
Building OCI image index with platform transitions...
✅ Built OCI image index containing 2 platform variants
Pushing OCI image index with 3 tags...
Successfully pushed hello_python v1.0.0
```

**Fallback (Error Recovery):**
```
✅ Found existing image for commit abc123d
Optimizing: Re-tagging existing image instead of rebuilding
Failed to tag existing image, falling back to rebuild: <error details>
Rebuilding image...
Successfully pushed hello_python v1.0.0 (after fallback)
```
