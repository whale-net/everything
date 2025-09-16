"""Release utilities for the Everything monorepo."""

load("//tools:oci.bzl", "python_oci_image_multiplatform", "go_oci_image_multiplatform")

def _app_metadata_impl(ctx):
    """Implementation for app_metadata rule."""
    # Create a JSON file with app metadata
    metadata = {
        "name": ctx.attr.name,
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
    
    # Repository name for container images
    repo_name = custom_repo_name if custom_repo_name else name
    image_target = name + "_image"
    repo_tag = repo_name + ":latest"
    
    # Create OCI images based on language
    if language == "python":
        python_oci_image_multiplatform(
            name = image_target,
            binary = binary_target,
            repo_tag = repo_tag,
        )
    elif language == "go":
        go_oci_image_multiplatform(
            name = image_target,
            binary = binary_target,
            repo_tag = repo_tag,
        )
    
    # Create release metadata
    app_metadata(
        name = name + "_metadata",
        binary_target = binary_target,
        image_target = image_target,
        description = description,
        version = version,
        language = language,
        registry = registry,
        repo_name = repo_name,
        domain = domain,
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
        "tarball": "//" + app_name + ":" + base_name + "_tarball",
        "amd64": "//" + app_name + ":" + base_name + "_amd64",
        "arm64": "//" + app_name + ":" + base_name + "_arm64",
        "amd64_tarball": "//" + app_name + ":" + base_name + "_amd64_tarball",
        "arm64_tarball": "//" + app_name + ":" + base_name + "_arm64_tarball",
    }

def format_registry_tags(registry, repo_name, version, commit_sha = None):
    """Format container registry tags for an app.
    
    Args:
        registry: Registry hostname (e.g., "ghcr.io")
        repo_name: Repository name
        version: Version tag
        commit_sha: Optional commit SHA for additional tag
        
    Returns:
        Dict with formatted registry tags
    """
    repo_lower = repo_name.lower()
    base_repo = registry + "/" + repo_lower
    
    tags = {
        "latest": base_repo + ":latest",
        "version": base_repo + ":" + version,
    }
    
    if commit_sha:
        tags["commit"] = base_repo + ":" + commit_sha
        
    return tags
