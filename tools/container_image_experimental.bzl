"""Experimental per-package layering for OCI images.

YOLO MODE: Let's try a radical approach!

Strategy:
Instead of trying to filter runfiles (hard), we'll create "synthetic" binaries
for each package that only depend on that package. Then tar each one separately.

Steps:
1. For each package, create a minimal py_binary that imports just that package
2. Create pkg_tar for each synthetic binary's runfiles
3. Stack all package layers + final app layer

This way each layer truly contains only one package's files!
"""

load("@rules_oci//oci:defs.bzl", "oci_image")
load("@rules_pkg//:pkg.bzl", "pkg_tar")
load("@rules_python//python:defs.bzl", "py_binary")

def _create_package_probe_binary(name, package_name):
    """Create a minimal py_binary that depends on a single package.
    
    This binary exists solely to let Bazel resolve the package's runfiles.
    """
    probe_name = name + "_probe_" + package_name.replace("-", "_").replace(".", "_")
    
    # Create a minimal Python script that imports the package
    script_name = probe_name + "_main"
    native.genrule(
        name = script_name,
        outs = [probe_name + "_main.py"],
        cmd = """
cat > $@ << 'EOF'
# Minimal script to force package resolution
try:
    import {package}
except ImportError:
    pass  # Package may not have importable module with same name
EOF
""".format(package = package_name.replace("-", "_")),
    )
    
    # Create py_binary with this package as only dependency
    # Use @pypi directly since we don't have a requirement function
    py_binary(
        name = probe_name,
        srcs = [":" + script_name],
        main = probe_name + "_main.py",
        deps = ["@pypi//:" + package_name],
        visibility = ["//visibility:private"],
    )
    
    return ":" + probe_name

def container_image_per_package(
    name,
    binary,
    base = "@ubuntu_base",
    env = None,
    entrypoint = None,
    language = None,
    python_version = "3.11",
    packages = None,
    app_layer = None,
    **kwargs):
    """EXPERIMENTAL: Build OCI image with one layer per pip package.
    
    YOLO approach: Create synthetic binaries to isolate each package!
    
    Args:
        name: Target name
        binary: Main py_binary target
        base: Base image
        env: Environment variables
        entrypoint: Custom entrypoint
        language: Must be "python"
        python_version: Python version (default: "3.11")
        packages: List of package names, e.g., ["fastapi", "pydantic"]
        app_layer: py_library with app code only
        **kwargs: Additional args for oci_image
    
    Example:
        container_image_per_package(
            name = "my_app_image",
            binary = ":my_app",
            language = "python",
            packages = ["fastapi", "pydantic", "uvicorn"],
            app_layer = ":app_lib",
        )
    """
    if not language or language != "python":
        fail("Per-package layering only supports Python apps")
    
    if not packages:
        fail("packages parameter required")
    
    binary_name = binary.split(":")[-1] if ":" in binary else binary.split("/")[-1]
    
    all_layers = []
    
    # Create a layer for each package using probe binaries
    for pkg in packages:
        pkg_safe = pkg.replace("-", "_").replace(".", "_")
        
        # Create probe binary that depends only on this package
        probe = _create_package_probe_binary(name, pkg)
        
        # Create tar from probe's runfiles (contains only this package)
        layer_name = name + "_pkg_" + pkg_safe
        pkg_tar(
            name = layer_name,
            srcs = [probe],
            package_dir = "/app",
            include_runfiles = True,
            strip_prefix = ".",
            visibility = ["//visibility:private"],
        )
        all_layers.append(":" + layer_name)
    
    # Add main binary layer (will include Python interpreter + everything)
    # This seems redundant, but ensures we have all transitive deps
    pkg_tar(
        name = name + "_full_deps",
        srcs = [binary],
        package_dir = "/app",
        include_runfiles = True,
        strip_prefix = ".",
        visibility = ["//visibility:private"],
    )
    all_layers.append(":" + name + "_full_deps")
    
    # Application code layer (if specified)
    if app_layer:
        pkg_tar(
            name = name + "_app_layer",
            srcs = [app_layer],
            package_dir = "/app",
            strip_prefix = ".",
            visibility = ["//visibility:private"],
        )
        all_layers.append(":" + name + "_app_layer")
    
    # Default entrypoint
    if not entrypoint:
        python_pattern = "rules_python++python+python_" + python_version.replace(".", "_") + "_*"
        entrypoint = [
            "/bin/sh",
            "-c",
            'exec /app/{}.runfiles/{}/bin/python3 /app/{} "$@"'.format(
                binary_name,
                python_pattern,
                binary_name
            ),
            binary_name,
        ]
    
    # Build final image
    oci_image(
        name = name,
        base = base,
        tars = all_layers,
        entrypoint = entrypoint,
        env = env,
        workdir = "/app",
        **kwargs
    )
