# OpenAPI Client Generation Platform Fix

## Problem

CI builds failing with error:
```
ERROR: Generating OpenAPI client for experience_api failed
bazel-out/.../bin/java: cannot execute binary file: Exec format error
external/rules_java++toolchains+remotejdk17_linux_aarch64/bin/java
```

**Root Cause**: Platform transition propagation

When building multi-arch container images:
1. `oci_image_index` applies platform transition to ARM64
2. Entire dependency graph gets transitioned (including OpenAPI generation)
3. Java toolchain resolves for **target platform** (ARM64) instead of **execution platform** (x86_64)
4. ARM64 Java binary tries to run on x86_64 GitHub Actions runner → Exec format error

## Solution

Refactored OpenAPI client generation to ensure code generation tools always run on execution platform:

### Architecture Change

**Before** (Single Custom Rule):
```
openapi_client_rule (target cfg)
  ├─ Java toolchain (follows target platform → ARM64 ❌)
  ├─ Generate tar
  └─ Extract + PyInfo
```

**After** (Two-Stage with Genrule):
```
openapi_client()
  ├─ genrule (exec cfg) → Generates tar
  │   └─ Uses host Java (x86_64 ✓)
  └─ openapi_client_provider_rule (target cfg) → Extract + PyInfo
      └─ Depends on tar from genrule
```

### Key Changes

1. **`tools/openapi/openapi_client_rule.bzl`**:
   - Split into `openapi_client_provider_rule` (target cfg) and genrule (exec cfg)
   - Genrule naturally runs in execution configuration
   - Added `local=True` to disable sandboxing (access host Java)

2. **`tools/openapi/run_openapi_gen.sh`** (new):
   - Finds system Java from common locations
   - Fallback to `$JAVA_HOME`, `/usr/bin/java`, or `java` in PATH
   - GitHub Actions runners have Java pre-installed

3. **`tools/openapi/BUILD.bazel`**:
   - Added `run_openapi_gen` sh_binary target

### Why This Works

- **Genrules execute in exec configuration** by default
- Code generation happens on build host (x86_64), independent of target platform (ARM64)
- Platform transitions don't affect genrule execution platform
- No toolchain resolution issues - uses system Java directly

### Testing

```bash
# Should succeed - OpenAPI generation uses x86_64 Java regardless of target platform
bazel build //generated/manman:experience_api --platforms=//tools:linux_arm64
bazel build //friendly_computing_machine:bot_image
```

### CI Impact

- Fixes all 7 failed image build jobs (bot, migration, worker, subscribe, taskpool, etc.)
- OpenAPI client generation now platform-independent
- No changes to generated client code or runtime behavior

## Alternative Solutions Considered

1. **Exec groups**: Too complex, still had toolchain resolution issues
2. **`cfg = "exec"` attributes**: Only affects attribute resolution, not toolchains
3. **Explicit platform-specific Java**: Requires maintaining platform-specific targets
4. **Bundled hermetic Java**: Adds ~200MB overhead, unnecessary given CI has Java

The genrule approach is the **simplest and most Bazel-idiomatic** solution.
