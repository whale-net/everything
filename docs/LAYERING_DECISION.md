# OCI Image Layering Strategy - Final Decision

## Executive Summary

**Decision: Adopt 2-layer approach as default, document per-package as advanced pattern**

The 2-layer architecture (dependencies + app code) provides the optimal balance of:
- Simple implementation
- Fast incremental builds (~1.4s average)
- Low tar overhead
- Good-enough caching for most use cases

Per-package layering remains a valid pattern for specialized scenarios but adds complexity without meaningful benefit for typical applications.

## Performance Measurements

### Current Implementation (2-layer)

Benchmark results from `tools/benchmark_layering.py`:

```
Two-layer approach:
  Clean build: 5.22s
  Average incremental: 1.39s
  Range: 1.20s - 1.81s
```

**Layer breakdown:**
- Dependencies layer: 277MB (all pip packages + interpreter)
- App layer: ~10KB (application code only)

**Cache behavior:**
- Changing app code → only app layer rebuilds (10KB)
- Changing dependencies (uv.lock) → deps layer rebuilds (277MB)

### Per-Package Analysis

From `tools/analyze_layer_overhead.py`:

**Package distribution (hello_fastapi):**
- 19 pip packages
- Sizes range from 56K (annotated-types) to 4.5M (pydantic-core)
- Total: ~277MB

**Estimated overhead:**
- Current: 2 tar operations
- Per-package: 21 tar operations (interpreter + 19 packages + app)
- Additional overhead: ~0.26s (with parallelization factor of 4)

## Decision Matrix

| Scenario | 2-Layer | Per-Package | Recommendation |
|----------|---------|-------------|----------------|
| **Local development** | ✅ Excellent | ⚠️ Overkill | Use 2-layer |
| **Stable dependencies** | ✅ Perfect fit | ❌ Unnecessary | Use 2-layer |
| **Frequent dep updates** | ⚠️ Good | ✅ Better | Consider per-package |
| **Large monorepo (50+ pkgs)** | ⚠️ Acceptable | ✅ Beneficial | Consider per-package |
| **CI/CD optimization** | ✅ Good | ✅ Better | Either works |

## When to Use Each Approach

### Use 2-Layer (Default)
- **Application development**: Code changes more frequent than dependency changes
- **Small to medium projects**: <50 packages
- **Stable dependencies**: uv.lock changes infrequently
- **Simplicity matters**: Easier to understand and maintain

### Use Per-Package (Advanced)
- **Dependency churn**: uv.lock changes multiple times per day
- **Large monorepo**: 50+ packages with selective updates
- **Shared infrastructure**: Multiple apps sharing common base layers
- **CI optimization**: Build time is critical and cache hit rate matters

## Implementation Guidance

### 2-Layer (Current Implementation)

```python
# In your app's BUILD.bazel
py_library(
    name = "app_lib",
    srcs = glob(["*.py"]),
    deps = ["@pypi//:fastapi"],
)

py_binary(
    name = "my_app",
    deps = [":app_lib"],
)

release_app(
    name = "my_app",
    language = "python",
    app_layer = ":app_lib",  # Enable 2-layer caching
)
```

### Per-Package (Experimental)

**Status**: Not implemented (requires significant Bazel rule development)

**Requirements:**
1. Custom Bazel rule (not macro) to dynamically discover packages
2. Integration with rules_pycross to extract package metadata
3. Dynamic tar layer generation (one per package)
4. Platform transition handling for cross-compilation

**Complexity estimate**: 2-3 days of development + testing

**See:** `tools/container_image_experimental.bzl` for design notes

## Benchmark Methodology

### Tools Created
1. **`tools/benchmark_layering.py`**: Measures build times
   - Clean build timing
   - Incremental build timing (5 iterations)
   - Automated app code modification

2. **`tools/discover_packages.py`**: Extracts package metadata
   - Scans runfiles structure
   - Identifies pip packages
   - Reports package count and structure

3. **`tools/analyze_layer_overhead.py`**: Overhead analysis
   - Calculates package sizes
   - Estimates tar operation costs
   - Provides recommendations

### Running Benchmarks

```bash
# Benchmark current approach
python3 tools/benchmark_layering.py

# Analyze overhead
python3 tools/analyze_layer_overhead.py

# Discover packages in an app
python3 tools/discover_packages.py bazel-bin/demo/hello_fastapi/hello_fastapi.runfiles
```

## Cost-Benefit Analysis

### 2-Layer Approach

**Benefits:**
- ✅ Simple to implement (already done)
- ✅ Fast incremental builds (1.4s)
- ✅ Low overhead (2 tar ops)
- ✅ Easy to understand
- ✅ Works for 90% of use cases

**Costs:**
- ⚠️ Dependency changes rebuild entire 277MB layer
- ⚠️ No per-package cache granularity

**Net value**: Very positive for typical development workflows

### Per-Package Approach

**Benefits:**
- ✅ Granular caching (per-package)
- ✅ Better for dependency updates
- ✅ Optimal for large monorepos

**Costs:**
- ❌ Complex implementation (2-3 days work)
- ❌ Higher tar overhead (~0.26s)
- ❌ More layers in final image (21 vs 2)
- ❌ Harder to understand and debug

**Net value**: Marginal for most projects, beneficial only for specialized scenarios

## Conclusion

The 2-layer approach hits the sweet spot:
- **Significant improvement** over single-layer (277MB → 10KB rebuilds)
- **Simple implementation** that's already working
- **Fast enough** for local development (1.4s incremental)
- **Good enough** for CI/CD caching

Per-package layering provides diminishing returns:
- Only helps when dependencies change (rare in typical dev)
- Adds 0.26s overhead to every build
- Requires 2-3 days of development work
- Increases system complexity significantly

**Recommendation**: Ship 2-layer as default, revisit per-package only if:
1. We have concrete evidence of dependency churn causing problems
2. Multiple teams report slow builds due to uv.lock changes
3. We expand to 50+ packages where granularity matters

## References

- Initial investigation: `docs/OCI_LAYERING_STRATEGY.md`
- Implementation: `tools/container_image.bzl`
- Agent instructions: `AGENTS.md` (Performance Optimization section)
- Benchmarking tools: `tools/benchmark_layering.py`, `tools/analyze_layer_overhead.py`
