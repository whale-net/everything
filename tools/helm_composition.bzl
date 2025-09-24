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
        content = """#!/usr/bin/env python3
import json
import os
import sys

output_dir = sys.argv[1]
os.makedirs(output_dir, exist_ok=True)

# Chart metadata
chart_name = "{chart_name}"
description = "{description}"

# Create Chart.yaml
chart_yaml_content = f'''apiVersion: v2
name: {{chart_name}}
description: {{description}}
type: application
version: 1.0.0
appVersion: "latest"
keywords:
  - composed
  - microservices
  - bazel-generated
'''

with open(os.path.join(output_dir, "Chart.yaml"), "w") as f:
    f.write(chart_yaml_content)

# Read app metadata
app_metadata_files = {app_metadata_paths}
apps = []
for metadata_file in app_metadata_files:
    if os.path.exists(metadata_file):
        with open(metadata_file, 'r') as f:
            apps.append(json.load(f))

# Read k8s artifact metadata  
artifact_metadata_files = {artifact_metadata_paths}
artifacts = []
for metadata_file in artifact_metadata_files:
    if os.path.exists(metadata_file):
        with open(metadata_file, 'r') as f:
            artifacts.append(json.load(f))

# Create values.yaml
values_content = f'''# Generated composed helm chart values
# This chart combines release_apps with manual k8s artifacts

domain: "{{domain}}"

# Application configurations from release_apps
apps:
'''

for app in apps:
    values_content += f'''  {{app["name"]}}:
    enabled: true
    image:
      repository: {{app["registry"]}}/{{app["repo_name"]}}
      tag: {{app["version"]}}
    replicas: 1
    resources:
      requests:
        memory: "128Mi"
        cpu: "100m"
      limits:
        memory: "512Mi" 
        cpu: "500m"
'''

values_content += '''
# Manual k8s artifacts
artifacts:
'''

for artifact in artifacts:
    values_content += f'''  {{artifact["name"]}}:
    type: {{artifact["type"]}}
    enabled: true
'''

# Add chart-specific values
chart_values = {chart_values}
if chart_values:
    values_content += '''
# Chart-specific overrides
'''
    for key, value in chart_values.items():
        values_content += f"{{key}}: \\"{{value}}\\"\\n"

with open(os.path.join(output_dir, "values.yaml"), "w") as f:
    f.write(values_content)

# Create templates directory
templates_dir = os.path.join(output_dir, "templates")
os.makedirs(templates_dir, exist_ok=True)

# Create a basic deployment template that references the apps
deployment_template = '''{{/*
Composed deployment template for multiple apps
Generated from release_apps and k8s artifacts
*/}}
{{- range $appName, $appConfig := .Values.apps }}
{{- if $appConfig.enabled }}
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ $appName }}
  labels:
    app: {{ $appName }}
    chart: {{ $.Chart.Name }}
    release: {{ $.Release.Name }}
spec:
  replicas: {{ $appConfig.replicas | default 1 }}
  selector:
    matchLabels:
      app: {{ $appName }}
      release: {{ $.Release.Name }}
  template:
    metadata:
      labels:
        app: {{ $appName }}
        release: {{ $.Release.Name }}
    spec:
      containers:
      - name: {{ $appName }}
        image: "{{ $appConfig.image.repository }}:{{ $appConfig.image.tag }}"
        ports:
        - containerPort: 8000
        resources:
          {{- toYaml $appConfig.resources | nindent 10 }}
        env:
        - name: APP_NAME
          value: {{ $appName }}
---
apiVersion: v1
kind: Service
metadata:
  name: {{ $appName }}-service
  labels:
    app: {{ $appName }}
    chart: {{ $.Chart.Name }}
    release: {{ $.Release.Name }}
spec:
  type: ClusterIP
  ports:
  - port: 8000
    targetPort: 8000
    protocol: TCP
  selector:
    app: {{ $appName }}
    release: {{ $.Release.Name }}
{{- end }}
{{- end }}
'''

with open(os.path.join(templates_dir, "deployments.yaml"), "w") as f:
    f.write(deployment_template)

print(f"Generated composed helm chart: {{chart_name}}")
print(f"  Apps: {{[app['name'] for app in apps]}}")
print(f"  Artifacts: {{[artifact['name'] for artifact in artifacts]}}")
        """.format(
            chart_name = ctx.attr.name,
            description = ctx.attr.description,
            domain = ctx.attr.name.split("_")[0] if "_" in ctx.attr.name else "default",
            app_metadata_paths = json.encode([f.path for f in app_metadata]),
            artifact_metadata_paths = json.encode([f.path for f in artifact_metadata]),
            chart_values = ctx.attr.chart_values,
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