"""
Composable Helm Chart System for Everything Monorepo

This provides a flexible way to compose helm charts from release_apps and manual k8s artifacts.
Supports the pattern: helm_chart = helm_macro(list[release_app] | list[manual_k8s_artifact])
"""

def _k8s_artifact_impl(ctx):
    """Implementation for k8s_artifact rule."""
    # Read the manifest file and create metadata
    manifest_file = ctx.file.manifest
    
    metadata = {
        "name": ctx.attr.name,
        "type": ctx.attr.artifact_type,
        "manifest_path": manifest_file.path,
        "depends_on": ctx.attr.depends_on,
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
        "artifact_type": attr.string(mandatory = True),  # job, configmap, secret, etc.
        "depends_on": attr.string_list(default = []),
    },
)

def helm_chart_composed(name, description, apps = [], k8s_artifacts = [], pre_deploy_jobs = [], chart_values = {}, depends_on_charts = []):
    """
    Compose a helm chart from release_apps and manual k8s artifacts.
    
    Args:
        name: Chart name
        description: Chart description
        apps: List of release_app targets (e.g., [":experience_api", ":status_api"])
        k8s_artifacts: List of k8s_artifact targets (e.g., [":migrations_job"])
        pre_deploy_jobs: List of jobs that must run before deployments
        chart_values: Additional helm values to merge
        depends_on_charts: Other charts this depends on
    """
    
    # Create a custom helm chart rule that combines everything
    _helm_chart_composed(
        name = name,
        description = description,
        apps = apps,
        k8s_artifacts = k8s_artifacts,
        pre_deploy_jobs = pre_deploy_jobs,
        chart_values = chart_values,
        depends_on_charts = depends_on_charts,
    )

def _helm_chart_composed_impl(ctx):
    """Implementation for helm_chart_composed rule."""
    
    # Collect metadata from all apps and artifacts
    app_metadata = []
    for app_target in ctx.attr.apps:
        # Read the app metadata
        metadata_files = app_target.files.to_list()
        for f in metadata_files:
            if f.basename.endswith("_metadata.json"):
                app_metadata.append(f)
    
    artifact_metadata = []
    for artifact_target in ctx.attr.k8s_artifacts:
        metadata_files = artifact_target.files.to_list()
        for f in metadata_files:
            if f.basename.endswith("_k8s_metadata.json"):
                artifact_metadata.append(f)
    
    # Generate the helm chart using a template system
    chart_dir = ctx.actions.declare_directory(ctx.label.name)
    
    # Create the generation script
    script = ctx.actions.declare_file(ctx.label.name + "_generate.py")
    ctx.actions.write(
        output = script,
        content = """
import json
import os
import sys

# Read app metadata
apps = []
for metadata_file in {app_metadata}:
    with open(metadata_file, 'r') as f:
        apps.append(json.load(f))

# Read k8s artifact metadata  
artifacts = []
for metadata_file in {artifact_metadata}:
    with open(metadata_file, 'r') as f:
        artifacts.append(json.load(f))

# Generate helm chart files
chart_name = "{chart_name}"
description = "{description}"
chart_values = {chart_values}
pre_deploy_jobs = {pre_deploy_jobs}

# Create Chart.yaml
chart_yaml = f'''
apiVersion: v2
name: {{chart_name}}
description: {{description}}
type: application
version: 1.0.0
appVersion: "latest"
'''

# Create values.yaml with apps and custom values
values_yaml = f'''
# Generated composed helm chart
apps: {{json.dumps(apps, indent=2)}}
artifacts: {{json.dumps(artifacts, indent=2)}}
'''

# TODO: Generate deployment templates based on app metadata and artifacts

print("Generated helm chart:", chart_name)
        """.format(
            app_metadata = [f.path for f in app_metadata],
            artifact_metadata = [f.path for f in artifact_metadata], 
            chart_name = ctx.attr.name,
            description = ctx.attr.description,
            chart_values = json.encode(ctx.attr.chart_values),
            pre_deploy_jobs = json.encode(ctx.attr.pre_deploy_jobs),
        )
    )
    
    # Run the generation
    ctx.actions.run(
        executable = "python3",
        arguments = [script.path, chart_dir.path],
        inputs = app_metadata + artifact_metadata + [script],
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
        "depends_on_charts": attr.string_list(default = []),
    },
)