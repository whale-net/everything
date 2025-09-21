"""Release utilities for the Everything monorepo."""

load("//tools:multiplatform_image.bzl", "multiplatform_python_image", "multiplatform_go_image", "multiplatform_push")

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
    }
    
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
    },
)

def release_app(name, binary_target = None, binary_amd64 = None, binary_arm64 = None, language = None, domain = None, description = "", version = "latest", registry = "ghcr.io", custom_repo_name = None):
    """Convenience macro to set up release metadata and OCI images for an app.
    
    This macro consolidates the creation of OCI images and release metadata,
    ensuring consistency between the two systems.
    
    Args:
        name: App name (should match directory name)
        binary_target: The py_binary or go_binary target for this app (used for both platforms if platform-specific binaries not provided)
        binary_amd64: AMD64-specific binary target (overrides binary_target for AMD64)
        binary_arm64: ARM64-specific binary target (overrides binary_target for ARM64)
        language: Programming language ("python" or "go")
        domain: Domain/category for the app (e.g., "demo", "api", "web")
        description: Optional description of the app
        version: Default version (can be overridden at release time)
        registry: Container registry (defaults to ghcr.io)
        custom_repo_name: Custom repository name (defaults to name)
    """
    if language not in ["python", "go"]:
        fail("Unsupported language: {}. Must be 'python' or 'go'".format(language))
    
    # Determine which binaries to use for each platform
    amd64_binary = binary_amd64 if binary_amd64 else binary_target
    arm64_binary = binary_arm64 if binary_arm64 else binary_target
    
    if not amd64_binary or not arm64_binary:
        fail("Must provide either 'binary_target' for both platforms or both 'binary_amd64' and 'binary_arm64'")
    
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
