"""
Helm-native chart generation using library chart composition.

This approach eliminates the complex Go renderer and uses Helm's built-in
composition patterns with library chart dependencies.
"""

def _helm_chart_native_impl(ctx):
    """Implementation for helm_chart_native rule."""
    
    chart_name = ctx.attr.name
    domain = ctx.attr.domain or ctx.attr.name.split("_")[0]
    
    # Collect app metadata
    apps_data = []
    for app_target in ctx.attr.apps:
        for file in app_target.files.to_list():
            if file.basename.endswith("_metadata.json"):
                apps_data.append(file)
    
    # Create output directory - the generator will create the full structure
    chart_dir = ctx.actions.declare_directory(chart_name)
    
    # Build arguments for Go chart generator
    args = [
        "--chart-name", chart_name,
        "--description", ctx.attr.description,
        "--domain", domain,
        "--version", ctx.attr.chart_version,
        "--output-dir", chart_dir.path,
        "--template-dir", ctx.files.templates[0].dirname,
    ]
    
    # Add app metadata files
    app_metadata_str = ",".join([f.path for f in apps_data])
    if app_metadata_str:
        args.extend(["--app-metadata", app_metadata_str])
    
    # Add custom values
    if ctx.attr.values:
        custom_values_list = ["{}={}".format(k, v) for k, v in ctx.attr.values.items()]
        custom_values_str = ",".join(custom_values_list)
        args.extend(["--custom-values", custom_values_str])
    
    # Add job configurations
    if ctx.attr.jobs:
        jobs_str = ",".join(ctx.attr.jobs)
        args.extend(["--jobs", jobs_str])
    
    # Run Go chart generator
    ctx.actions.run(
        executable = ctx.executable._chart_generator,
        arguments = args,
        inputs = apps_data + ctx.files.templates,
        outputs = [chart_dir],
    )
    
    return [DefaultInfo(files = depset([chart_dir]))]

# All template generation is now handled by the Go chart generator

helm_chart_native = rule(
    implementation = _helm_chart_native_impl,
    attrs = {
        "domain": attr.string(
            doc = "Domain name for the chart (defaults to first part of chart name)"
        ),
        "description": attr.string(
            mandatory = True,
            doc = "Chart description"
        ),
        "apps": attr.label_list(
            providers = [DefaultInfo],
            doc = "List of release_app targets to include"
        ),
        "values": attr.string_dict(
            default = {},
            doc = "Additional values to include in values.yaml"
        ),
        "jobs": attr.string_list(
            default = [],
            doc = "List of job configurations in 'name:key=val,key=val' format"
        ),
        "chart_version": attr.string(
            default = "1.0.0",
            doc = "Helm chart version"
        ),
        "_chart_generator": attr.label(
            executable = True,
            cfg = "exec",
            default = "//tools/chart_generator:chart_generator",
        ),
        "templates": attr.label(
            allow_files = True,
            default = "//tools/templates/helm_native:helm_native_templates",
        ),
    },
    doc = """
    Generate a Helm chart using whale-net library chart composition.
    
    This rule creates a proper Helm chart that depends on the whale-net-library
    chart and uses its reusable templates. No custom Go renderer needed!
    
    Example:
        helm_chart_native(
            name = "manman_services",
            description = "ManMan services chart",
            domain = "manman",
            apps = [
                ":experience_api_metadata",
                ":status_api_metadata",
            ],
            values = {
                "ingress.enabled": "true",
                "service.type": "ClusterIP",
            },
            jobs = [{
                "name": "migrations",
                "image.repository": "ghcr.io/whale-net/manman-api",
                "command": ["python", "migrate.py"],
            }],
        )
    """
)

def helm_chart_native_macro(name, description, domain = None, apps = [], values = {}, jobs = [], chart_version = "1.0.0"):
    """Convenience macro for helm_chart_native rule."""
    
    # Jobs should be passed as-is to the rule (Starlark will handle conversion)
    helm_chart_native(
        name = name,
        description = description,
        domain = domain,
        apps = apps,
        values = values,
        jobs = jobs,
        chart_version = chart_version,
    )