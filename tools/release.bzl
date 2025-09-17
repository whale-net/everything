"""Release utilities for the Everything monorepo."""

load("//tools:oci.bzl", "python_oci_image_multiplatform", "go_oci_image_multiplatform")
load("@rules_oci//oci:defs.bzl", "oci_push")

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

def release_app(name, binary_target, language, domain, description = "", version = "latest", registry = "ghcr.io", custom_repo_name = None):
    """Convenience macro to set up release metadata and OCI images for an app.
    
    This macro consolidates the creation of OCI images and release metadata,
    ensuring consistency between the two systems.
    
    Args:
        name: App name (should match directory name)
        binary_target: The py_binary or go_binary target for this app
        language: Programming language ("python" or "go")
        domain: Domain/category for the app (e.g., "demo", "api", "web")
        description: Optional description of the app
        version: Default version (can be overridden at release time)
        registry: Container registry (defaults to ghcr.io)
        custom_repo_name: Custom repository name (defaults to name)
    """
    if language not in ["python", "go"]:
        fail("Unsupported language: {}. Must be 'python' or 'go'".format(language))
    
    # Repository name for container images should use domain-app format
    image_name = domain + "-" + name
    image_target = name + "_image"
    repo_tag = image_name + ":latest"
    
    # Create OCI images based on language
    # Tag with "manual" so they're not built by //... (only when explicitly requested)
    if language == "python":
        python_oci_image_multiplatform(
            name = image_target,
            binary = binary_target,
            repo_tag = repo_tag,
            tags = ["manual", "release"],
        )
    elif language == "go":
        go_oci_image_multiplatform(
            name = image_target,
            binary = binary_target,
            repo_tag = repo_tag,
            tags = ["manual", "release"],
        )
    
    # Create oci_push target for the multi-platform image
    # This will push a manifest that includes both AMD64 and ARM64
    registry_repo = registry + "/whale-net/" + image_name  # Hardcode whale-net org for now
    
    oci_push(
        name = image_target + "_push",
        image = ":" + image_target,
        repository = registry_repo,
        tags = ["manual", "release"],
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
        tags = ["manual", "release"],  # Don't build with //...
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
        Dict with image target names
    """
    base_name = app_name + "_image"
    return {
        "base": "//" + app_name + ":" + base_name,
        "amd64": "//" + app_name + ":" + base_name + "_amd64",
        "arm64": "//" + app_name + ":" + base_name + "_arm64",
    }
