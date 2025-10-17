# Python Runtime Optimization

## Summary

We optimize Python container images from ~400MB to ~87MB (77% reduction) using a genrule that strips debug symbols and removes unnecessary files.

## Why Not a Custom Toolchain?

The "pure" Bazel approach would be registering a custom stripped Python toolchain. We don't do this because:

- **Complex**: Requires maintaining custom Python builds for each platform
- **Brittle**: Breaks rules_python's automatic toolchain resolution  
- **Incompatible**: Would need custom integration with rules_pycross
- **Effort**: 2-3 weeks vs 2 hours for current approach

## Current Approach: Genrule Optimization

We optimize in `container_image.bzl` as a build step:

```starlark
genrule(
    name = name + "_split_layers",
    tools = ["//tools/scripts:strip_python.sh"],
    cmd = "strip and split Python runtime...",
)
```

### Is This Hermetic?

**Yes, with caveats:**
- ✅ Deterministic (same inputs → same outputs)
- ✅ Cached correctly by Bazel
- ✅ Reproducible across machines
- ⚠️ Post-processes toolchain output (but deterministically)

### Trade-offs

| Aspect | Value |
|--------|-------|
| Size reduction | 77% (400MB → 87MB) |
| Maintenance | Two bash scripts |
| Local dev | Full runtime with debug symbols |
| Production | Stripped runtime |
| Hermetic | Good enough |

## What Gets Optimized

1. Strip binaries: `strip --strip-all` (108MB → 26MB)
2. Strip libraries: `strip --strip-unneeded` (235MB → 29MB)
3. Remove: TCL/TK, headers, tests, cache files (~15MB)

## Conclusion

The genrule approach is pragmatic: significant benefits, low complexity, good-enough hermetic properties.
