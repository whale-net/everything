"""
Simple Composable Helm Chart System for Everything Monorepo
"""

def _k8s_artifact_impl(ctx):
    """Implementation for k8s_artifact rule."""
    manifest_file = ctx.file.manifest
    
    metadata = {
        "name": ctx.attr.name,
        "type": ctx.attr.artifact_type,
        "manifest_path": manifest_file.path,
        "hook_weight": ctx.attr.hook_weight,
        "hook_delete_policy": ctx.attr.hook_delete_policy,
    }
    
    output = ctx.actions.declare_file(ctx.label.name + "_k8s_metadata.json")
    ctx.actions.write(
        output = output,
        content = json.encode(metadata),
    )
    
    return [DefaultInfo(files = depset([output, manifest_file]))]

k8s_artifact = rule(
    implementation = _k8s_artifact_impl,
    attrs = {
        "manifest": attr.label(allow_single_file = [".yaml", ".yml"], mandatory = True),
        "artifact_type": attr.string(mandatory = True),
        "hook_weight": attr.int(default = -5),
        "hook_delete_policy": attr.string(default = "before-hook-creation"),
    },
)

def helm_chart_composed(name, description, domain = None, apps = [], k8s_artifacts = [], pre_deploy_jobs = [], chart_values = {}, deploy_order_weight = 0):
    """Compose a helm chart from release_apps and manual k8s artifacts.
    
    Args:
        name: Chart name
        description: Chart description
        domain: Explicit domain name (if not provided, extracted from chart name)
        apps: List of release_app targets
        k8s_artifacts: List of k8s_artifact targets
        pre_deploy_jobs: List of job names that should run before deployment
        chart_values: Dictionary of chart-specific values
        deploy_order_weight: Weight for deployment ordering (lower weights deploy first)
    """
    
    _helm_chart_composed(
        name = name,
        description = description,
        domain = domain,
        apps = apps,
        k8s_artifacts = k8s_artifacts,
        pre_deploy_jobs = pre_deploy_jobs,
        chart_values = chart_values,
        deploy_order_weight = deploy_order_weight,
    )

def _helm_chart_composed_impl(ctx):
    """Implementation for helm_chart_composed rule."""
    
    # Collect metadata files from app targets
    app_metadata_files = []
    for app_target in ctx.attr.apps:
        for file in app_target.files.to_list():
            if file.basename.endswith("_metadata.json"):
                app_metadata_files.append(file)
    
    # Collect k8s artifact files
    k8s_artifact_files = []
    k8s_artifact_metadata_files = []
    for artifact_target in ctx.attr.k8s_artifacts:
        for file in artifact_target.files.to_list():
            if file.basename.endswith("_k8s_metadata.json"):
                k8s_artifact_metadata_files.append(file)
            elif file.path.endswith((".yaml", ".yml")):
                k8s_artifact_files.append(file)
    
    chart_dir = ctx.actions.declare_directory(ctx.label.name)
    
    # Build arguments for Go renderer - no Python needed!
    # Use explicit domain if provided, otherwise extract from chart name
    if ctx.attr.domain:
        domain = ctx.attr.domain
    else:
        domain = ctx.attr.name.split("_")[0] if "_" in ctx.attr.name else "default"
    
    args = [
        ctx.files.templates[0].dirname,  # template directory
        chart_dir.path,                  # output directory  
        ctx.attr.name,                   # chart name
        ctx.attr.description,            # description
        domain,                          # domain
    ]
    
    # Add app metadata files
    args.extend([f.path for f in app_metadata_files])
    
    # Add k8s artifacts if any
    if k8s_artifact_metadata_files:
        args.append("--k8s-artifacts")
        args.append(",".join([f.path for f in k8s_artifact_metadata_files]))
    
    # Add chart values if any
    if ctx.attr.chart_values:
        args.append("--chart-values")
        chart_value_pairs = [k + "=" + v for k, v in ctx.attr.chart_values.items()]
        args.append(",".join(chart_value_pairs))
    
    # Add deploy weight if set
    if ctx.attr.deploy_order_weight != 0:
        args.append("--deploy-weight")
        args.append(str(ctx.attr.deploy_order_weight))
    
    # Run Go template renderer directly - single tool, no Python!
    template_files = ctx.files.templates
    all_metadata_files = app_metadata_files + k8s_artifact_metadata_files + k8s_artifact_files
    
    ctx.actions.run(
        executable = ctx.executable.renderer,
        arguments = args,
        inputs = template_files + all_metadata_files,
        outputs = [chart_dir],
    )
    
    return [DefaultInfo(files = depset([chart_dir]))]

_helm_chart_composed = rule(
    implementation = _helm_chart_composed_impl,
    attrs = {
        "description": attr.string(mandatory = True),
        "domain": attr.string(),
        "apps": attr.label_list(providers = [DefaultInfo]),
        "k8s_artifacts": attr.label_list(providers = [DefaultInfo]),
        "pre_deploy_jobs": attr.string_list(default = []),
        "chart_values": attr.string_dict(default = {}),
        "deploy_order_weight": attr.int(default = 0),
        "renderer": attr.label(
            executable = True,
            cfg = "exec",
            default = "//tools/helm_renderer:helm_renderer",
        ),
        "templates": attr.label(
            allow_files = True,
            default = "//tools/templates/helm_composition:helm_composition_templates",
        ),
    },
)