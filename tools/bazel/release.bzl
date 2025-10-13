"""Release utilities for the Everything monorepo."""

load("//tools/bazel:container_image.bzl", "multiplatform_image")
load("//tools/helm:helm.bzl", "helm_chart")
load("//tools/openapi:openapi.bzl", "openapi_spec")

def _app_metadata_impl(ctx):
    """Implementation for app_metadata rule."""
    # Create a JSON file with app metadata
    metadata = {
        "name": ctx.attr.app_name,
        "version": ctx.attr.version,
        "binary_target": str(ctx.attr.binary_target.label),
        "image_target": str(ctx.attr.image_target.label),
        "description": ctx.attr.description,
        "language": ctx.attr.language,
        "registry": ctx.attr.registry,
        "organization": ctx.attr.organization,
        "repo_name": ctx.attr.repo_name,
        "domain": ctx.attr.domain,
    }
    
    # Add metadata directly from attributes
    if ctx.attr.app_type:
        metadata["app_type"] = ctx.attr.app_type
    if ctx.attr.port:
        metadata["port"] = ctx.attr.port
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
    if ctx.attr.args:
        metadata["args"] = ctx.attr.args
    
    # Add OpenAPI spec target if provided
    if ctx.attr.openapi_spec_target:
        metadata["openapi_spec_target"] = str(ctx.attr.openapi_spec_target.label)
    
    output = ctx.actions.declare_file(ctx.label.name + "_metadata.json")
    ctx.actions.write(
        output = output,
        content = json.encode(metadata),
    )
    
    return [DefaultInfo(files = depset([output]))]

app_metadata = rule(
    implementation = _app_metadata_impl,
    attrs = {
        "app_name": attr.string(mandatory = True),
        "version": attr.string(default = "latest"),
        "binary_target": attr.label(mandatory = True),
        "image_target": attr.label(mandatory = True),
        "description": attr.string(default = ""),
        "language": attr.string(mandatory = True),
        "registry": attr.string(default = "ghcr.io"),
        "organization": attr.string(default = "whale-net"),
        "repo_name": attr.string(mandatory = True),
        "domain": attr.string(mandatory = True),
        "app_type": attr.string(default = ""),
        "port": attr.int(default = 0),
        "replicas": attr.int(default = 0),
        "health_check_enabled": attr.bool(default = False),
        "health_check_path": attr.string(default = "/health"),
        "ingress_host": attr.string(default = ""),
        "ingress_tls_secret": attr.string(default = ""),
        "command": attr.string_list(default = []),
        "args": attr.string_list(default = []),
        "openapi_spec_target": attr.label(default = None),
    },
)

# Note: This function has many parameters (19) to support flexible app configuration.
# They are logically grouped as:
# - Binary config: name, binary_name, language
# - Release config: domain, description, version, registry, organization, custom_repo_name
# - Deployment config: app_type, port, replicas, command, args
# - Health check config: health_check_enabled, health_check_path
# - Ingress config: ingress_host, ingress_tls_secret
# - OpenAPI config: fastapi_app
# Bazel/Starlark does not support nested struct parameters, so they remain flat.
def release_app(name, binary_name = None, language = None, domain = None, description = "", version = "latest", registry = "ghcr.io", organization = "whale-net", custom_repo_name = None, app_type = "", port = 0, replicas = 0, health_check_enabled = False, health_check_path = "/health", ingress_host = "", ingress_tls_secret = "", command = [], args = [], fastapi_app = None):
    """Convenience macro to set up release metadata and OCI images for an app.
    
    This macro consolidates the creation of OCI images and release metadata,
    ensuring consistency between the two systems. Works with standard py_binary
    and go_binary targets.
    
    The binaries are built for different platforms using Bazel's --platforms flag.
    Cross-compilation is handled automatically by rules_pycross (Python) and rules_go (Go).
    
    Args:
        name: App name (MUST use dashes, not underscores: 'my-app' not 'my_app')
        binary_name: Target label for the binary. Can be:
                     - Simple name: "my_app" -> looks for :my_app
                     - Full label: "//path/to:binary" -> uses that binary
                     Defaults to name if not provided.
        language: Programming language ("python", "go", or "scala")
        domain: Domain/category for the app (e.g., "demo", "api", "web")
        description: Optional description of the app
        version: Default version (can be overridden at release time)
        registry: Container registry (defaults to ghcr.io)
        organization: Container registry organization (defaults to whale-net)
        custom_repo_name: Custom repository name (defaults to name)
        app_type: Application type (external-api, internal-api, worker, job)
        port: Port the application listens on (0 = not specified)
        replicas: Default number of replicas (0 = use composer default based on app_type)
        health_check_enabled: Whether to enable health checks (default: False)
        health_check_path: Path for health check endpoint (default: /health)
        ingress_host: Custom ingress hostname (empty = use default {app}-{env}.local pattern)
        ingress_tls_secret: TLS secret name for ingress (empty = no TLS)
        command: Override container command (default: use image ENTRYPOINT)
        args: Container arguments
        fastapi_app: For FastAPI apps, specify the module path and variable name (e.g., "main:app")
                     to auto-generate OpenAPI specs. Creates a {name}_openapi_spec target.
    """
    # Validate name format - must use dashes, not underscores
    if "_" in name:
        fail("App name '{}' contains underscores. Use dashes instead (e.g., 'my-app' not 'my_app')".format(name))
    
    if language not in ["python", "go", "scala"]:
        fail("Unsupported language: {}. Must be 'python', 'go', or 'scala'".format(language))
    
    # Single binary target - no platform suffixes needed
    # Binary will be built for different platforms using --platforms flag
    base_label = binary_name if binary_name else name
    if not base_label.startswith("//") and not base_label.startswith(":"):
        base_label = ":" + base_label
    
    # Image name uses domain-app format (e.g., "demo-hello-python")
    image_name = domain + "-" + name
    image_target = name + "_image"
    
    # Create multiplatform OCI image using SINGLE binary target
    # Bazel will build it for different platforms based on --platforms flag
    multiplatform_image(
        name = image_target,
        binary = base_label,  # Single binary, built for different platforms
        registry = registry,
        repository = organization,
        image_name = image_name,
        language = language,
        cmd = args if args else [],  # Pass container args if specified
    )
    
    # Use the binary directly for change detection
    # All platforms are built from the same sources, so one reference is enough
    binary_target_ref = base_label
    
    # Auto-generate OpenAPI spec for FastAPI apps (before metadata creation)
    openapi_spec_target_ref = None
    if fastapi_app and language == "python":
        # Parse module:variable syntax
        if ":" in fastapi_app:
            module_path, app_var = fastapi_app.split(":", 1)
        else:
            module_path = fastapi_app
            app_var = "app"
        
        # For OpenAPI generation, we need a library target, not a binary
        # Try to find a corresponding _lib target, or use the binary if that's all we have
        lib_target = base_label
        if not lib_target.endswith("_lib"):
            # Check if there's a {name}_lib or main_lib target we should use instead
            # For now, just use the binary target - it will work but might be less efficient
            pass
        
        # Use the openapi_spec rule to generate spec with proper dependencies
        openapi_spec_target_name = name + "_openapi_spec"
        openapi_spec(
            name = openapi_spec_target_name,
            app_target = lib_target,
            module_path = module_path,
            app_variable = app_var,
            domain = domain,
            visibility = ["//visibility:public"],
        )
        openapi_spec_target_ref = ":" + openapi_spec_target_name
    
    # Create release metadata
    app_metadata(
        name = name + "_metadata",
        app_name = name,
        binary_target = binary_target_ref,
        image_target = image_target,
        description = description,
        version = version,
        language = language,
        registry = registry,
        organization = organization,
        repo_name = image_name,
        domain = domain,
        app_type = app_type,
        port = port,
        replicas = replicas,
        health_check_enabled = health_check_enabled,
        health_check_path = health_check_path,
        ingress_host = ingress_host,
        ingress_tls_secret = ingress_tls_secret,
        command = command,
        args = args,
        openapi_spec_target = openapi_spec_target_ref,
        tags = ["release-metadata"],
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
        "push": "//" + app_name + ":" + base_name + "_push",
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
        apps: List of app_metadata targets to include (e.g., ["//demo/hello_python:hello-python_metadata"])
        chart_name: Base name of the Helm chart (defaults to name, MUST use dashes not underscores). 
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
            apps = ["//demo/hello_fastapi:hello-fastapi_metadata"],
        )
    """
    if not domain:
        fail("domain is required for release_helm_chart")
    
    if not namespace:
        fail("namespace is required for release_helm_chart")
    
    # Validate chart_name format - must use dashes, not underscores
    base_chart_name = chart_name or name
    if "_" in base_chart_name:
        fail("Chart name '{}' contains underscores. Use dashes instead (e.g., 'my-chart' not 'my_chart')".format(base_chart_name))
    
    # Construct the actual chart name with helm-namespace- prefix
    # This makes chart artifacts clearly identifiable (e.g., helm-demo-hello-fastapi)
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
    # Target format: "//demo/hello_python:hello-python_metadata"
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
