"""Experimental per-package layering for OCI images.

This provides an alternative to container_image() that creates one layer
per pip package for maximum cache efficiency.
"""

load("@rules_oci//oci:defs.bzl", "oci_image")
load("@rules_pkg//:pkg.bzl", "pkg_tar")

def container_image_per_package(
    name,
    binary,
    base = "@ubuntu_base",
    env = None,
    entrypoint = None,
    language = None,
    python_version = "3.11",
    app_layer = None,
    **kwargs):
    """EXPERIMENTAL: Build OCI image with one layer per pip package.
    
    This is an experiment to see if per-package layering is faster than
    the two-layer approach. Creates:
    
    - Layer 0: Python interpreter (~50MB)
    - Layer 1-N: One layer per pip package (~5-150MB each)
    - Layer N+1: Application code (~10KB)
    
    Trade-offs vs two-layer approach:
    - Pro: Maximum cache granularity (change one package = rebuild one layer)
    - Con: More tar creation overhead (20 tars vs 2 tars)
    - Con: More complex build graph
    - Unknown: Net performance impact (needs benchmarking!)
    
    Args:
        Same as container_image()
    """
    if not language:
        fail("language parameter is required")
    
    if language != "python":
        fail("Per-package layering only supports Python apps currently")
    
    binary_name = binary.split(":")[-1] if ":" in binary else binary.split("/")[-1]
    
    # For now, fall back to regular implementation
    # Full implementation would:
    # 1. Run discover_packages.py to get package list
    # 2. Create pkg_tar for Python interpreter
    # 3. Create pkg_tar for each package
    # 4. Create pkg_tar for app code
    # 5. Pass all layers to oci_image in order
    
    # This requires genrule or ctx.actions.run support which is complex
    # Would need a full rule implementation, not a macro
    fail("Per-package layering not yet fully implemented - use app_layer instead")
