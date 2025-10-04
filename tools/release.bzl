"""Release utilities for the Everything monorepo."""

load("//tools:container_image.bzl", "multiplatform_image")
load("//tools/helm:helm.bzl", "helm_chart")
load("//tools:app_info.bzl", "AppInfo")

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
        "organization": ctx.attr.organization,
        "repo_name": ctx.attr.repo_name,
        "domain": ctx.attr.domain,
    }
    
    # Extract metadata from binary's AppInfo provider if available
    port_to_use = ctx.attr.port
    app_type_to_use = ctx.attr.app_type
    args_to_use = ctx.attr.args
    
    if ctx.attr.binary_info:
        binary_app_info = ctx.attr.binary_info[AppInfo]
        
        # Use values from AppInfo provider if not explicitly overridden
        if binary_app_info.port and not port_to_use:
            port_to_use = binary_app_info.port
        if binary_app_info.app_type and not app_type_to_use:
            app_type_to_use = binary_app_info.app_type
        if binary_app_info.args and not args_to_use:
            args_to_use = binary_app_info.args
    
    # Add extracted/provided values to metadata
    if app_type_to_use:
        metadata["app_type"] = app_type_to_use
    if port_to_use:
        metadata["port"] = port_to_use
    metadata["replicas"] = ctx.attr.replicas
    
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
    
    # Add command and args
    if ctx.attr.command:
        metadata["command"] = ctx.attr.command
    if args_to_use:
        metadata["args"] = args_to_use
    
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
        "binary_info": attr.label(providers = [AppInfo]),  # Optional: binary's AppInfo provider
        "image_target": attr.string(mandatory = True),
        "description": attr.string(default = ""),
        "language": attr.string(mandatory = True),
        "registry": attr.string(default = "ghcr.io"),
        "organization": attr.string(default = "whale-net"),
        "repo_name": attr.string(mandatory = True),
        "domain": attr.string(mandatory = True),
        "app_type": attr.string(default = ""),  # Optional, will be inferred if not provided
        "port": attr.int(default = 0),  # Port the app listens on (0 = not specified)
        "replicas": attr.int(default = 0),  # Default replica count (0 = use composer default)
        "health_check_enabled": attr.bool(default = False),  # Whether health checks are enabled
        "health_check_path": attr.string(default = "/health"),  # Health check endpoint path
        "ingress_host": attr.string(default = ""),  # Custom ingress host (empty = use default pattern)
        "ingress_tls_secret": attr.string(default = ""),  # TLS secret name for ingress
        "command": attr.string_list(default = []),  # Container command override
        "args": attr.string_list(default = []),  # Container arguments (optional if binary_info provides them)
    },
)

# Note: This function has many parameters (18) to support flexible app configuration.
# They are logically grouped as:
# - Binary config: name, binary_name, language
# - Release config: domain, description, version, registry, organization, custom_repo_name
# - Deployment config: app_type, port, replicas, command, args
# - Health check config: health_check_enabled, health_check_path
# - Ingress config: ingress_host, ingress_tls_secret
# Bazel/Starlark does not support nested struct parameters, so they remain flat.
def release_app(name, binary_name = None, language = None, domain = None, description = "", version = "latest", registry = "ghcr.io", organization = "whale-net", custom_repo_name = None, app_type = "", port = 0, replicas = 0, health_check_enabled = False, health_check_path = "/health", ingress_host = "", ingress_tls_secret = "", command = [], args = []):
    """Convenience macro to set up release metadata and OCI images for an app.
    
    This macro consolidates the creation of OCI images and release metadata,
    ensuring consistency between the two systems. Works with multiplatform_py_binary
    and multiplatform_go_binary which auto-generate platform-specific binaries.
    
    For multiplatform builds, use the corresponding wrapper macros:
    - Python: multiplatform_py_binary (from //tools:python_binary.bzl)
    - Go: multiplatform_go_binary (from //tools:go_binary.bzl)
    
    Both macros create {name}_linux_amd64 and {name}_linux_arm64 targets automatically,
    enabling cross-compilation for container images. They also create {name}_info targets
    that expose AppInfo providers with metadata (args, port, app_type).
    
    Args:
        name: App name (should match directory name and multiplatform binary name)
        binary_name: Target label for the binaries. Can be:
                     - Simple name: "my_app" -> looks for :my_app_linux_amd64/arm64
                     - Full label: "//path/to:binary" -> looks for //path/to:binary_linux_amd64/arm64
                     Defaults to name if not provided.
        language: Programming language ("python" or "go")
        domain: Domain/category for the app (e.g., "demo", "api", "web")
        description: Optional description of the app
        version: Default version (can be overridden at release time)
        registry: Container registry (defaults to ghcr.io)
        organization: Container registry organization (defaults to whale-net)
        custom_repo_name: Custom repository name (defaults to name)
        app_type: Application type (external-api, internal-api, worker, job).
                  Optional: automatically extracted from binary's AppInfo if not specified.
        port: Port the application listens on (0 = not specified).
              Optional: automatically extracted from binary's AppInfo if not specified.
        replicas: Default number of replicas (0 = use composer default based on app_type)
        health_check_enabled: Whether to enable health checks (default: False)
        health_check_path: Path for health check endpoint (default: /health)
        ingress_host: Custom ingress hostname (empty = use default {app}-{env}.local pattern)
        ingress_tls_secret: TLS secret name for ingress (empty = no TLS)
        command: Override container command (default: use image ENTRYPOINT)
        args: Container arguments (optional: automatically extracted from binary's AppInfo if not specified)
    """
    if language not in ["python", "go"]:
        fail("Unsupported language: {}. Must be 'python' or 'go'".format(language))
    
    # Construct binary targets from binary_name
    # If binary_name is not provided, default to :name (same package)
    # If binary_name starts with // or :, use it as-is (it's a label)
    # Otherwise, treat it as a simple name in the current package
    base_label = binary_name if binary_name else name
    if not base_label.startswith("//") and not base_label.startswith(":"):
        base_label = ":" + base_label
    
    binary_amd64 = base_label + "_linux_amd64"
    binary_arm64 = base_label + "_linux_arm64"
    binary_info_label = base_label + "_info"  # AppInfo provider target
    
    # Image name uses domain-app format (e.g., "demo-hello_python")
    # Repository is just the organization (e.g., "whale-net")
    # Full path will be: registry/repository/image_name (e.g., "ghcr.io/whale-net/demo-hello_python")
    image_name = domain + "-" + name
    image_target = name + "_image"
    
    # Create multiplatform OCI image using the explicitly provided or defaulted binaries
    # CRITICAL: Pass image_name explicitly so domain+name identifies the app
    multiplatform_image(
        name = image_target,
        binary_amd64 = binary_amd64,
        binary_arm64 = binary_arm64,
        registry = registry,
        repository = organization,  # Just the org, not org/image
        image_name = image_name,  # Explicit domain-app format
        language = language,
    )
    
    # Both Python and Go now create AppInfo providers
    binary_info = binary_info_label
    
    # Create release metadata (use AMD64 binary as reference for metadata)
    app_metadata(
        name = name + "_metadata",
        app_name = name,  # Pass the actual app name
        binary_target = binary_amd64,  # Reference binary for metadata
        binary_info = binary_info,  # AppInfo provider for extracting args, etc
        image_target = image_target,
        description = description,
        version = version,
        language = language,
        registry = registry,
        organization = organization,
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
        args = args,  # Container arguments (optional: overrides binary's args if provided)
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
