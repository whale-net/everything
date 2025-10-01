"""
Helm chart utilities for the release helper.
"""

import json
import re
import subprocess
from pathlib import Path
from typing import Dict, List, Optional

from tools.release_helper.core import run_bazel, find_workspace_root
from tools.release_helper.git import get_latest_app_version


def get_helm_chart_metadata(bazel_target: str) -> Dict:
    """Get release metadata for a helm chart by building and reading its metadata target.
    
    Args:
        bazel_target: Full bazel target path (e.g., "//demo:fastapi_chart_chart_metadata")
    """
    # Build the metadata target
    run_bazel(["build", bazel_target])

    # Extract path from target for finding the generated file
    # Target format: //path/to/chart:target_name
    if not bazel_target.startswith("//"):
        raise ValueError(f"Invalid bazel target format: {bazel_target}")
    
    target_parts = bazel_target[2:].split(":")
    if len(target_parts) != 2:
        raise ValueError(f"Invalid bazel target format: {bazel_target}")
    
    package_path = target_parts[0]
    target_name = target_parts[1]

    # Read the generated JSON file
    workspace_root = find_workspace_root()
    metadata_file = workspace_root / f"bazel-bin/{package_path}/{target_name}_chart_metadata.json"
    if not metadata_file.exists():
        raise FileNotFoundError(f"Metadata file not found: {metadata_file}")

    with open(metadata_file) as f:
        return json.load(f)


def list_all_helm_charts() -> List[Dict[str, str]]:
    """List all helm charts in the monorepo that have release metadata.
    
    Returns:
        List of dicts with 'bazel_target', 'name', 'domain', 'apps' for each chart
    """
    # Query for all helm chart metadata targets
    result = run_bazel(["query", "kind(helm_chart_metadata, //...)", "--output=label"])

    charts = []
    for line in result.stdout.strip().split('\n'):
        if line and '_chart_metadata' in line:
            # Get metadata to extract chart info
            try:
                metadata = get_helm_chart_metadata(line)
                charts.append({
                    'bazel_target': line,
                    'name': metadata['name'],
                    'domain': metadata['domain'],
                    'namespace': metadata['namespace'],
                    'apps': metadata.get('apps', []),
                    'chart_target': metadata.get('chart_target', ''),
                })
            except Exception as e:
                print(f"Warning: Could not get metadata for {line}: {e}")
                continue

    return sorted(charts, key=lambda x: x['name'])


def find_helm_chart_bazel_target(chart_name: str) -> str:
    """Find the bazel target for a helm chart by name.
    
    Args:
        chart_name: Name of the helm chart (e.g., "hello-fastapi")
        
    Returns:
        Bazel target path (e.g., "//demo:fastapi_chart_chart_metadata")
        
    Raises:
        ValueError: If chart not found or multiple charts match
    """
    all_charts = list_all_helm_charts()
    
    # Filter by exact chart name match
    matching_charts = [c for c in all_charts if c['name'] == chart_name]
    
    if not matching_charts:
        raise ValueError(f"No helm chart found with name '{chart_name}'")
    
    if len(matching_charts) > 1:
        targets = [c['bazel_target'] for c in matching_charts]
        raise ValueError(f"Multiple helm charts found with name '{chart_name}': {targets}")
    
    return matching_charts[0]['bazel_target']


def resolve_app_versions_for_chart(chart_metadata: Dict, use_released_versions: bool = True) -> Dict[str, str]:
    """Resolve the versions of apps included in a helm chart.
    
    Args:
        chart_metadata: Metadata dict from get_helm_chart_metadata
        use_released_versions: If True, use latest git tags. If False, use "latest"
        
    Returns:
        Dict mapping app name to version (e.g., {"hello_fastapi": "v1.0.0"})
    """
    app_versions = {}
    
    for app_name in chart_metadata.get('apps', []):
        if use_released_versions:
            # Get domain from the chart to construct proper tag
            # We need to query the app's metadata to get its domain
            try:
                from tools.release_helper.release import find_app_bazel_target
                from tools.release_helper.metadata import get_app_metadata
                
                app_target = find_app_bazel_target(app_name)
                app_metadata = get_app_metadata(app_target)
                app_domain = app_metadata['domain']
                
                # Get latest version from git tags
                latest_version = get_latest_app_version(app_domain, app_name)
                
                if latest_version:
                    app_versions[app_name] = latest_version
                else:
                    # Fallback to "latest" if no version found
                    print(f"Warning: No released version found for {app_name}, using 'latest'")
                    app_versions[app_name] = "latest"
            except Exception as e:
                print(f"Warning: Could not resolve version for {app_name}: {e}, using 'latest'")
                app_versions[app_name] = "latest"
        else:
            # Use "latest" for all apps
            app_versions[app_name] = "latest"
    
    return app_versions


def build_helm_chart(chart_target: str, chart_name: str, chart_version: str, app_versions: Optional[Dict[str, str]] = None) -> Path:
    """Build a helm chart with specified version and app versions.
    
    Args:
        chart_target: Bazel target for the chart (e.g., "//demo:fastapi_chart")
        chart_name: Name of the chart (e.g., "hello-fastapi")
        chart_version: Version to use for the chart (e.g., "v1.0.0")
        app_versions: Optional dict of app name -> version overrides
        
    Returns:
        Path to the generated chart tarball
    """
    workspace_root = find_workspace_root()
    
    # Extract package path and target name
    if not chart_target.startswith("//"):
        raise ValueError(f"Invalid bazel target format: {chart_target}")
    
    target_parts = chart_target[2:].split(":")
    if len(target_parts) != 2:
        raise ValueError(f"Invalid bazel target format: {chart_target}")
    
    package_path = target_parts[0]
    target_name = target_parts[1]
    
    # Build the chart with bazel
    # TODO: In the future, we might want to inject app versions into the chart build
    # For now, we build the chart as-is and assume it uses version placeholders
    print(f"Building bazel target: {chart_target}")
    run_bazel(["build", chart_target])
    
    # Find the generated tarball
    # The helm_chart rule outputs a tarball named {chart_name}.tar.gz (not {target_name})
    chart_tarball = workspace_root / f"bazel-bin/{package_path}/{chart_name}.tar.gz"
    
    if not chart_tarball.exists():
        # Try with target name as fallback
        alt_tarball = workspace_root / f"bazel-bin/{package_path}/{target_name}.tar.gz"
        if alt_tarball.exists():
            chart_tarball = alt_tarball
        else:
            raise FileNotFoundError(f"Chart tarball not found at {chart_tarball} or {alt_tarball}")
    
    print(f"Found chart tarball: {chart_tarball}")
    return chart_tarball


def package_helm_chart_for_release(
    chart_name: str,
    chart_version: str,
    output_dir: Optional[Path] = None,
    use_released_app_versions: bool = True
) -> Path:
    """Package a helm chart for release with resolved app versions.
    
    Args:
        chart_name: Name of the helm chart (e.g., "hello-fastapi")
        chart_version: Version for the chart (e.g., "v1.0.0")
        output_dir: Optional output directory for the packaged chart
        use_released_app_versions: Whether to resolve app versions from git tags
        
    Returns:
        Path to the packaged chart tarball
    """
    # Find the chart
    chart_metadata_target = find_helm_chart_bazel_target(chart_name)
    chart_metadata = get_helm_chart_metadata(chart_metadata_target)
    
    # Resolve app versions
    app_versions = resolve_app_versions_for_chart(chart_metadata, use_released_app_versions)
    
    print(f"Packaging chart '{chart_name}' version {chart_version}")
    print(f"App versions: {app_versions}")
    
    # Get the actual chart target (without _chart_metadata suffix)
    # The metadata contains chart_target which is relative to the package
    chart_package = chart_metadata_target.rsplit(":", 1)[0]
    chart_target_name = chart_metadata.get('chart_target', '').lstrip(':')
    chart_target = f"{chart_package}:{chart_target_name}"
    
    # Build the chart - use the chart_name from metadata for finding the output file
    actual_chart_name = chart_metadata.get('name', chart_name)
    chart_tarball = build_helm_chart(chart_target, actual_chart_name, chart_version, app_versions)
    
    # If output_dir is specified, copy the tarball there
    if output_dir:
        output_dir.mkdir(parents=True, exist_ok=True)
        output_path = output_dir / f"{chart_name}-{chart_version}.tgz"
        
        # Copy the tarball
        import shutil
        shutil.copy(chart_tarball, output_path)
        return output_path
    
    return chart_tarball
