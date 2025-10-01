"""Release utilities for the Everything monorepo."""

load("//tools:multiplatform_image.bzl", "multiplatform_python_image", "multiplatform_go_image", "multiplatform_push")
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
# - Binary config: name, binary_target, binary_amd64, binary_arm64, language
# - Release config: domain, description, version, registry, custom_repo_name
# - Deployment config: app_type, port, replicas, command, args
# - Health check config: health_check_enabled, health_check_path
# - Ingress config: ingress_host, ingress_tls_secret
# Bazel/Starlark does not support nested struct parameters, so they remain flat.
def release_app(name, binary_target = None, binary_amd64 = None, binary_arm64 = None, language = None, domain = None, description = "", version = "latest", registry = "ghcr.io", custom_repo_name = None, app_type = "", port = 0, replicas = 0, health_check_enabled = True, health_check_path = "/health", ingress_host = "", ingress_tls_secret = "", command = [], args = []):
    """Convenience macro to set up release metadata and OCI images for an app.
    
    This macro consolidates the creation of OCI images and release metadata,
    ensuring consistency between the two systems. When used with multiplatform_py_binary,
    it automatically detects platform-specific binaries.
    
    Args:
        name: App name (should match directory name and multiplatform_py_binary name)
        binary_target: The py_binary or go_binary target for this app (used for both platforms if platform-specific binaries not provided)
        binary_amd64: AMD64-specific binary target (auto-detected if using multiplatform_py_binary)
        binary_arm64: ARM64-specific binary target (auto-detected if using multiplatform_py_binary)
        language: Programming language ("python" or "go")
        domain: Domain/category for the app (e.g., "demo", "api", "web")
        description: Optional description of the app
        version: Default version (can be overridden at release time)
        registry: Container registry (defaults to ghcr.io)
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
    
    # Auto-detect platform-specific binaries for Python apps using multiplatform_py_binary
    if language == "python" and not binary_amd64 and not binary_arm64 and not binary_target:
        # Assume multiplatform_py_binary pattern: name, name_linux_amd64, name_linux_arm64
        amd64_binary = ":" + name + "_linux_amd64"
        arm64_binary = ":" + name + "_linux_arm64"
    else:
        # Use explicitly provided binaries or fall back to single binary_target
        amd64_binary = binary_amd64 if binary_amd64 else binary_target
        arm64_binary = binary_arm64 if binary_arm64 else binary_target
    
    if not amd64_binary or not arm64_binary:
        fail("Must provide either 'binary_target' for both platforms, both 'binary_amd64' and 'binary_arm64', or use multiplatform_py_binary with name '{}'".format(name))
    
    # Repository name for container images should use domain-app format
    image_name = domain + "-" + name
    image_target = name + "_image"
    repository = registry + "/whale-net/" + image_name  # Hardcode whale-net org for now
    
    # Create multiplatform OCI images based on language
    # Tag with "manual" so they're not built by //... (only when explicitly requested)
    if language == "python":
        if binary_amd64 and binary_arm64:
            # Use platform-specific binaries
            multiplatform_python_image(
                name = image_target,
                binary_amd64 = amd64_binary,
                binary_arm64 = arm64_binary,
                repository = repository,
                tags = ["manual", "container-image"],
            )
        else:
            # Use single binary for both platforms
            multiplatform_python_image(
                name = image_target,
                binary = amd64_binary,  # Use either binary for both platforms
                repository = repository,
                tags = ["manual", "container-image"],
            )
    elif language == "go":
        multiplatform_go_image(
            name = image_target,
            binary = binary_target,
            repository = repository, 
            tags = ["manual", "container-image"],
        )
    
    # Create push targets for all image variants
    multiplatform_push(
        name = image_target + "_push",
        image = image_target,
        repository = repository,
        tag = "latest",
        tags = ["manual", "container-push"],
    )
    
    # Create release metadata
    app_metadata(
        name = name + "_metadata",
        app_name = name,  # Pass the actual app name
        binary_target = amd64_binary,  # Use the resolved AMD64 binary as primary reference
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
    chart_version = "0.1.0",
    namespace = None,
    environment = "production",
    domain = None,
    manual_manifests = [],
    **kwargs
):
    """Convenience macro to set up a releasable Helm chart.
    
    This macro wraps helm_chart and creates release metadata for CI/CD integration.
    
    Args:
        name: Target name for the chart
        apps: List of app_metadata targets to include (e.g., ["//demo/hello_python:hello_python_metadata"])
        chart_name: Name of the Helm chart (defaults to name)
        chart_version: Initial version of the chart (will be overridden during release)
        namespace: Kubernetes namespace for the chart
        environment: Target environment (development, staging, production)
        domain: Domain/category for the chart (e.g., "demo", "api", required)
        manual_manifests: List of k8s_manifests targets or direct YAML files
        **kwargs: Additional arguments passed to helm_chart
    """
    if not domain:
        fail("domain is required for release_helm_chart")
    
    if not namespace:
        fail("namespace is required for release_helm_chart")
    
    actual_chart_name = chart_name or name
    
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
