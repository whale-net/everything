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

def helm_chart_composed(name, description, apps = [], k8s_artifacts = [], pre_deploy_jobs = [], chart_values = {}, depends_on_charts = [], deploy_order_weight = 0):
    """Compose a helm chart from release_apps and manual k8s artifacts.
    
    Args:
        name: Chart name
        description: Chart description  
        apps: List of release_app targets
        k8s_artifacts: List of k8s_artifact targets
        pre_deploy_jobs: List of job names that should run before deployment
        chart_values: Dictionary of chart-specific values
        depends_on_charts: List of chart dependencies (supports name, name@version, name@version:repository formats)
        deploy_order_weight: Weight for deployment ordering (lower weights deploy first)
    """
    
    _helm_chart_composed(
        name = name,
        description = description,
        apps = apps,
        k8s_artifacts = k8s_artifacts,
        pre_deploy_jobs = pre_deploy_jobs,
        chart_values = chart_values,
        depends_on_charts = depends_on_charts,
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
depends_on_charts = """ + str(ctx.attr.depends_on_charts) + """
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

# Create Chart.yaml with dependencies
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
annotations:
  # Deployment ordering metadata
  "composition.whale-net.io/deploy-weight": "{deploy_order_weight}"
  "composition.whale-net.io/domain": "{domain}"
  "composition.whale-net.io/generated-by": "helm_composition_simple.bzl"
'''

# Add dependencies if specified
if depends_on_charts:
    chart_yaml += 'dependencies:\\n'
    for dependency in depends_on_charts:
        # Parse dependency string - supports multiple formats:
        # - name (uses default version and file path)
        # - name@version (uses file path)  
        # - name@version:repository (full specification)
        # - name@version:repository?condition (conditional dependency)
        
        condition = None
        if '?' in dependency:
            dependency, condition = dependency.split('?', 1)
            
        dep_parts = dependency.split('@')
        dep_name = dep_parts[0]
        dep_version = dep_parts[1] if len(dep_parts) > 1 else "~1.0.0"
        
        # Check if repository is specified (name@version:repo format)
        if ':' in dep_version:
            repo_parts = dep_version.split(':', 1)
            dep_version = repo_parts[0]
            dep_repository = repo_parts[1]
        else:
            dep_repository = f"file://../{dep_name}"
        
        chart_yaml += f'''  - name: {dep_name}
    version: "{dep_version}"
    repository: "{dep_repository}"'''
        
        if condition:
            chart_yaml += f'''
    condition: {condition}'''
        chart_yaml += '''
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

# Create migration job templates if we have job artifacts
migration_jobs = [artifact for artifact in artifacts if artifact["type"] == "job"]
if migration_jobs:
    # Sort jobs by hook weight for proper execution order
    migration_jobs.sort(key=lambda x: x.get("hook_weight", -5))
    
    migration_template = f'''{{{{/*
Migration jobs template - Generated from k8s artifacts
Sorted by hook weight for proper execution order
*/}}}}
{{{{- range $artifactName, $artifactConfig := .Values.artifacts }}}}
{{{{- if and $artifactConfig.enabled (eq $artifactConfig.type "job") }}}}
---
apiVersion: batch/v1
kind: Job
metadata:
  name: {{{{ $.Release.Name }}}}-{{{{ $artifactName }}}}
  labels:
    app: {{{{ $artifactName }}}}
    chart: {{{{ $.Chart.Name }}}}
    release: {{{{ $.Release.Name }}}}
    heritage: {{{{ $.Release.Service }}}}
  annotations:
    "helm.sh/hook": pre-install,pre-upgrade
    {{{{- if eq $artifactName "migrations_job" }}}}
    "helm.sh/hook-weight": "-10"
    {{{{- else if eq $artifactName "config_setup_job" }}}}
    "helm.sh/hook-weight": "-8"
    {{{{- else if eq $artifactName "schema_setup_job" }}}}
    "helm.sh/hook-weight": "-9"
    {{{{- else }}}}
    "helm.sh/hook-weight": "-5"
    {{{{- end }}}}
    "helm.sh/hook-delete-policy": before-hook-creation
spec:
  template:
    metadata:
      name: {{{{ $.Release.Name }}}}-{{{{ $artifactName }}}}
      labels:
        app: {{{{ $artifactName }}}}
        release: {{{{ $.Release.Name }}}}
    spec:
      containers:
      - name: {{{{ $artifactName }}}}
        {{{{- if eq $artifactName "migrations_job" }}}}
        # Use the first app's image for migrations (typically contains the migration code)
        {{{{- $firstApp := "" }}}}
        {{{{- range $appName, $appConfig := index $.Values $domain "apps" }}}}
        {{{{- if not $firstApp }}}}{{{{ $firstApp = $appName }}}}{{{{- end }}}}
        {{{{- end }}}}
        image: "{{{{ index $.Values.images $firstApp "repository" }}}}:{{{{ index $.Values.images $firstApp "tag" }}}}"
        command: ["python", "-m", "alembic", "upgrade", "head"]
        {{{{- else }}}}
        # Generic job configuration - customize based on artifact name
        image: "{{{{ index $.Values.images (keys $.Values.images | first) "repository" }}}}:{{{{ index $.Values.images (keys $.Values.images | first) "tag" }}}}"
        command: ["echo", "Job {{{{ $artifactName }}}} executed"]
        {{{{- end }}}}
        env:
        {{{{- if eq $artifactName "migrations_job" }}}}
        - name: DATABASE_URL
          valueFrom:
            secretKeyRef:
              name: {{{{ $.Release.Name }}}}-database-secret
              key: url
        - name: PYTHONPATH
          value: "/app"
        {{{{- end }}}}
        - name: JOB_NAME
          value: {{{{ $artifactName }}}}
        {{{{- if $.Values.env }}}}
        {{{{- range $key, $value := $.Values.env }}}}
        - name: {{{{ $key | upper }}}}
          value: "{{{{ $value }}}}"
        {{{{- end }}}}
        {{{{- end }}}}
        resources:
          requests:
            memory: "256Mi"
            cpu: "200m"
          limits:
            memory: "512Mi"
            cpu: "500m"
      restartPolicy: OnFailure
      backoffLimit: 3
{{{{- end }}}}
{{{{- end }}}}
'''
    
    with open(os.path.join(templates_dir, "jobs.yaml"), "w") as f:
        f.write(migration_template)

# Create ConfigMap templates for configmap artifacts
configmap_artifacts = [artifact for artifact in artifacts if artifact["type"] == "configmap"]
if configmap_artifacts:
    configmap_template = f'''{{{{/*
ConfigMaps template - Generated from k8s artifacts
*/}}}}
{{{{- range $artifactName, $artifactConfig := .Values.artifacts }}}}
{{{{- if and $artifactConfig.enabled (eq $artifactConfig.type "configmap") }}}}
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{{{ $.Release.Name }}}}-{{{{ $artifactName }}}}
  labels:
    app: {{{{ $artifactName }}}}
    chart: {{{{ $.Chart.Name }}}}
    release: {{{{ $.Release.Name }}}}
    heritage: {{{{ $.Release.Service }}}}
data:
  {{{{- if eq $artifactName "config" }}}}
  # Default configuration for the application
  app_env: {{{{ $.Values.env.app_env | default "dev" | quote }}}}
  log_level: "INFO"
  database_pool_size: "10"
  {{{{- if $.Values.env }}}}
  {{{{- range $key, $value := $.Values.env }}}}
  {{{{ $key }}: {{{{ $value | quote }}}}
  {{{{- end }}}}
  {{{{- end }}}}
  {{{{- else }}}}
  # Generic configmap - customize based on artifact name
  artifact_name: {{{{ $artifactName | quote }}}}
  chart_name: {{{{ $.Chart.Name | quote }}}}
  release_name: {{{{ $.Release.Name | quote }}}}
  {{{{- end }}}}
{{{{- end }}}}
{{{{- end }}}}
'''
    
    with open(os.path.join(templates_dir, "configmaps.yaml"), "w") as f:
        f.write(configmap_template)

# Create a notes template for deployment instructions
notes_template = f'''{{{{/*
NOTES template - Provides post-deployment instructions
*/}}}}
1. Get the application URLs by running these commands:
{{{{- if .Values.ingress.enabled }}}}
  http://{{{{ .Values.ingress.host }}}}
{{{{- else if contains "NodePort" .Values.service.type }}}}
  export NODE_PORT=$(kubectl get --namespace {{{{ .Release.Namespace }}}} -o jsonpath="{{.spec.ports[0].nodePort}}" services {{{{ include "multiapp.fullname" . }}}})
  export NODE_IP=$(kubectl get nodes --namespace {{{{ .Release.Namespace }}}} -o jsonpath="{{.items[0].status.addresses[0].address}}")
  echo http://$NODE_IP:$NODE_PORT
{{{{- else if contains "LoadBalancer" .Values.service.type }}}}
     NOTE: It may take a few minutes for the LoadBalancer IP to be available.
           You can watch the status by running 'kubectl get --namespace {{{{ .Release.Namespace }}}} svc -w {{{{ include "multiapp.fullname" . }}}}'
  export SERVICE_IP=$(kubectl get svc --namespace {{{{ .Release.Namespace }}}} {{{{ include "multiapp.fullname" . }}}} --template "{{"{{"}}.status.loadBalancer.ingress[0].ip{{"}}"}}")
  echo http://$SERVICE_IP:{{{{ .Values.service.port }}}}
{{{{- else if contains "ClusterIP" .Values.service.type }}}}
  # Port forward to access services locally
{{{{- $domain := .Values.domain }}}}
{{{{- range $appName, $appConfig := index .Values $domain "apps" }}}}
{{{{- if $appConfig.enabled }}}}
  kubectl --namespace {{{{ $.Release.Namespace }}}} port-forward service/{{{{ $appName }}}}-service {{{{ $appConfig.port }}}}:{{{{ $appConfig.port }}}}
  # Then access {{{{ $appName }}}} at: http://localhost:{{{{ $appConfig.port }}}}
{{{{- end }}}}
{{{{- end }}}}
{{{{- end }}}}

2. Application Status:
{{{{- $domain := .Values.domain }}}}
{{{{- range $appName, $appConfig := index .Values $domain "apps" }}}}
{{{{- if $appConfig.enabled }}}}
   - {{{{ $appName }}}}: {{{{ $appConfig.replicas }}}} replica(s) at {{{{ index $.Values.images $appName "repository" }}}}:{{{{ index $.Values.images $appName "tag" }}}}
{{{{- end }}}}
{{{{- end }}}}

3. Manual Artifacts:
{{{{- range $artifactName, $artifactConfig := .Values.artifacts }}}}
{{{{- if $artifactConfig.enabled }}}}
   - {{{{ $artifactName }}}} ({{{{ $artifactConfig.type }}}})
{{{{- end }}}}
{{{{- end }}}}

4. Check deployment status:
   kubectl get pods,services,jobs -l release={{{{ .Release.Name }}}}
'''

with open(os.path.join(templates_dir, "NOTES.txt"), "w") as f:
    f.write(notes_template)

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
        "deploy_order_weight": attr.int(default = 0),
    },
)