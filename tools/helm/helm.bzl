"""Helm chart generation rules for the Everything monorepo."""

def _helm_chart_impl(ctx):
    """Implementation for helm_chart rule.
    
    This rule collects app_metadata JSON files, runs helm_composer to generate
    a Helm chart, and outputs the chart directory as a tarball.
    """
    # Collect metadata files from app_metadata targets
    metadata_files = []
    for dep in ctx.attr.apps:
        files = dep[DefaultInfo].files.to_list()
        if len(files) != 1:
            fail("Expected exactly one metadata file from target %s, got %d" % (dep.label, len(files)))
        metadata_files.append(files[0])
    
    # Collect manual manifest files
    manifest_files = []
    for dep in ctx.attr.manual_manifests:
        files = dep[DefaultInfo].files.to_list()
        manifest_files.extend(files)
    
    # Declare output directory (we'll create a tarball of the chart)
    chart_tarball = ctx.actions.declare_file(ctx.attr.chart_name + ".tar.gz")
    
    # We also need an intermediate directory for the chart
    # The composer creates OutputDir/ChartName/, so we declare a parent directory
    chart_parent_dir = ctx.actions.declare_directory(ctx.attr.chart_name + "_chart")
    
    # Build the helm_composer command
    args = ctx.actions.args()
    
    # Add metadata file paths
    args.add("--metadata")
    args.add_joined(metadata_files, join_with = ",")
    
    # Add manual manifest file paths if any
    if manifest_files:
        args.add("--manifests")
        args.add_joined(manifest_files, join_with = ",")
    
    # Add chart configuration
    args.add("--chart-name", ctx.attr.chart_name)
    args.add("--version", ctx.attr.chart_version)
    args.add("--environment", ctx.attr.environment)
    args.add("--namespace", ctx.attr.namespace)
    # Pass the parent directory - composer will create ChartName/ inside it
    args.add("--output", chart_parent_dir.path)
    
    # Add template directory - get the directory of the first template file
    template_files = ctx.files._templates
    if not template_files:
        fail("No template files found")
    
    # Get the templates root directory (parent of base/)
    # Template files are in tools/helm/templates/base/*.tmpl and tools/helm/templates/*.tmpl
    # We need to find a file and get its grandparent directory
    first_template = template_files[0]
    # For tools/helm/templates/base/Chart.yaml.tmpl, dirname is tools/helm/templates/base
    # For tools/helm/templates/deployment.yaml.tmpl, dirname is tools/helm/templates
    # We need the common root: tools/helm/templates
    template_dir = first_template.dirname
    if template_dir.endswith("/base"):
        template_dir = template_dir[:-5]  # Remove "/base" suffix
    args.add("--template-dir", template_dir)
    
    # Run helm_composer to generate the chart
    ctx.actions.run(
        executable = ctx.executable._helm_composer,
        arguments = [args],
        inputs = metadata_files + template_files + manifest_files,
        outputs = [chart_parent_dir],
        mnemonic = "GenerateHelmChart",
        progress_message = "Generating Helm chart %s" % ctx.attr.chart_name,
    )
    
    # Create a tarball of the chart directory (which is inside chart_parent_dir)
    ctx.actions.run_shell(
        command = "tar -czf {} -C {} {}".format(
            chart_tarball.path,
            chart_parent_dir.path,
            ctx.attr.chart_name,
        ),
        inputs = [chart_parent_dir],
        outputs = [chart_tarball],
        mnemonic = "PackageHelmChart",
        progress_message = "Packaging Helm chart %s" % ctx.attr.chart_name,
    )
    
    return [
        DefaultInfo(
            files = depset([chart_tarball, chart_parent_dir]),
            runfiles = ctx.runfiles(files = [chart_tarball]),
        ),
    ]

helm_chart = rule(
    implementation = _helm_chart_impl,
    attrs = {
        "apps": attr.label_list(
            mandatory = True,
            doc = "List of app_metadata targets to include in the chart",
        ),
        "manual_manifests": attr.label_list(
            default = [],
            allow_files = [".yaml", ".yml"],
            doc = "List of k8s_manifests targets or direct YAML files to include in the chart",
        ),
        "chart_name": attr.string(
            mandatory = True,
            doc = "Name of the Helm chart",
        ),
        "chart_version": attr.string(
            default = "0.1.0",
            doc = "Version of the Helm chart",
        ),
        "environment": attr.string(
            default = "development",
            doc = "Target environment (development, staging, production)",
        ),
        "namespace": attr.string(
            mandatory = True,
            doc = "Kubernetes namespace for the chart",
        ),
        "_helm_composer": attr.label(
            default = Label("//tools/helm:helm_composer"),
            executable = True,
            cfg = "exec",
            doc = "The helm_composer binary",
        ),
        "_templates": attr.label(
            default = Label("//tools/helm:templates"),
            allow_files = True,
            doc = "Helm chart templates",
        ),
    },
    doc = """
    Generates a Helm chart from app_metadata targets.
    
    The chart will include all specified apps and automatically configure
    ingress for external APIs. Ingress hostname and other configurations
    should be set in the values.yaml by chart consumers.
    
    Optionally include manual Kubernetes manifests (ConfigMaps, Secrets,
    NetworkPolicies, etc.) via manual_manifests. These will be automatically
    processed to inject Helm templating for namespace and labels.
    
    Example:
        helm_chart(
            name = "my_app_chart",
            apps = ["//path/to:app_metadata"],
            chart_name = "my-app",
            namespace = "production",
        )
        
        # With manual manifests
        helm_chart(
            name = "my_app_chart_enhanced",
            apps = ["//path/to:app_metadata"],
            manual_manifests = [":k8s_manifests"],
            chart_name = "my-app",
            namespace = "production",
        )
    """,
)
