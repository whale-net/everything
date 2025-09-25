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
        "depends_on": ctx.attr.depends_on,
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
        "depends_on": attr.string_list(default = []),
        "hook_weight": attr.int(default = -5),
        "hook_delete_policy": attr.string(default = "before-hook-creation"),
    },
)

def helm_chart_composed(name, description, apps = [], k8s_artifacts = [], pre_deploy_jobs = [], chart_values = {}, deploy_order_weight = 0):
    """Compose a helm chart from release_apps and manual k8s artifacts.
    
    Args:
        name: Chart name
        description: Chart description  
        apps: List of release_app targets
        k8s_artifacts: List of k8s_artifact targets
        pre_deploy_jobs: List of job names that should run before deployment
        chart_values: Dictionary of chart-specific values
        deploy_order_weight: Weight for deployment ordering (lower weights deploy first)
    """
    
    _helm_chart_composed(
        name = name,
        description = description,
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
    
    # Create a data collection script that generates JSON for the Go renderer
    data_script = ctx.actions.declare_file(ctx.label.name + "_collect_data.py") 
    data_file = ctx.actions.declare_file(ctx.label.name + "_data.json")
    
    data_script_content = """#!/usr/bin/env python3
import json
import os
import sys

data_file = sys.argv[1]

# Chart metadata
chart_name = \"""" + ctx.attr.name + """\"
description = \"""" + ctx.attr.description + """\"
domain = \"""" + (ctx.attr.name.split("_")[0] if "_" in ctx.attr.name else "default") + """\"
deploy_order_weight = """ + str(ctx.attr.deploy_order_weight) + """

# Read app metadata files
app_metadata_files = """ + str([f.path for f in app_metadata_files]) + """
apps = []

for metadata_file in app_metadata_files:
    if os.path.exists(metadata_file):
        with open(metadata_file, 'r') as f:
            try:
                app_data = json.load(f)
                apps.append(app_data)
                print(f"Loaded app metadata: {app_data['name']}")
            except json.JSONDecodeError as e:
                print(f"Warning: Could not parse {metadata_file}: {e}")

# Read k8s artifact metadata
k8s_metadata_files = """ + str([f.path for f in k8s_artifact_metadata_files]) + """
artifacts = []

for metadata_file in k8s_metadata_files:
    if os.path.exists(metadata_file):
        with open(metadata_file, 'r') as f:
            try:
                artifact_data = json.load(f)
                artifacts.append(artifact_data)
                print(f"Loaded k8s artifact: {artifact_data['name']}")
            except json.JSONDecodeError as e:
                print(f"Warning: Could not parse {metadata_file}: {e}")

# Create chart data structure for Go renderer
chart_data = {
    "chart_name": chart_name,
    "description": description,
    "domain": domain,
    "deploy_order_weight": deploy_order_weight,
    "apps": apps,
    "artifacts": artifacts,
    "chart_values": """ + str(ctx.attr.chart_values) + """
}

# Write JSON data for Go renderer
with open(data_file, 'w') as f:
    json.dump(chart_data, f, indent=2)

print(f"Generated chart data for: {chart_name}")
print(f"  Apps: {len(apps)}")
print(f"  Artifacts: {len(artifacts)}")
"""
    
    ctx.actions.write(
        output = data_script,
        content = data_script_content,
    )
    
    # Run data collection script
    ctx.actions.run(
        executable = "python3",
        arguments = [data_script.path, data_file.path],
        inputs = [data_script] + app_metadata_files + k8s_artifact_metadata_files,
        outputs = [data_file],
    )
    
    # Run Go template renderer
    template_files = ctx.files.templates
    renderer_inputs = [data_file] + template_files
    
    ctx.actions.run(
        executable = ctx.executable.renderer,
        arguments = [
            ctx.files.templates[0].dirname,  # template directory
            chart_dir.path,                  # output directory
            data_file.path,                  # data file
        ],
        inputs = renderer_inputs,
        outputs = [chart_dir],
    )
    
    return [DefaultInfo(files = depset([chart_dir]))]

_helm_chart_composed = rule(
    implementation = _helm_chart_composed_impl,
    attrs = {
        "description": attr.string(mandatory = True),
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