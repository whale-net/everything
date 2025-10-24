#!/usr/bin/env python3
"""Version resolver utility for Helm chart releases.

This utility resolves app versions by querying Bazel metadata targets
and integrating with Git tags and release information.
"""

import json
import subprocess
import sys
import argparse
import os
from pathlib import Path
from typing import Dict, List, Optional, Any

class VersionResolver:
    """Resolves app versions for Helm chart generation."""
    
    def __init__(self, workspace_root: Optional[str] = None):
        self.workspace_root = workspace_root or os.getcwd()
        
    def get_app_metadata(self, domain: str, app_name: str) -> Dict[str, Any]:
        """Get metadata for a single app by reading its metadata file directly."""
        metadata_target = f"//{domain}:{app_name}_metadata"
        
        try:
            # Try to find the bazel-bin directory
            bazel_bin_candidates = [
                os.path.join(self.workspace_root, "bazel-bin"),  # Standard location
                os.environ.get("BAZEL_BIN", ""),  # Environment variable if available
            ]
            
            bazel_bin = None
            for candidate in bazel_bin_candidates:
                if candidate and os.path.isdir(candidate):
                    bazel_bin = candidate
                    break
            
            if not bazel_bin:
                # Try to get bazel-bin from bazel info
                try:
                    info_result = subprocess.run([
                        "bazel", "info", "bazel-bin"
                    ], capture_output=True, text=True, check=True, cwd=self.workspace_root)
                    bazel_bin = info_result.stdout.strip()
                except subprocess.CalledProcessError:
                    # If that fails, try the default symlink
                    bazel_bin = os.path.join(self.workspace_root, "bazel-bin")
            
            metadata_file = Path(bazel_bin) / domain / f"{app_name}_metadata_metadata.json"
            
            # Check if metadata file exists, if not try to build it  
            if not metadata_file.exists():
                print(f"Metadata file not found at {metadata_file}, trying to build...", file=sys.stderr)
                build_result = subprocess.run([
                    "bazel", "build", metadata_target
                ], capture_output=True, text=True, cwd=self.workspace_root)
                
                if build_result.returncode != 0:
                    raise RuntimeError(f"Failed to build metadata target {metadata_target}: {build_result.stderr}")
            
            if metadata_file.exists():
                with open(metadata_file) as f:
                    metadata = json.load(f)
                    print(f"Successfully loaded metadata for {app_name}: {metadata['description']}", file=sys.stderr)
                    return metadata
            else:
                raise FileNotFoundError(f"Metadata file not found: {metadata_file}")
                
        except Exception as e:
            print(f"Warning: Could not resolve metadata for {app_name}: {e}", file=sys.stderr)
            # Return fallback metadata
            return {
                "name": app_name,
                "version": "latest",
                "binary_target": f"//{domain}/src/host:{app_name}",
                "image_target": f"{app_name}_image",
                "description": f"Service: {app_name}",
                "language": "python",
                "registry": "ghcr.io", 
                "repo_name": f"{domain}-{app_name}",
                "domain": domain,
            }
    
    def resolve_image_tag(self, metadata: Dict[str, Any], version_strategy: str) -> str:
        """Resolve image tag based on strategy."""
        if version_strategy == "latest":
            return "latest"
        elif version_strategy == "stable":
            # In a full implementation, this would query for the latest stable release
            return self._get_latest_stable_tag(metadata)
        elif version_strategy.startswith("tag:"):
            return version_strategy[4:]  # Remove "tag:" prefix
        elif version_strategy.startswith("sha:"):
            return version_strategy[4:]  # Remove "sha:" prefix
        else:
            # Default to the version from metadata
            return metadata.get("version", "latest")
    
    def _get_latest_stable_tag(self, metadata: Dict[str, Any]) -> str:
        """Get the latest stable git tag for the app."""
        try:
            # Get all git tags, filter for app-specific tags if available
            result = subprocess.run([
                "git", "tag", "--sort=-version:refname"
            ], capture_output=True, text=True, check=True, cwd=self.workspace_root)
            
            tags = result.stdout.strip().split('\n')
            app_name = metadata["name"]
            domain = metadata["domain"]
            
            # Look for tags in format: domain-app-v1.0.0 or v1.0.0
            for tag in tags:
                if tag.startswith(f"{domain}-{app_name}-v"):
                    return tag.split('-', 2)[2]  # Return version part
                elif tag.startswith('v') and '.' in tag:
                    return tag  # Return general version tag
            
            return "latest"
        except Exception:
            return "latest"
    
    def generate_chart_values(self, domain: str, apps: List[str], 
                            version_strategy: str = "latest",
                            overrides: Optional[Dict[str, Any]] = None) -> Dict[str, Any]:
        """Generate complete values.yaml content for a chart."""
        
        values = {
            "images": {},
            domain: {"apps": {}},
            "service": {
                "enabled": True,
                "type": "ClusterIP"
            },
            "ingress": {
                "enabled": False,
                "host": "localhost",
                "tls": {"enabled": False}
            },
            "env": {
                "app_env": "dev"
            }
        }
        
        # Process each app
        for app_name in apps:
            metadata = self.get_app_metadata(domain, app_name)
            resolved_tag = self.resolve_image_tag(metadata, version_strategy)
            
            # Image configuration
            values["images"][app_name] = {
                "name": f"{metadata['registry']}/whale-net/{metadata['repo_name']}",
                "tag": resolved_tag,
                "repository": f"{metadata['registry']}/whale-net/{metadata['repo_name']}"
            }
            
            # App configuration
            # Note: Resource limits are example values only. Actual resource configuration
            # is determined by the Helm chart composer (tools/helm/composer.go) based on
            # app type and language. See DefaultResourceConfigForLanguage() in types.go.
            values[domain]["apps"][app_name] = {
                "enabled": True,
                "version": resolved_tag,
                "description": metadata.get("description", ""),
                "language": metadata.get("language", ""),
                "replicas": 1,
                "port": 8000,
                "resources": {
                    "requests": {
                        "memory": "128Mi",
                        "cpu": "100m"
                    },
                    "limits": {
                        "memory": "512Mi",
                        "cpu": "500m"
                    }
                }
            }
        
        # Apply user overrides
        if overrides:
            self._deep_merge(values, overrides)
        
        return values
    
    def _deep_merge(self, base: Dict[str, Any], override: Dict[str, Any]) -> None:
        """Deep merge dictionaries in place."""
        for key, value in override.items():
            if key in base and isinstance(base[key], dict) and isinstance(value, dict):
                self._deep_merge(base[key], value)
            else:
                # Convert string values that look like booleans
                if isinstance(value, str):
                    if value.lower() == "true":
                        base[key] = True
                    elif value.lower() == "false":
                        base[key] = False
                    else:
                        base[key] = value
                else:
                    base[key] = value

def main():
    parser = argparse.ArgumentParser(description="Resolve app versions for Helm charts")
    parser.add_argument("--domain", required=True, help="Domain name")
    parser.add_argument("--apps", required=True, nargs="+", help="App names")
    parser.add_argument("--version-strategy", default="latest", 
                      help="Version strategy: latest, stable, tag:v1.0.0")
    parser.add_argument("--overrides", help="JSON overrides for values")
    parser.add_argument("--output-format", choices=["yaml", "json"], default="yaml",
                      help="Output format")
    parser.add_argument("--workspace-root", help="Bazel workspace root")
    
    args = parser.parse_args()
    
    # Parse overrides
    overrides = {}
    if args.overrides:
        try:
            overrides = json.loads(args.overrides)
        except json.JSONDecodeError as e:
            print(f"Error parsing overrides JSON: {e}", file=sys.stderr)
            sys.exit(1)
    
    # Create resolver and generate values
    resolver = VersionResolver(args.workspace_root)
    values = resolver.generate_chart_values(
        args.domain, 
        args.apps, 
        args.version_strategy,
        overrides
    )
    
    # Output results
    if args.output_format == "json":
        json.dump(values, sys.stdout, indent=2, sort_keys=True)
    else:
        try:
            import yaml
            yaml.dump(values, sys.stdout, default_flow_style=False, sort_keys=True)
        except ImportError:
            print("Error: PyYAML not available. Use --output-format json instead.", file=sys.stderr)
            json.dump(values, sys.stdout, indent=2, sort_keys=True)

if __name__ == "__main__":
    main()