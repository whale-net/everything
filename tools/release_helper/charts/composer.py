"""
Helm chart composer - generates Helm charts from app metadata.
"""

import json
import os
import shutil
import tempfile
from pathlib import Path
from typing import Dict, List, Optional

import yaml

from tools.release_helper.charts.types import (
    AppConfig,
    AppMetadata,
    AppType,
    HealthCheckConfig,
    IngressConfig,
    ManifestFile,
    resolve_app_type,
)


class HelmComposer:
    """Composes Helm charts from app metadata."""
    
    def __init__(
        self,
        chart_name: str,
        version: str,
        environment: str,
        namespace: str,
        output_dir: str,
        template_dir: str
    ):
        """Initialize the composer.
        
        Args:
            chart_name: Name of the Helm chart
            version: Chart version
            environment: Environment name (e.g., 'production')
            namespace: Kubernetes namespace
            output_dir: Output directory for generated chart
            template_dir: Directory containing template files
        """
        self.chart_name = chart_name
        self.version = version
        self.environment = environment
        self.namespace = namespace
        self.output_dir = output_dir
        self.template_dir = template_dir
        self.apps: List[AppMetadata] = []
        self.manifests: List[ManifestFile] = []
    
    def load_metadata(self, metadata_files: List[str]) -> None:
        """Load app metadata from JSON files.
        
        Args:
            metadata_files: List of paths to metadata JSON files
        """
        for file_path in metadata_files:
            with open(file_path, 'r') as f:
                data = json.load(f)
            
            metadata = AppMetadata(
                name=data.get('name', ''),
                app_type=data.get('app_type', ''),
                version=data.get('version', 'latest'),
                description=data.get('description', ''),
                registry=data.get('registry', ''),
                repo_name=data.get('repo_name', ''),
                image_target=data.get('image_target', ''),
                domain=data.get('domain', ''),
                language=data.get('language', ''),
                port=data.get('port'),
                replicas=data.get('replicas'),
                labels=data.get('labels', {}),
                annotations=data.get('annotations', {}),
                dependencies=data.get('dependencies', []),
                health_check=data.get('health_check'),
                ingress=data.get('ingress'),
                command=data.get('command'),
                args=data.get('args')
            )
            
            self.apps.append(metadata)
    
    def load_manifests(self, manifest_files: List[str]) -> None:
        """Load manual Kubernetes manifest files.
        
        Args:
            manifest_files: List of paths to manifest YAML files
        """
        for file_path in manifest_files:
            with open(file_path, 'rb') as f:
                content = f.read()
            
            manifest = ManifestFile(
                path=file_path,
                content=content,
                filename=os.path.basename(file_path)
            )
            
            self.manifests.append(manifest)
    
    def generate_chart(self) -> None:
        """Generate the complete Helm chart."""
        # Create output directory structure
        chart_dir = Path(self.output_dir) / self.chart_name
        templates_dir = chart_dir / "templates"
        
        chart_dir.mkdir(parents=True, exist_ok=True)
        templates_dir.mkdir(parents=True, exist_ok=True)
        
        # Generate Chart.yaml
        self._generate_chart_yaml(chart_dir)
        
        # Generate values.yaml
        self._generate_values_yaml(chart_dir)
        
        # Generate resource templates
        self._generate_resource_templates(templates_dir)
        
        # Process manual manifests
        self._process_manual_manifests(templates_dir)
    
    def _generate_chart_yaml(self, chart_dir: Path) -> None:
        """Generate Chart.yaml file."""
        chart_data = {
            'apiVersion': 'v2',
            'name': self.chart_name,
            'description': 'Composed Helm chart for multiple applications',
            'type': 'application',
            'version': self.version,
            'appVersion': self.version
        }
        
        with open(chart_dir / 'Chart.yaml', 'w') as f:
            yaml.dump(chart_data, f, default_flow_style=False, sort_keys=False)
    
    def _build_app_config(self, app: AppMetadata) -> AppConfig:
        """Build AppConfig from AppMetadata with smart defaults."""
        app_type = resolve_app_type(app.name, app.app_type)
        
        # Get default resources for this app type
        resources = app_type.default_resource_config()
        
        # Set replicas: use metadata if provided, otherwise default based on type
        replicas = app.replicas if app.replicas else 1
        if replicas == 0:
            replicas = 1
            if app_type in (AppType.EXTERNAL_API, AppType.INTERNAL_API):
                replicas = 2
        
        # Set default port
        port = app.port
        if not port and app_type in (AppType.EXTERNAL_API, AppType.INTERNAL_API):
            port = 8000
        
        # Build health check configuration
        health_check = None
        if app_type in (AppType.EXTERNAL_API, AppType.INTERNAL_API):
            if app.health_check and app.health_check.get('enabled'):
                # Use health check path from metadata
                health_check = HealthCheckConfig(
                    path=app.health_check.get('path', '/health'),
                    initial_delay_seconds=10,
                    period_seconds=10,
                    timeout_seconds=5,
                    success_threshold=1,
                    failure_threshold=3
                )
            elif not app.health_check or app.health_check.get('path'):
                # Default to /health for APIs if not specified
                health_check = HealthCheckConfig(
                    path='/health',
                    initial_delay_seconds=10,
                    period_seconds=10,
                    timeout_seconds=5,
                    success_threshold=1,
                    failure_threshold=3
                )
        
        # Build ingress configuration if provided
        ingress_config = None
        if app.ingress and app.ingress.get('host'):
            ingress_config = IngressConfig(
                host=app.ingress.get('host', ''),
                tls_secret_name=app.ingress.get('tls_secret_name', '')
            )
        
        return AppConfig(
            type=app_type.value,
            image=app.get_image(),
            image_tag=app.get_image_tag(),
            port=port,
            replicas=replicas,
            resources=resources.to_values_format(),
            health_check=health_check,
            command=app.command,
            args=app.args,
            env={},
            ingress=ingress_config
        )
    
    def _generate_values_yaml(self, chart_dir: Path) -> None:
        """Generate values.yaml file."""
        values_data = {
            'global': {
                'namespace': self.namespace,
                'environment': self.environment
            },
            'apps': {},
            'ingress': {
                'enabled': self._has_external_apis(),
                'className': '',
                'annotations': {},
                'tls': []
            },
            'manifests': {
                'enabled': True
            }
        }
        
        # Build app configurations
        for app in self.apps:
            config = self._build_app_config(app)
            app_dict = {
                'type': config.type,
                'image': config.image,
                'imageTag': config.image_tag,
                'replicas': config.replicas,
                'resources': config.resources
            }
            
            if config.port:
                app_dict['port'] = config.port
            
            if config.health_check:
                hc = config.health_check
                app_dict['healthCheck'] = {
                    'path': hc.path,
                    'initialDelaySeconds': hc.initial_delay_seconds,
                    'periodSeconds': hc.period_seconds,
                    'timeoutSeconds': hc.timeout_seconds,
                    'successThreshold': hc.success_threshold,
                    'failureThreshold': hc.failure_threshold
                }
                if hc.port:
                    app_dict['healthCheck']['port'] = hc.port
            
            if config.command:
                app_dict['command'] = config.command
            
            if config.args:
                app_dict['args'] = config.args
            
            if config.env:
                app_dict['env'] = config.env
            
            if config.ingress:
                app_dict['ingress'] = {
                    'host': config.ingress.host,
                    'tlsSecretName': config.ingress.tls_secret_name
                }
            
            values_data['apps'][app.name] = app_dict
        
        with open(chart_dir / 'values.yaml', 'w') as f:
            yaml.dump(values_data, f, default_flow_style=False, sort_keys=False)
    
    def _generate_resource_templates(self, templates_dir: Path) -> None:
        """Generate Kubernetes resource templates."""
        # Determine which templates are needed
        template_map = set()
        for app in self.apps:
            app_type = resolve_app_type(app.name, app.app_type)
            for tmpl in app_type.template_artifacts():
                template_map.add(tmpl)
        
        # Copy templates
        for tmpl in template_map:
            # Source files have .tmpl extension
            src_path = Path(self.template_dir) / f"{tmpl}.tmpl"
            # Destination files keep original name (without .tmpl)
            dst_path = templates_dir / tmpl
            
            if src_path.exists():
                shutil.copy(src_path, dst_path)
    
    def _process_manual_manifests(self, templates_dir: Path) -> None:
        """Process and copy manual Kubernetes manifests."""
        for i, manifest in enumerate(self.manifests):
            # Generate a unique filename with prefix to avoid conflicts
            dst_filename = f"manifest-{i:02d}-{manifest.filename}"
            dst_path = templates_dir / dst_filename
            
            # Process the manifest to inject Helm templating
            processed = self._inject_helm_templating(manifest.content)
            
            with open(dst_path, 'wb') as f:
                f.write(processed)
    
    def _inject_helm_templating(self, content: bytes) -> bytes:
        """Process a Kubernetes manifest to inject Helm template directives."""
        content_str = content.decode('utf-8')
        
        # Add Helm template header comment
        helm_content = "{{- if .Values.manifests.enabled | default true }}\n"
        
        # Replace namespace references with Helm template
        lines = content_str.split('\n')
        for i, line in enumerate(lines):
            # Match lines like "  namespace: some-namespace"
            if 'namespace:' in line and '{{' not in line:
                # Extract indentation
                indent = len(line) - len(line.lstrip(' \t'))
                spaces = ' ' * indent
                lines[i] = f"{spaces}namespace: {{{{ .Values.global.namespace }}}}"
            
            # Add environment label if labels section exists
            if 'labels:' in line and i + 1 < len(lines):
                # Check if next line is already indented (part of labels)
                next_line = lines[i + 1]
                if next_line and next_line[0] in (' ', '\t'):
                    # Add environment label after the labels: line
                    indent = len(next_line) - len(next_line.lstrip(' \t'))
                    spaces = ' ' * indent
                    env_label = f"{spaces}environment: {{{{ .Values.global.environment }}}}"
                    # Insert if not already present
                    if 'environment:' not in content_str:
                        lines.insert(i + 1, env_label)
        
        content_str = '\n'.join(lines)
        helm_content += content_str
        helm_content += "\n{{- end }}\n"
        
        return helm_content.encode('utf-8')
    
    def _has_external_apis(self) -> bool:
        """Check if any apps are external APIs."""
        for app in self.apps:
            try:
                app_type = resolve_app_type(app.name, app.app_type)
                if app_type == AppType.EXTERNAL_API:
                    return True
            except ValueError:
                continue
        return False


def main():
    """Main entry point for CLI usage."""
    import argparse
    
    parser = argparse.ArgumentParser(
        description='Generate Helm charts from app metadata'
    )
    parser.add_argument(
        '--metadata',
        required=True,
        help='Comma-separated list of metadata JSON files'
    )
    parser.add_argument(
        '--manifests',
        help='Comma-separated list of manual Kubernetes manifest YAML files'
    )
    parser.add_argument(
        '--chart-name',
        default='composed-chart',
        help='Name of the Helm chart'
    )
    parser.add_argument(
        '--version',
        default='1.0.0',
        help='Chart version'
    )
    parser.add_argument(
        '--environment',
        default='production',
        help='Environment name'
    )
    parser.add_argument(
        '--namespace',
        default='default',
        help='Kubernetes namespace'
    )
    parser.add_argument(
        '--output',
        default='.',
        help='Output directory for generated chart'
    )
    parser.add_argument(
        '--template-dir',
        required=True,
        help='Directory containing template files'
    )
    
    args = parser.parse_args()
    
    # Parse metadata file list
    metadata_files = [f.strip() for f in args.metadata.split(',')]
    
    # Parse manifest file list
    manifest_files = []
    if args.manifests:
        manifest_files = [f.strip() for f in args.manifests.split(',')]
    
    # Create composer
    composer = HelmComposer(
        chart_name=args.chart_name,
        version=args.version,
        environment=args.environment,
        namespace=args.namespace,
        output_dir=args.output,
        template_dir=args.template_dir
    )
    
    # Load metadata
    composer.load_metadata(metadata_files)
    
    # Load manifests if provided
    if manifest_files:
        composer.load_manifests(manifest_files)
    
    # Generate chart
    composer.generate_chart()
    
    print(f"Successfully generated Helm chart: {os.path.join(args.output, args.chart_name)}")
    print(f"  Chart: {args.chart_name} (version {args.version})")
    print(f"  Environment: {args.environment}")
    print(f"  Namespace: {args.namespace}")
    print(f"  Apps: {len(metadata_files)}")


if __name__ == '__main__':
    main()
