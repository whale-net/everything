"""Release utilities for the Everything monorepo."""

def _app_metadata_impl(ctx):
    """Implementation for app_metadata rule."""
    # Create a JSON file with app metadata
    metadata = {
        "name": ctx.attr.name,
        "version": ctx.attr.version,
        "binary_target": ctx.attr.binary_target,
        "image_target": ctx.attr.image_target,
        "description": ctx.attr.description,
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
    },
)

def release_app(name, binary_target, description = "", version = "latest"):
    """Convenience macro to set up release metadata for an app.
    
    Args:
        name: App name (should match directory name)
        binary_target: The py_binary or go_binary target for this app
        description: Optional description of the app
        version: Default version (can be overridden at release time)
    """
    # Image target is derived from binary target name
    image_target = binary_target + "_image"
    
    app_metadata(
        name = name + "_metadata",
        binary_target = binary_target,
        image_target = image_target,
        description = description,
        version = version,
        visibility = ["//visibility:public"],
    )
