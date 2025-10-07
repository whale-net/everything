# OCI Image Layering Strategy

## Current Implementation: Two-Layer Architecture

### The Problem

With standard `py_binary` and `pkg_tar`, every code change rebuilds a **277MB tarball** including:
- Python interpreter (hermetic from rules_python)
- All pip dependencies (fastapi, pydantic, uvicorn, etc.)
- Your application code

This makes local development slow: change one line → rebuild 277MB → wait.

### The Solution

**Two-layer approach** with `app_layer` parameter:

```starlark
release_app(
    name = "my_app",
    language = "python",
    app_layer = ":app_lib",  # py_library with ONLY app code
)
```

**Result:**
- **Layer 1 (dependencies)**: 277MB - Python + all packages - Rarely changes
- **Layer 2 (app code)**: ~10KB - Just your .py files - Changes frequently

When you change app code:
- Layer 1: Docker cache hit ✅
- Layer 2: Rebuild in ~2 seconds ✅

## Why Not Per-Package Layers?

The obvious next step: **one layer per pip package**. This would be ideal for caching:

```
Layer 1: Python interpreter (50MB)
Layer 2: pydantic-core (150MB)
Layer 3: fastapi (5MB)
Layer 4: uvicorn (2MB)
...
Layer N: Your app code (10KB)
```

### Benefits
- **Maximum cache efficiency**: Change fastapi version → only rebuild fastapi layer
- **Semantic organization**: Each layer is a logical unit
- **Dependency ordering**: Stable packages (typing-extensions) at bottom

### Why We Don't Do It (Yet)

#### 1. Build Time Trade-off
Creating 20+ separate tarballs might be **slower** than creating 2:
- Each `pkg_tar` action has overhead
- More actions = more scheduling overhead
- Bazel would need to run 20+ tar commands instead of 2

**Need benchmarking**: Does 20 small tars beat 2 large tars?

#### 2. Implementation Complexity
Requires **dynamic package discovery**:
```starlark
# Need to programmatically find all packages in runfiles
packages = discover_pip_packages(binary.runfiles)  # How?

# Create a layer for each
for pkg in packages:
    pkg_tar(
        name = pkg + "_layer",
        srcs = select_package_files(binary, pkg),  # How?
    )
```

Challenges:
- Runfiles structure is complex (symlinks, manifests)
- No built-in Bazel rules to query runfiles by package
- Would need custom Starlark or Python script to parse runfiles

#### 3. Layer Count Limits
- Docker has a **practical limit of ~127 layers**
- Large projects could hit this (though unlikely)
- More layers = more manifest complexity

#### 4. Maintenance Burden
- More layers = more moving parts
- Harder to debug "why is this file in the wrong layer?"
- Adds conceptual complexity for developers

## Future Direction

If per-package layering proves valuable, here's how to implement it:

### Step 1: Package Discovery Tool
Create `tools/discover_packages.py`:
```python
def discover_pip_packages(runfiles_manifest: Path) -> List[Package]:
    """Parse runfiles manifest and extract pip package info."""
    packages = []
    for line in runfiles_manifest.read_text().splitlines():
        if "rules_pycross++lock_repos+pypi/_lock/" in line:
            pkg_name = extract_package_name(line)
            packages.append(Package(name=pkg_name, files=[...]))
    return packages
```

### Step 2: Dynamic Layer Generation
```starlark
def _generate_package_layers(ctx, binary):
    """Generate one pkg_tar per pip package."""
    # Run discovery tool
    packages = ctx.actions.run(
        executable = ctx.executable._discover_packages,
        arguments = [binary.runfiles_manifest],
    )
    
    # Create layers
    layers = []
    for pkg in packages:
        layer = pkg_tar(
            name = pkg.name + "_layer",
            srcs = pkg.files,
        )
        layers.append(layer)
    
    return layers
```

### Step 3: Benchmark
Compare build times:
- **Current**: 2 layers, ~2 seconds per rebuild
- **Per-package**: 20+ layers, ??? seconds per rebuild

If the per-package approach is **faster or comparable**, adopt it!

## Decision Log

**2025-10-07**: Implemented two-layer approach
- Reason: Dramatic improvement over single-layer (2s vs 2s but with Docker caching)
- Sweet spot between simplicity and performance
- Can iterate to per-package later if needed

**Future**: Per-package layers
- Needs: Implementation + benchmarking
- Waiting for: Real-world feedback on whether 2-layer is sufficient
- If you need this: Open an issue with your use case!

## References

- **OCI Image Spec**: https://github.com/opencontainers/image-spec
- **rules_oci examples**: https://github.com/bazel-contrib/rules_oci/tree/main/examples
- **Docker layer best practices**: https://docs.docker.com/build/cache/
