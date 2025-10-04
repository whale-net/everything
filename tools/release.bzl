"""Release utilities for the Everything monorepo."""

load("//tools:container_image.bzl", "multiplatform_image")
load("//tools/helm:helm.bzl", "helm_chart")

def _app_metadata_impl(ctx):
    """Implementation for app_metadata rule."""
    # Create a JSON file with app metadata
    # The app name should be passed explicitly, not derived from rule name
    metadata = {
        "name": ctx.attr.app_name,  # Use explicit app_name instead of ctx.attr.name
        "version": ctx.attr.version,
        "binary_target": ctx.attr.binary_target,
        "image_target": ctx.attr.image_target,
        "description": ctx.attr.description,
        "language": ctx.attr.language,
        "registry": ctx.attr.registry,
        "repo_name": ctx.attr.repo_name,
        "domain": ctx.attr.domain,
        "app_type": ctx.attr.app_type,  # Add app_type to metadata
        "port": ctx.attr.port,  # Port the app listens on
        "replicas": ctx.attr.replicas,  # Default replica count
    }
    
    # Add optional health check configuration if provided
    if ctx.attr.health_check_enabled:
        metadata["health_check"] = {
            "enabled": ctx.attr.health_check_enabled,
            "path": ctx.attr.health_check_path,
        }
    
    # Add optional ingress configuration if provided
    if ctx.attr.ingress_host:
        metadata["ingress"] = {
            "host": ctx.attr.ingress_host,
            "tls_secret_name": ctx.attr.ingress_tls_secret,
        }
    
    # Add command and args if provided
    if ctx.attr.command:
        metadata["command"] = ctx.attr.command
    if ctx.attr.args:
        metadata["args"] = ctx.attr.args
    
    output = ctx.actions.declare_file(ctx.label.name + "_metadata.json")
    ctx.actions.write(
        output = output,
        content = json.encode(metadata),
    )
    
    return [DefaultInfo(files = depset([output]))]

app_metadata = rule(
    implementation = _app_metadata_impl,
    attrs = {
        "app_name": attr.string(mandatory = True),  # Add explicit app_name attribute
        "version": attr.string(default = "latest"),
        "binary_target": attr.string(mandatory = True),
        "image_target": attr.string(mandatory = True),
        "description": attr.string(default = ""),
        "language": attr.string(mandatory = True),
        "registry": attr.string(default = "ghcr.io"),
        "repo_name": attr.string(mandatory = True),
        "domain": attr.string(mandatory = True),
        "app_type": attr.string(default = ""),  # Optional, will be inferred if not provided
        "port": attr.int(default = 0),  # Port the app listens on (0 = not specified)
        "replicas": attr.int(default = 0),  # Default replica count (0 = use composer default)
        "health_check_enabled": attr.bool(default = True),  # Whether health checks are enabled
        "health_check_path": attr.string(default = "/health"),  # Health check endpoint path
        "ingress_host": attr.string(default = ""),  # Custom ingress host (empty = use default pattern)
        "ingress_tls_secret": attr.string(default = ""),  # TLS secret name for ingress
        "command": attr.string_list(default = []),  # Container command override
        "args": attr.string_list(default = []),  # Container arguments
    },
)

# Note: This function has many parameters (16) to support flexible app configuration.
# They are logically grouped as:
# - Binary config: name, binary_target, language
# - Release config: domain, description, version, registry, organization, custom_repo_name
# - Deployment config: app_type, port, replicas, command, args
# - Health check config: health_check_enabled, health_check_path
# - Ingress config: ingress_host, ingress_tls_secret
# Bazel/Starlark does not support nested struct parameters, so they remain flat.
def release_app(name, binary_target = None, language = None, domain = None, description = "", version = "latest", registry = "ghcr.io", organization = "whale-net", custom_repo_name = None, app_type = "", port = 0, replicas = 0, health_check_enabled = True, health_check_path = "/health", ingress_host = "", ingress_tls_secret = "", command = [], args = []):
    """Convenience macro to set up release metadata and OCI images for an app.
    
    This macro consolidates the creation of OCI images and release metadata,
    ensuring consistency between the two systems. Works with multiplatform_py_binary
    and multiplatform_go_binary which auto-generate platform-specific binaries.
    
    For multiplatform builds, use the corresponding wrapper macros:
    - Python: multiplatform_py_binary (from //tools:python_binary.bzl)
    - Go: multiplatform_go_binary (from //tools:go_binary.bzl)
    
    Both macros create {name}_linux_amd64 and {name}_linux_arm64 targets automatically,
    enabling cross-compilation for container images.
    
    Args:
        name: App name (should match directory name and multiplatform binary name)
        binary_target: Optional binary target. If not provided, auto-detects:
                       - Python: Checks for :name_linux_amd64 (from multiplatform_py_binary)
                       - Go: Checks for :name_linux_amd64 (from multiplatform_go_binary)
        language: Programming language ("python" or "go")
        domain: Domain/category for the app (e.g., "demo", "api", "web")
        description: Optional description of the app
        version: Default version (can be overridden at release time)
        registry: Container registry (defaults to ghcr.io)
        organization: Container registry organization (defaults to whale-net)
        custom_repo_name: Custom repository name (defaults to name)
        app_type: Application type for Helm chart generation (external-api, internal-api, worker, job).
                  If empty, will be inferred from app name by the Helm composer tool.
        port: Port the application listens on (required for API types, 0 = not specified)
        replicas: Default number of replicas (0 = use composer default based on app_type)
        health_check_enabled: Whether to enable health checks (default: True for APIs)
        health_check_path: Path for health check endpoint (default: /health)
        ingress_host: Custom ingress hostname (empty = use default {app}-{env}.local pattern)
        ingress_tls_secret: TLS secret name for ingress (empty = no TLS)
        command: Override container command (default: use image ENTRYPOINT)
        args: Container arguments (default: empty, or binary's default args)
    """
    if language not in ["python", "go"]:
        fail("Unsupported language: {}. Must be 'python' or 'go'".format(language))
    
    # Auto-detect binary targets if not provided
    # Both Python and Go apps use platform-specific binaries for cross-compilation
    #
    # NOTE: For both languages, binary_target defaults to _linux_amd64 for metadata purposes only.
    # The actual multiplatform build (below) explicitly uses BOTH _linux_amd64 and _linux_arm64,
    # so both architectures are built correctly. This is just a reference target for metadata.
    if not binary_target:
        if language == "python":
            binary_target = ":" + name + "_linux_amd64"
        else:
            # Go apps also use platform-specific binaries for cross-compilation
            binary_target = ":" + name + "_linux_amd64"
    
    # Repository name for container images should use domain-app format
    image_name = domain + "-" + name
    image_target = name + "_image"
    repository = organization + "/" + image_name  # Repository path (without registry)
    
    # Derive platform-specific binary targets from binary_target
    # If binary_target ends with _linux_amd64, derive ARM64 by replacing the suffix
    # This handles both local targets (":name_linux_amd64") and cross-package targets ("//path:name_linux_amd64")
    if binary_target.endswith("_linux_amd64"):
        # Already platform-specific - derive both platforms
        binary_amd64_target = binary_target
        binary_arm64_target = binary_target.replace("_linux_amd64", "_linux_arm64")
    else:
        # Assume standard naming: append platform suffixes
        binary_amd64_target = binary_target + "_linux_amd64"
        binary_arm64_target = binary_target + "_linux_arm64"
    
    # Create multiplatform OCI image
    if language == "python":
        # Python apps have platform-specific binaries with correct wheels for each architecture
        # CRITICAL: Both platforms are built explicitly here - platform transitions ensure
        # each gets the correct wheels for its architecture (x86_64 vs aarch64)
        multiplatform_image(
            name = image_target,
            binary_amd64 = binary_amd64_target,
            binary_arm64 = binary_arm64_target,
            registry = registry,
            repository = repository,
            language = language,
        )
    else:
        # Go apps need platform-specific binaries for cross-compilation
        # Similar to Python, we explicitly build for both architectures
        multiplatform_image(
            name = image_target,
            binary_amd64 = binary_amd64_target,
            binary_arm64 = binary_arm64_target,
            registry = registry,
            repository = repository,
            language = language,
        )
    
    # Create release metadata
    app_metadata(
        name = name + "_metadata",
        app_name = name,  # Pass the actual app name
        binary_target = binary_target,
        image_target = image_target,
        description = description,
        version = version,
        language = language,
        registry = registry,
        repo_name = image_name,  # Use domain-app format
        domain = domain,
        app_type = app_type,  # Pass through app_type for Helm chart generation
        port = port,  # Port configuration
        replicas = replicas,  # Replica count
        health_check_enabled = health_check_enabled,  # Health check configuration
        health_check_path = health_check_path,
        ingress_host = ingress_host,  # Ingress configuration
        ingress_tls_secret = ingress_tls_secret,
        command = command,  # Container command override
        args = args,  # Container arguments
        tags = ["release-metadata"],  # No manual tag - metadata should be easily discoverable
        visibility = ["//visibility:public"],
    )

def get_release_metadata_target(app_name):
    """Get the metadata target name for an app.
    
    Args:
        app_name: Name of the app
        
    Returns:
        Target name for the app's metadata
    """
    return "//" + app_name + ":" + app_name + "_metadata"

def get_image_targets(app_name):
    """Get all image target names for an app.
    
    Args:
        app_name: Name of the app
        
    Returns:
        Dict with image target names including platform-specific push targets
    """
    base_name = app_name + "_image"
    return {
        "base": "//" + app_name + ":" + base_name,
        "amd64": "//" + app_name + ":" + base_name,  # AMD64 uses base target
        "arm64": "//" + app_name + ":" + base_name,  # ARM64 uses same base target with different platform flag
        "push_base": "//" + app_name + ":" + base_name + "_push",
        "push_amd64": "//" + app_name + ":" + base_name + "_push_amd64",
        "push_arm64": "//" + app_name + ":" + base_name + "_push_arm64",
    }

def _helm_chart_metadata_impl(ctx):
    """Implementation for helm_chart_metadata rule."""
    # Create a JSON file with helm chart metadata
    metadata = {
        "name": ctx.attr.chart_name,
        "version": ctx.attr.chart_version,
        "namespace": ctx.attr.namespace,
        "environment": ctx.attr.environment,
        "domain": ctx.attr.domain,
        "apps": ctx.attr.app_names,  # List of app names this chart includes
        "chart_target": ctx.attr.chart_target,  # The actual helm_chart target
    }
    
    output = ctx.actions.declare_file(ctx.label.name + "_chart_metadata.json")
    ctx.actions.write(
        output = output,
        content = json.encode(metadata),
    )
    
    return [DefaultInfo(files = depset([output]))]

helm_chart_metadata = rule(
    implementation = _helm_chart_metadata_impl,
    attrs = {
        "chart_name": attr.string(mandatory = True),
        "chart_version": attr.string(mandatory = True),
        "namespace": attr.string(mandatory = True),
        "environment": attr.string(default = "production"),
        "domain": attr.string(mandatory = True),
        "app_names": attr.string_list(mandatory = True),
        "chart_target": attr.string(mandatory = True),
    },
)

def release_helm_chart(
    name,
    apps,
    chart_name = None,
    chart_version = "0.0.0-dev",
    namespace = None,
    environment = "production",
    domain = None,
    manual_manifests = [],
    **kwargs
):
    """Convenience macro to set up a releasable Helm chart.
    
    This macro wraps helm_chart and creates release metadata for CI/CD integration.
    The actual chart name will be prefixed with "helm-{namespace}-" to make artifacts
    clearly identifiable (e.g., "helm-demo-hello-fastapi").
    
    Args:
        name: Target name for the chart
        apps: List of app_metadata targets to include (e.g., ["//demo/hello_python:hello_python_metadata"])
        chart_name: Base name of the Helm chart (defaults to name). 
                   Will be prefixed with "helm-{namespace}-" automatically.
        chart_version: Version for local builds (default: "0.0.0-dev"). 
                      This is overridden during release by auto-versioning from git tags.
                      Only affects local/development builds.
        namespace: Kubernetes namespace for the chart
        environment: Target environment (development, staging, production)
        domain: Domain/category for the chart (e.g., "demo", "api", required)
        manual_manifests: List of k8s_manifests targets or direct YAML files
        **kwargs: Additional arguments passed to helm_chart
        
    Example:
        release_helm_chart(
            name = "fastapi_chart",
            chart_name = "hello-fastapi",  # Will become "helm-demo-hello-fastapi"
            namespace = "demo",
            domain = "demo",
            apps = ["//demo/hello_fastapi:hello_fastapi_metadata"],
        )
    """
    if not domain:
        fail("domain is required for release_helm_chart")
    
    if not namespace:
        fail("namespace is required for release_helm_chart")
    
    # Construct the actual chart name with helm-namespace- prefix
    # This makes chart artifacts clearly identifiable (e.g., helm-demo-hello-fastapi)
    base_chart_name = chart_name or name
    actual_chart_name = "helm-{}-{}".format(namespace, base_chart_name)
    
    # Create the helm_chart target
    helm_chart(
        name = name,
        apps = apps,
        chart_name = actual_chart_name,
        chart_version = chart_version,
        namespace = namespace,
        environment = environment,
        manual_manifests = manual_manifests,
        **kwargs
    )
    
    # Extract app names from app_metadata targets
    # Target format: "//demo/hello_python:hello_python_metadata"
    app_names = []
    for app_target in apps:
        # Extract the app name from the target
        # Split on : to get the target name, then remove _metadata suffix
        target_parts = app_target.split(":")
        if len(target_parts) == 2:
            target_name = target_parts[1]
            if target_name.endswith("_metadata"):
                app_name = target_name[:-9]  # Remove "_metadata"
                app_names.append(app_name)
    
    # Create release metadata for the chart
    helm_chart_metadata(
        name = name + "_chart_metadata",
        chart_name = actual_chart_name,
        chart_version = chart_version,
        namespace = namespace,
        environment = environment,
        domain = domain,
        app_names = app_names,
        chart_target = ":" + name,
        tags = ["helm-release-metadata"],
        visibility = ["//visibility:public"],
    )
