"""Helm chart release system for multi-app deployments using templates."""

def _render_template_impl(ctx):
    """Helper action to render a template using the template_renderer."""
    
    # Prepare the context as JSON
    context_dict = {
        "chart_name": ctx.attr.chart_name,
        "domain": ctx.attr.domain,
        "chart_version": ctx.attr.chart_version,
        "apps": ctx.attr.apps,
        "description": ctx.attr.description,
        "overrides": ctx.attr.overrides,
    }
    
    # Convert to JSON string for passing to the renderer
    context_json = json.encode(context_dict)
    
    # Run the template renderer
    ctx.actions.run(
        executable = ctx.executable._template_renderer,
        arguments = [
            "--template", ctx.file.template.path,
            "--context", context_json,
            "--output", ctx.outputs.output.path,
            "--type", ctx.attr.template_type,
        ],
        inputs = [ctx.file.template],
        outputs = [ctx.outputs.output],
        tools = [ctx.executable._template_renderer],
        mnemonic = "RenderHelmTemplate",
    )

_render_template = rule(
    implementation = _render_template_impl,
    attrs = {
        "template": attr.label(
            allow_single_file = True,
            mandatory = True,
        ),
        "chart_name": attr.string(mandatory = True),
        "domain": attr.string(mandatory = True),
        "chart_version": attr.string(mandatory = True),
        "apps": attr.string_list(mandatory = True),
        "description": attr.string(mandatory = True),
        "overrides": attr.string_dict(default = {}),
        "template_type": attr.string(mandatory = True),
        "_template_renderer": attr.label(
            default = Label("//tools/helm:template_renderer"),
            executable = True,
            cfg = "exec",
        ),
    },
    outputs = {
        "output": "%{name}.out",
    },
)

def helm_chart_release_impl(ctx):
    """Implementation for helm_chart_release rule using templates."""
    
    # Generate chart name using domain+name convention
    chart_name = "{}-{}".format(ctx.attr.domain, ctx.attr.name)
    
    # Prepare common template context
    app_list = ", ".join(ctx.attr.apps)
    chart_description = ctx.attr.description or "Multi-app Helm chart for {domain} domain - includes {app_list}".format(
        domain = ctx.attr.domain, app_list = app_list
    )
    
    # Declare output files
    chart_yaml = ctx.actions.declare_file("{}/Chart.yaml".format(chart_name))
    values_yaml = ctx.actions.declare_file("{}/values.yaml".format(chart_name))
    
    # Render Chart.yaml using template
    chart_context = json.encode({
        "chart_name": chart_name,
        "domain": ctx.attr.domain,
        "chart_version": ctx.attr.chart_version,
        "description": chart_description,
    })
    
    ctx.actions.run(
        executable = ctx.executable._template_renderer,
        arguments = [
            "--template", ctx.file._chart_template.path,
            "--context", chart_context,
            "--output", chart_yaml.path,
            "--type", "chart",
        ],
        inputs = [ctx.file._chart_template],
        outputs = [chart_yaml],
        tools = [ctx.executable._template_renderer],
        mnemonic = "RenderChartYaml",
    )
    
    # Render values.yaml using template
    values_context = json.encode({
        "domain": ctx.attr.domain,
        "apps": ctx.attr.apps,
        "overrides": ctx.attr.values_overrides,
    })
    
    ctx.actions.run(
        executable = ctx.executable._template_renderer,
        arguments = [
            "--template", ctx.file._values_template.path,
            "--context", values_context,
            "--output", values_yaml.path,
            "--type", "values",
        ],
        inputs = [ctx.file._values_template],
        outputs = [values_yaml],
        tools = [ctx.executable._template_renderer],
        mnemonic = "RenderValuesYaml",
    )
    
    # Create a simple manifest file
    manifest_file = ctx.actions.declare_file("{}.manifest".format(chart_name))
    
    metadata_count = len(ctx.files.app_metadata_deps) if hasattr(ctx.files, 'app_metadata_deps') else 0
    
    manifest_content = """# Helm Chart Manifest for {chart_name}
Chart: {chart_name}
Domain: {domain}
Version: {version}
Apps: {apps}

Files generated:
- Chart.yaml
- values.yaml

Metadata files processed: {metadata_count}

Build success!
""".format(
        chart_name = chart_name,
        domain = ctx.attr.domain,
        version = ctx.attr.chart_version,
        apps = ", ".join(ctx.attr.apps),
        metadata_count = metadata_count
    )
    
    ctx.actions.write(
        output = manifest_file,
        content = manifest_content
    )
    
    return [DefaultInfo(
        files = depset([manifest_file, chart_yaml, values_yaml]),
        runfiles = ctx.runfiles(files = [manifest_file, chart_yaml, values_yaml])
    )]

helm_chart_release = rule(
    implementation = helm_chart_release_impl,
    attrs = {
        "domain": attr.string(
            mandatory = True,
            doc = "Domain name for the chart (e.g., 'manman', 'demo')"
        ),
        "apps": attr.string_list(
            mandatory = True,
            doc = "List of app names to include in the chart"
        ),
        "description": attr.string(
            doc = "Chart description"
        ),
        "chart_version": attr.string(
            default = "1.0.0",
            doc = "Helm chart version"
        ),
        "values_overrides": attr.string_dict(
            default = {},
            doc = "Dictionary of values to override in the generated values.yaml"
        ),
        "app_metadata_deps": attr.label_list(
            allow_files = True,
            doc = "App metadata files for version resolution (auto-populated)"
        ),
        "_template_renderer": attr.label(
            default = Label("//tools/helm:template_renderer"),
            executable = True,
            cfg = "exec",
        ),
        "_chart_template": attr.label(
            default = Label("//tools/helm:templates/Chart.yaml.template"),
            allow_single_file = True,
        ),
        "_values_template": attr.label(
            default = Label("//tools/helm:templates/values.yaml.template"),
            allow_single_file = True,
        ),
    },
    doc = """
    Create a Helm chart release with multi-app version management using templates.
    
    This rule integrates with the existing release_app pattern to automatically
    resolve app versions and generate Helm charts with proper image references.
    
    Example:
        helm_chart_release(
            name = "host_chart",
            domain = "manman", 
            apps = ["experience_api", "status_api"],
            description = "ManMan host services",
            chart_version = "1.0.0",
        )
    """
)

def helm_chart_release_macro(domain, charts):
    """Convenience macro for defining multiple charts in a domain."""
    
    for chart_name, chart_config in charts.items():
        apps = chart_config["apps"]
        description = chart_config.get("description", "")
        version = chart_config.get("version", "1.0.0")
        custom_values = chart_config.get("custom_values", {})
        
        # Generate metadata dependency labels
        metadata_deps = []
        for app_name in apps:
            metadata_target = "//{domain}:{app}_metadata".format(domain = domain, app = app_name)
            metadata_deps.append(metadata_target)
        
        # Call the actual rule with metadata dependencies
        helm_chart_release(
            name = chart_name,
            domain = domain,
            apps = apps,
            description = description,
            chart_version = version,
            values_overrides = custom_values,
            app_metadata_deps = metadata_deps,
        )