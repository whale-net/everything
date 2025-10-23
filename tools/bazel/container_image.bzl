"""Simplified OCI container image rules with multiplatform support.

ARCHITECTURE: Single Binary → Platform Transitions → Single Push
==================================================================

1. SINGLE BINARY TARGET
   - One py_binary or go_binary (no platform-specific variants)
   - Cross-compilation handled by Bazel + rules_pycross
   - Built for different platforms using --platforms flag

2. OCI IMAGE INDEX with Platform Transitions
   - Single {name} target that builds for all platforms via Bazel transitions
   - oci_image_index automatically builds base image for each platform
   - Creates proper OCI index manifest with platform metadata
   - Used by oci_push to push multi-arch images

3. LOAD TARGET (Local Testing Only)
   - {name}_load: Load image to local Docker
   - Must specify --platforms flag (e.g., --platforms=//tools:linux_arm64)
   - Used only for local development/testing

4. PUSH TARGET (Production Release)
   - {name}_push: Pushes OCI image index (contains ALL platforms)
   - Single manifest automatically serves correct architecture
   - Users pull ONE tag that works on any platform
   - Release system ONLY uses this target

LAYER CACHING STRATEGY:
=======================
Python images use 4 separate layers with independent caching:

1. CA Certs Layer (//tools/cacerts:cacerts)
   - Bazel caches based on: Ubuntu ca-certificates package version
   - Invalidated when: Base certificate package is updated
   - Shared across: ALL apps via content-addressable storage
   
2. Python Interpreter Layer ({name}_python_layer)
   - Bazel caches based on: Python toolchain version (globally configured)
   - Invalidated when: Python version changes (e.g., 3.13.0 -> 3.13.1)
   - Location: /opt/python3.13/{arch}/ (universal across all apps)
   - Size: ~240-375MB (stripped)
   - **Universal Layer**: All apps produce IDENTICAL Python layer tars because
     Python is placed at /opt/python3.13/{arch}/ regardless of app name.
   - **Perfect Deduplication**: Content-addressable storage means this layer
     is built once and reused for all Python apps (same SHA256).
   - **Storage Optimization**: With --remote_download_minimal, even this
     universal layer isn't downloaded during CI - only metadata is fetched.
   
3. Dependencies Layer ({name}_deps_layer)
   - Bazel caches based on: uv.lock + resolved wheel hashes for target platform
   - Invalidated when: Dependencies added/updated in uv.lock
   - Shared across: Apps with identical dependencies (content-addressable)
   - Size: varies by app (10KB-100MB)
   
4. App Code Layer ({name}_app_layer)
   - Bazel caches based on: Source files in _main workspace + local libs
   - Invalidated when: App code or local library code changes
   - Unique per: Each app
   - Size: typically small (100KB-10MB)
   - **Includes Symlink**: Contains symlink from /app/{binary}.runfiles/rules_python++...
     to /opt/python3.13/{arch}/ so Python stub scripts find the interpreter

Each layer is a separate Bazel target, enabling:
- Independent action caching per layer
- Parallel layer building
- Minimal rebuilds (only changed layers)
- Efficient remote cache usage
- **Perfect Python runtime sharing**: All Python apps produce IDENTICAL
  Python layer tars (universal /opt/python3.13/{arch}/ location)

**Universal Python Architecture:**
The Python interpreter is placed at /opt/python3.13/{arch}/ in a dedicated layer.
Each app's layer contains a symlink from its expected runfiles location
(/app/{binary}.runfiles/rules_python++python+python_3_13_{arch}) to the universal
location. This achieves true layer sharing while maintaining compatibility with
Python's stub script expectations.

This is the idiomatic Bazel way to build multiplatform images.
"""

load("@rules_oci//oci:defs.bzl", "oci_image", "oci_image_index", "oci_load", "oci_push")
load("@rules_pkg//:pkg.bzl", "pkg_tar")

def _get_binary_name(binary):
    """Extract binary name from label."""
    if ":" in binary:
        return binary.split(":")[-1]
    return binary.split("/")[-1]

def _get_binary_path(binary):
    """Extract full binary path from label (including package path)."""
    # For //friendly_computing_machine/src:fcm_cli -> returns src/fcm_cli
    # For //demo/hello:hello -> returns hello
    # For //manman/src/worker:worker -> returns src/worker/worker
    # For :hello -> returns hello (relative label)
    if ":" in binary:
        binary_name = binary.split(":")[-1]
        if "//" in binary:
            package_path = binary.split("//")[1].split(":")[0]
            parts = package_path.split("/")
            
            # Only simplify if the package path is a single segment matching the binary name
            # //demo/hello:hello -> hello (not hello/hello)
            # But //manman/src/worker:worker -> src/worker/worker (has parent path)
            if len(parts) == 1 and parts[0] == binary_name:
                return binary_name
            
            # For multi-segment paths, include everything after the first segment
            # //friendly_computing_machine/src:fcm_cli -> src/fcm_cli
            # //manman/src/worker:worker -> src/worker/worker
            if "/" in package_path:
                return package_path.split("/", 1)[-1] + "/" + binary_name
            else:
                return binary_name
        else:
            # :hello -> hello (relative label in current package)
            return binary_name
    # //demo/hello -> hello
    return binary.split("/")[-1]

def container_image(
    name,
    binary,
    base = "@ubuntu_base",
    env = None,
    entrypoint = None,
    language = None,
    python_version = "3.13",
    **kwargs):
    """Build a single-platform OCI container image.
    
    Simple, clean wrapper around oci_image that uses hermetic toolchains.
    The same binary target will be built for different platforms when invoked
    with different --platforms flags.
    
    LAYERING STRATEGY - Optimized for Cache Efficiency:
    ====================================================
    Uses 4 separate layers for optimal caching:
    1. CA certificates (//tools/cacerts:cacerts) - shared across ALL apps
    2. Python interpreter (/opt/python3.13/{arch}/) - universal location, shared across ALL Python apps
    3. Third-party dependencies (rules_pycross pypi packages) - per-app or shared if deps match
    4. Application code (_main/ workspace) - unique per app, includes symlink to universal Python
    
    This layering strategy ensures:
    - Base layers (certs, interpreter) are cached and shared across ALL Python apps
    - Dependencies layer is shared when apps have identical deps
    - Only app code layer is rebuilt during typical development
    - Registry pushes only upload changed layers (reduces bandwidth)
    - Container pulls only download changed layers (faster deployments)
    - Maximum cache efficiency: all Python apps produce identical Python layer (same content hash)
    
    Args:
        name: Image name
        binary: Binary target to containerize (single target, built for current platform)
        base: Base image (defaults to ubuntu:24.04)
        env: Environment variables dict
        entrypoint: Override entrypoint (auto-detected from language)
        language: Language of the binary ("python" or "go") - REQUIRED
        python_version: Python version for path construction (default: "3.13")
        **kwargs: Additional oci_image arguments
    """
    if not language:
        fail("language parameter is required for container_image")
    
    # For Python apps, create optimized layers
    if language == "python":
        # First, create full runfiles tar to extract from
        pkg_tar(
            name = name + "_full_runfiles",
            srcs = [binary],
            package_dir = "/app",
            include_runfiles = True,
            strip_prefix = ".",
            tags = ["manual"],
        )
        
        # Extract and layer Python interpreter to universal location
        # Places Python at /opt/python3.13/<arch>/ for sharing across all apps
        native.genrule(
            name = name + "_python_layer",
            srcs = [":" + name + "_full_runfiles"],
            outs = [name + "_python_layer.tar"],
            tools = ["//tools/scripts:strip_python.sh"],
            cmd = """
                set -e
                trap 'rm -rf layer_tmp python_layer' EXIT
                
                # Extract full runfiles
                mkdir -p layer_tmp
                tar -xf $(location :{name}_full_runfiles) -C layer_tmp
                
                # Find Python interpreter directory
                cd layer_tmp
                PYTHON_DIR=$$(find app -path "*/rules_python++python+python_3_*" -type d -print -quit)
                
                if [ -z "$$PYTHON_DIR" ]; then
                    echo "Error: Python directory not found"
                    exit 1
                fi
                
                # Extract architecture from path (e.g., x86_64-unknown-linux-gnu)
                ARCH=$$(basename "$$PYTHON_DIR" | sed 's/.*python_3_13_//')
                
                if [ -z "$$ARCH" ] || [ "$$ARCH" = "$$(basename "$$PYTHON_DIR")" ]; then
                    echo "Error: Failed to extract architecture from $$PYTHON_DIR"
                    exit 1
                fi
                
                cd ..
                
                # Create universal Python location: /opt/python3.13/<arch>/
                mkdir -p python_layer/opt/python3.13/$$ARCH
                
                # Copy Python to universal location
                cp -r layer_tmp/$$PYTHON_DIR/* python_layer/opt/python3.13/$$ARCH/
                
                # Strip the Python installation (failures are non-fatal for optimization)
                $(location //tools/scripts:strip_python.sh) python_layer/opt/python3.13/$$ARCH 2>&1 | head -20 || echo "Warning: strip_python.sh encountered issues but continuing"
                
                # Create final tar with universal path
                tar -cf $@ -C python_layer .
                """.format(name = name),
            tags = ["manual"],
        )
        
        # Extract and layer third-party dependencies
        native.genrule(
            name = name + "_deps_layer",
            srcs = [":" + name + "_full_runfiles"],
            outs = [name + "_deps_layer.tar"],
            tools = ["//tools/scripts:strip_python.sh"],
            cmd = """
                set -e
                trap 'rm -rf layer_tmp deps_layer deps_tmp.tar' EXIT
                
                # Extract full runfiles
                mkdir -p layer_tmp
                tar -xf $(location :{name}_full_runfiles) -C layer_tmp
                
                # Find and archive deps - handle empty case gracefully
                cd layer_tmp
                DEPS_PATHS=$$(find app -path "*/rules_pycross++lock_repos+pypi*" || true)
                if [ -n "$$DEPS_PATHS" ]; then
                    echo "$$DEPS_PATHS" | tar -cf ../deps_tmp.tar -T -
                else
                    # No dependencies - create empty tar
                    tar -cf ../deps_tmp.tar -T /dev/null
                fi
                cd ..
                
                # Extract to temp directory and strip if not empty
                mkdir -p deps_layer
                if [ -s deps_tmp.tar ]; then
                    tar -xf deps_tmp.tar -C deps_layer
                    # Strip (failures are non-fatal for optimization)
                    $(location //tools/scripts:strip_python.sh) deps_layer 2>&1 | head -20 || echo "Warning: strip_python.sh encountered issues but continuing"
                fi
                
                # Create final tar
                tar -cf $@ -C deps_layer .
                """.format(name = name),
            tags = ["manual"],
        )
        
        # Extract app code and create symlink to universal Python location
        native.genrule(
            name = name + "_app_layer",
            srcs = [":" + name + "_full_runfiles"],
            outs = [name + "_app_layer.tar"],
            cmd = """
                set -e
                trap 'rm -rf layer_tmp app_layer app_tmp.tar' EXIT
                
                # Extract full runfiles
                mkdir -p layer_tmp
                tar -xf $(location :{name}_full_runfiles) -C layer_tmp
                
                # Find Python interpreter directory to determine architecture
                cd layer_tmp
                PYTHON_DIR=$$(find app -path "*/rules_python++python+python_3_*" -type d -print -quit)
                
                if [ -z "$$PYTHON_DIR" ]; then
                    echo "Error: Python directory not found"
                    exit 1
                fi
                
                # Extract architecture from path
                ARCH=$$(basename "$$PYTHON_DIR" | sed 's/.*python_3_13_//')
                
                if [ -z "$$ARCH" ] || [ "$$ARCH" = "$$(basename "$$PYTHON_DIR")" ]; then
                    echo "Error: Failed to extract architecture from $$PYTHON_DIR"
                    exit 1
                fi
                
                # Find and archive app code
                {{
                    # App code from _main workspace
                    find app -path "*/_main" 2>/dev/null || true
                    # MANIFEST files for TreeArtifact resolution
                    find app -name "MANIFEST" 2>/dev/null || true
                    # Binary wrapper scripts (py_binary stub scripts)
                    find app -type f -not -path "*/runfiles/*" -not -path "*/rules_python*" -not -path "*/rules_pycross*" 2>/dev/null || true
                    # Binary symlinks
                    find app -type l -not -path "*/runfiles/*" 2>/dev/null || true
                }} | tar -cf ../app_tmp.tar -T -
                cd ..
                
                # Extract to create final layer
                mkdir -p app_layer
                if [ -f app_tmp.tar ] && [ -s app_tmp.tar ]; then
                    tar -xf app_tmp.tar -C app_layer
                fi
                
                # Create symlink from expected Python location to universal location
                # Expected: /app/{{binary}}.runfiles/rules_python++python+python_3_13_{{arch}}
                # Target: /opt/python3.13/{{arch}}
                EXPECTED_DIR="app_layer/$${{PYTHON_DIR}}"
                mkdir -p "$$(dirname "$$EXPECTED_DIR")"
                ln -sf /opt/python3.13/$$ARCH "$$EXPECTED_DIR"
                
                # Create final tar with app code and symlink
                tar -cf $@ -C app_layer .
                """.format(name = name),
            tags = ["manual"],
        )
        
        layer_targets = [
            ":" + name + "_python_layer",
            ":" + name + "_deps_layer",
            ":" + name + "_app_layer",
        ]
    else:
        # For non-Python (Go), use single layer as before
        pkg_tar(
            name = name + "_layer",
            srcs = [binary],
            package_dir = "/app",
            include_runfiles = True,
            strip_prefix = ".",
            tags = ["manual"],
        )
        layer_targets = [":" + name + "_layer"]

    
    binary_name = _get_binary_name(binary)
    binary_path = _get_binary_path(binary)
    image_env = env or {}
    
    # Add SSL_CERT_FILE environment variable for Python's SSL module
    # This points to the CA certificates bundle from rules_distroless
    if "SSL_CERT_FILE" not in image_env:
        image_env["SSL_CERT_FILE"] = "/etc/ssl/certs/ca-certificates.crt"
    
    # Determine entrypoint based on language
    if not entrypoint:
        if language == "python":
            # Python interpreter is accessed via symlink from app runfiles to universal location
            # Symlink: /app/{binary}.runfiles/rules_python++python+python_{version}_{arch}
            # Target: /opt/python3.13/{arch}/
            # Use glob pattern to match the architecture-specific path
            entrypoint = [
                "/bin/sh",
                "-c",
                # Execute Python from the symlinked path (which points to universal location)
                'exec /app/{binary_path}.runfiles/rules_python++python+python_{version}_*/bin/python3 /app/$0 "$@"'.format(
                    binary_path = binary_path,
                    version = python_version.replace(".", "_"),
                ),
                binary_path,
            ]
        else:
            # Go binaries are self-contained executables
            entrypoint = ["/app/" + binary_path]
    
    # Build list of all layers in order (bottom to top)
    # CA certs first (changes rarely), then language-specific layers
    all_tars = ["//tools/cacerts:cacerts"] + layer_targets
    
    oci_image(
        name = name,
        base = base,
        tars = all_tars,
        entrypoint = entrypoint,
        workdir = "/app",
        env = image_env,
        tags = ["manual"],
        **kwargs
    )

def multiplatform_image(
    name,
    binary,
    base = "@ubuntu_base",
    registry = "ghcr.io",
    repository = None,
    image_name = None,
    language = None,
    **kwargs):
    """Build multiplatform OCI images using platform transitions.
    
    ARCHITECTURE: 1 Binary → Platform Transitions → 1 Index → 1 Push
    ==================================================================
    
    Generated Targets:
    
    BUILD TARGETS:
        {name}_base: Base oci_image (built per-platform via transitions)
        {name}: oci_image_index with platform transitions
            → Automatically builds {name}_base for each platform
            → Creates proper OCI index manifest with platform metadata
    
    LOAD TARGET (local testing - must specify platform):
        {name}_load: Load image to local Docker
            → Must use --platforms flag (e.g., --platforms=//tools:linux_arm64)
            → Creates: {image_name}:latest
            → Usage: bazel run //app:my_app_image_load --platforms=//tools:linux_arm64
        
    PUSH TARGET (production release - SINGLE TAG):
        {name}_push: Push OCI image index with ALL platforms
            → Publishes ONE tag containing both amd64 and arm64
            → Docker automatically serves correct architecture
            → Used by: bazel run //tools:release -- release my_app
            → Result: Users pull ONE tag, works on any platform
    
    Platform transitions handle cross-compilation automatically via Bazel's
    configuration system. No platform-specific targets needed!
    
    Usage Example:
        multiplatform_image(
            name = "my_app_image",
            binary = ":my_app",  # Single binary target
            image_name = "demo-my_app",
            language = "python",  # or "go"
        )
    
    Args:
        name: Base name for all generated targets
        binary: Single binary target (built for different platforms via --platforms flag)
        base: Base image (defaults to ubuntu:24.04)
        registry: Container registry (defaults to ghcr.io)
        repository: Organization/namespace (e.g., "whale-net")
        image_name: Image name in domain-app format (e.g., "demo-my_app") - REQUIRED
        language: Language of binary ("python" or "go") - REQUIRED
        **kwargs: Additional arguments passed to container_image
    """
    if not binary:
        fail("binary parameter is required")
    if not language:
        fail("language parameter is required")
    if not image_name:
        fail("image_name parameter is required for multiplatform_image")
    
    # Build platform-specific images using the SAME binary target
    # Platform transitions: Bazel builds the base image once per platform
    container_image(
        name = name + "_base",
        binary = binary,
        base = base,
        language = language,
        **kwargs
    )
    
    # Create multiplatform manifest list using platform transitions
    # The platforms parameter triggers Bazel's configuration transition:
    # - Builds _base image for each platform automatically
    # - Creates proper OCI index with platform metadata
    # 
    # NOTE ON STRUCTURE: This creates a nested index (outer index -> inner index)
    # - Outer index: Points to the inner index blob
    # - Inner index: Contains platform-specific manifests with proper metadata
    # - This is valid per OCI spec and supported by Docker/container registries
    # - When pushed, Docker resolves through the nesting to get the right platform
    #
    # This nested structure is the expected behavior when using platform transitions
    # and is more maintainable than manually creating platform-specific image targets.
    oci_image_index(
        name = name,
        images = [
            ":" + name + "_base",
        ],
        platforms = [
            "//tools:linux_x86_64",
            "//tools:linux_arm64",
        ],
        tags = ["manual"],
        visibility = ["//visibility:public"],  # Allow cross-compilation test to access
    )
    
    # =======================================================================
    # LOAD TARGET: Local testing with explicit platform
    # =======================================================================
    # NOTE: oci_load doesn't support loading image indexes directly.
    # You must specify which Linux platform to load using --platforms flag.
    #
    # Usage:
    #   # On M1/M2 Macs (load Linux ARM64 image):
    #   bazel run //app:app_image_load --platforms=//tools:linux_arm64
    #
    #   # On Intel Macs/PCs (load Linux AMD64 image):
    #   bazel run //app:app_image_load --platforms=//tools:linux_x86_64
    #
    # Without the --platforms flag, you may get macOS binaries which won't run in Docker.
    # The platform flag ensures the image contains Linux binaries for Docker.
    #
    # Example test:
    #   bazel run //demo/hello_fastapi:hello-fastapi_image_load --platforms=//tools:linux_arm64
    #   docker run --rm -p 8000:8000 demo-hello-fastapi:latest
    #   curl http://localhost:8000/
    
    oci_load(
        name = name + "_load",
        image = ":" + name + "_base",
        repo_tags = [image_name + ":latest"],
        tags = ["manual"],
    )
    
    # =======================================================================
    # PUSH TARGET: Production releases (SINGLE TAG with both platforms)
    # =======================================================================
    # Pushes the OCI image index which contains BOTH amd64 and arm64 images.
    # Docker automatically serves the correct architecture when users pull.
    #
    # Usage (via release tool):
    #   bazel run //tools:release -- release-multiarch my_app --version v1.0.0
    #
    # Result: ONE tag published (e.g., ghcr.io/owner/app:v1.0.0)
    #         Manifest contains both platforms, Docker auto-selects correct one
    if repository and image_name:
        repo_path = registry + "/" + repository + "/" + image_name
        
        oci_push(
            name = name + "_push",
            image = ":" + name,
            repository = repo_path,
            remote_tags = [],
            tags = ["manual"],
        )
