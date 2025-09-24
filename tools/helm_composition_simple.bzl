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
    
    # Create an enhanced generation script that reads real metadata
    script = ctx.actions.declare_file(ctx.label.name + "_generate.py") 
    
    script_content = """#!/usr/bin/env python3
import json
import os
import sys

output_dir = sys.argv[1]
os.makedirs(output_dir, exist_ok=True)

# Chart metadata
chart_name = \"""" + ctx.attr.name + """\"
description = \"""" + ctx.attr.description + """\"
domain = \"""" + (ctx.attr.name.split("_")[0] if "_" in ctx.attr.name else "default") + """\"

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

# Create Chart.yaml
chart_yaml = f'''apiVersion: v2
name: {chart_name}
description: {description}
type: application
version: 1.0.0
appVersion: "latest"
keywords:
  - composed
  - microservices
  - bazel-generated
home: https://github.com/whale-net/everything
sources:
  - https://github.com/whale-net/everything
maintainers:
  - name: whale-net
    url: https://github.com/whale-net/everything
'''

with open(os.path.join(output_dir, "Chart.yaml"), "w") as f:
    f.write(chart_yaml)

# Create values.yaml with real app data
values_yaml = f'''# Generated composed helm chart values
# This chart combines release_apps with manual k8s artifacts

domain: {domain}

# Image configurations for each app (from release_app metadata)
images:
'''

for app in apps:
    app_name = app["name"]
    repo_name = app["repo_name"] 
    registry = app["registry"]
    version = app["version"]
    
    values_yaml += f'''  {app_name}:
    name: "{registry}/whale-net/{repo_name}"
    tag: "{version}"
    repository: "{registry}/whale-net/{repo_name}"
'''

values_yaml += f'''
# Application configurations (from release_app metadata)
{domain}:
  apps:
'''

for app in apps:
    app_name = app["name"]
    version = app["version"]
    
    values_yaml += f'''    {app_name}:
      enabled: true
      version: "{version}"
      replicas: 1
      port: 8000
      resources:
        requests:
          memory: "128Mi"
          cpu: "100m"
        limits:
          memory: "512Mi"
          cpu: "500m"
'''

# Add k8s artifacts section
if artifacts:
    values_yaml += '''
# Manual Kubernetes artifacts
artifacts:
'''
    for artifact in artifacts:
        artifact_name = artifact["name"]
        artifact_type = artifact["type"]
        values_yaml += f'''  {artifact_name}:
    type: {artifact_type}
    enabled: true
'''

# Add chart-specific values
chart_values = """ + str(ctx.attr.chart_values) + """
if chart_values:
    values_yaml += '''
# Chart-specific overrides
'''
    for key, value in chart_values.items():
        values_yaml += f'{key}: "{value}"\\n'

# Common configuration
values_yaml += f'''
# Common configuration
domain: "{domain}"

service:
  enabled: true
  type: ClusterIP

ingress:
  enabled: false
  host: "localhost"
  tls:
    enabled: false

env:
  app_env: "dev"
'''

with open(os.path.join(output_dir, "values.yaml"), "w") as f:
    f.write(values_yaml)

# Create templates directory
templates_dir = os.path.join(output_dir, "templates")
os.makedirs(templates_dir, exist_ok=True)

# Create deployment template for apps
deployment_template = f'''{{{{/*
Multi-app deployment template - Generated from release_apps
*/}}}}
{{{{- $domain := .Values.domain }}}}
{{{{- range $appName, $appConfig := index .Values $domain "apps" }}}}
{{{{- if $appConfig.enabled }}}}
{{{{- $appContext := dict "Values" $.Values "Chart" $.Chart "Release" $.Release "appName" $appName }}}}
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{{{ include "multiapp.fullname" $appContext }}}}
  labels:
    app: {{{{ $appName }}}}
    chart: {{{{ $.Chart.Name }}}}
    release: {{{{ $.Release.Name }}}}
    heritage: {{{{ $.Release.Service }}}}
spec:
  replicas: {{{{ $appConfig.replicas | default 1 }}}}
  selector:
    matchLabels:
      app: {{{{ $appName }}}}
      release: {{{{ $.Release.Name }}}}
  template:
    metadata:
      labels:
        app: {{{{ $appName }}}}
        release: {{{{ $.Release.Name }}}}
    spec:
      containers:
      - name: {{{{ $appName }}}}
        image: "{{{{ index $.Values.images $appName "repository" }}}}:{{{{ index $.Values.images $appName "tag" }}}}"
        ports:
        - containerPort: {{{{ $appConfig.port | default 8000 }}}}
        resources:
          {{{{- toYaml $appConfig.resources | nindent 10 }}}}
        env:
        - name: APP_NAME
          value: {{{{ $appName }}}}
        {{{{- if $.Values.env }}}}
        {{{{- range $key, $value := $.Values.env }}}}
        - name: {{{{ $key | upper }}}}
          value: "{{{{ $value }}}}"
        {{{{- end }}}}
        {{{{- end }}}}
---
apiVersion: v1
kind: Service
metadata:
  name: {{{{ $appName }}}}-service
  labels:
    app: {{{{ $appName }}}}
    chart: {{{{ $.Chart.Name }}}}
    release: {{{{ $.Release.Name }}}}
spec:
  type: {{{{ $.Values.service.type }}}}
  ports:
  - port: {{{{ $appConfig.port | default 8000 }}}}
    targetPort: {{{{ $appConfig.port | default 8000 }}}}
    protocol: TCP
    name: http
  selector:
    app: {{{{ $appName }}}}
    release: {{{{ $.Release.Name }}}}
{{{{- end }}}}
{{{{- end }}}}
'''

with open(os.path.join(templates_dir, "deployments.yaml"), "w") as f:
    f.write(deployment_template)

# Create helper template
helpers_template = '''{{{{/*
Expand the name of the chart.
*/}}}}
{{{{- define "multiapp.name" -}}}}
{{{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}}}
{{{{- end }}}}

{{{{/*
Create a default fully qualified app name.
*/}}}}
{{{{- define "multiapp.fullname" -}}}}
{{{{- if .Values.fullnameOverride }}}}
{{{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}}}
{{{{- else }}}}
{{{{- $name := default .Chart.Name .Values.nameOverride }}}}
{{{{- if contains $name .Release.Name }}}}
{{{{- printf "%s-%s" .Release.Name .appName | trunc 63 | trimSuffix "-" }}}}
{{{{- else }}}}
{{{{- printf "%s-%s-%s" .Release.Name $name .appName | trunc 63 | trimSuffix "-" }}}}
{{{{- end }}}}
{{{{- end }}}}
{{{{- end }}}}

{{{{/*
Create chart name and version as used by the chart label.
*/}}}}
{{{{- define "multiapp.chart" -}}}}
{{{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}}}
{{{{- end }}}}
'''

with open(os.path.join(templates_dir, "_helpers.tpl"), "w") as f:
    f.write(helpers_template)

print(f"Generated composed helm chart: {chart_name}")
print(f"  Apps: {len(apps)} apps loaded from metadata")
print(f"  Artifacts: {len(artifacts)} k8s artifacts")
if apps:
    print("  App details:")
    for app in apps:
        print(f"    - {app['name']}: {app['registry']}/whale-net/{app['repo_name']}:{app['version']}")
"""
    
    ctx.actions.write(
        output = script,
        content = script_content,
    )
    
    # Run the generation with all input files
    all_inputs = [script] + app_metadata_files + k8s_artifact_metadata_files + k8s_artifact_files
    
    ctx.actions.run(
        executable = "python3",
        arguments = [script.path, chart_dir.path],
        inputs = all_inputs,
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