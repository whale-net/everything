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
    },
)

def helm_chart_composed(name, description, apps = [], k8s_artifacts = [], pre_deploy_jobs = [], chart_values = {}, depends_on_charts = []):
    """Compose a helm chart from release_apps and manual k8s artifacts."""
    
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
    
    chart_dir = ctx.actions.declare_directory(ctx.label.name)
    
    # Create a simple generation script
    script = ctx.actions.declare_file(ctx.label.name + "_generate.py") 
    
    script_content = """#!/usr/bin/env python3
import json
import os
import sys

output_dir = sys.argv[1]
os.makedirs(output_dir, exist_ok=True)

# Create Chart.yaml
chart_yaml = '''apiVersion: v2
name: """ + ctx.attr.name + """
description: """ + ctx.attr.description + """
type: application
version: 1.0.0
appVersion: "latest"
keywords:
  - composed
  - microservices
'''

with open(os.path.join(output_dir, "Chart.yaml"), "w") as f:
    f.write(chart_yaml)

# Create values.yaml
values_yaml = '''# Generated composed helm chart
domain: """ + (ctx.attr.name.split("_")[0] if "_" in ctx.attr.name else "default") + """

# Apps from this composition
apps:
  # Placeholder - will be enhanced with real app metadata
  placeholder:
    enabled: true
    image:
      repository: ghcr.io/whale-net/placeholder
      tag: latest
'''

with open(os.path.join(output_dir, "values.yaml"), "w") as f:
    f.write(values_yaml)

# Create templates directory
templates_dir = os.path.join(output_dir, "templates")
os.makedirs(templates_dir, exist_ok=True)

# Create a basic template
template_content = '''# Generated template for """ + ctx.attr.name + """
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Release.Name }}-config
  labels:
    app.kubernetes.io/name: {{ .Chart.Name }}
    helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
    app.kubernetes.io/instance: {{ .Release.Name }}
data:
  chart-name: "{{ .Chart.Name }}"
  release-name: "{{ .Release.Name }}"
  composition-type: "bazel-generated"
'''

with open(os.path.join(templates_dir, "configmap.yaml"), "w") as f:
    f.write(template_content)

print("Generated composed helm chart: """ + ctx.attr.name + """")
print("  Apps: """ + str(len(ctx.attr.apps)) + """")
print("  Artifacts: """ + str(len(ctx.attr.k8s_artifacts)) + """")
"""
    
    ctx.actions.write(
        output = script,
        content = script_content,
    )
    
    # Run the generation
    ctx.actions.run(
        executable = "python3",
        arguments = [script.path, chart_dir.path],
        inputs = [script],
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