"""
Helm chart utilities for the release helper.
"""

import json
import os
import subprocess
import tempfile
import yaml
from pathlib import Path
from typing import Dict, List, Optional

from tools.release_helper.core import run_bazel
from tools.release_helper.metadata import get_app_metadata
from tools.release_helper.release import find_app_bazel_target


def get_helm_chart_targets(bazel_target: str) -> Dict[str, str]:
    """Get all Helm chart target names for an app using domain+app naming.
    
    Args:
        bazel_target: Full bazel target path for the app metadata
        
    Returns:
        Dict with Helm chart target names if chart is enabled, empty dict otherwise
    """
    # Get app metadata to check if Helm chart is enabled
    metadata = get_app_metadata(bazel_target)
    
    if not metadata.get("helm_chart_enabled", False):
        return {}
    
    # Extract package path from target
    target_parts = bazel_target[2:].split(":")
    if len(target_parts) != 2:
        raise ValueError(f"Invalid bazel target format: {bazel_target}")
    
    package_path = target_parts[0]
    app_name = metadata["name"]
    domain = metadata["domain"]
    
    # Use domain+app naming pattern for chart targets (consistent with image naming)
    chart_target_name = f"{domain}_{app_name}_helm"
    
    return {
        "chart": f"//{package_path}:{chart_target_name}_chart",
        "package": f"//{package_path}:{chart_target_name}_package",
        "chart_name": f"{domain}-{app_name}",  # Chart name follows domain-app pattern
    }


def build_helm_chart(bazel_target: str) -> Optional[str]:
    """Build Helm chart for an app.
    
    Args:
        bazel_target: Full bazel target path for the app metadata
        
    Returns:
        Path to the generated chart directory, or None if chart not enabled
    """
    chart_targets = get_helm_chart_targets(bazel_target)
    
    if not chart_targets:
        print(f"Helm chart not enabled for {bazel_target}")
        return None
    
    print(f"Building Helm chart for {bazel_target}...")
    run_bazel(["build", chart_targets["chart"]])
    
    # Extract package path from target for finding the generated chart
    target_parts = bazel_target[2:].split(":")
    package_path = target_parts[0]
    
    metadata = get_app_metadata(bazel_target)
    app_name = metadata["name"]
    
    # Find the generated chart directory
    from tools.release_helper.core import find_workspace_root
    workspace_root = find_workspace_root()
    
    # Use the domain+app naming pattern for the chart directory
    metadata = get_app_metadata(bazel_target)
    domain = metadata["domain"]
    app_name = metadata["name"]
    chart_target_name = f"{domain}_{app_name}_helm"
    
    chart_dir = workspace_root / f"bazel-bin/{package_path}/{chart_target_name}_chart"
    
    if not chart_dir.exists():
        raise FileNotFoundError(f"Generated chart directory not found: {chart_dir}")
    
    return str(chart_dir)


def package_helm_chart(bazel_target: str) -> Optional[str]:
    """Package Helm chart for an app.
    
    Args:
        bazel_target: Full bazel target path for the app metadata
        
    Returns:
        Path to the packaged chart (.tgz file), or None if chart not enabled
    """
    chart_targets = get_helm_chart_targets(bazel_target)
    
    if not chart_targets:
        print(f"Helm chart not enabled for {bazel_target}")
        return None
    
    print(f"Packaging Helm chart for {bazel_target}...")
    run_bazel(["build", chart_targets["package"]])
    
    # Extract package path from target for finding the generated package
    target_parts = bazel_target[2:].split(":")
    package_path = target_parts[0]
    
    metadata = get_app_metadata(bazel_target)
    app_name = metadata["name"]
    chart_version = metadata.get("version", "latest")
    
    # Find the generated package file
    from tools.release_helper.core import find_workspace_root
    workspace_root = find_workspace_root()
    
    metadata = get_app_metadata(bazel_target)
    app_name = metadata["name"]
    domain = metadata["domain"]
    chart_version = metadata.get("version", "latest")
    
    # Use domain-app naming pattern for the package file
    chart_name = f"{domain}-{app_name}"
    package_file = workspace_root / f"bazel-bin/{package_path}/{chart_name}-{chart_version}.tgz"
    
    if not package_file.exists():
        raise FileNotFoundError(f"Generated chart package not found: {package_file}")
    
    return str(package_file)


def validate_helm_chart(chart_dir: str) -> bool:
    """Validate a Helm chart directory.
    
    Args:
        chart_dir: Path to the chart directory
        
    Returns:
        True if the chart is valid, False otherwise
    """
    chart_path = Path(chart_dir)
    
    # Check for required files
    required_files = ["Chart.yaml", "values.yaml"]
    for file_name in required_files:
        file_path = chart_path / file_name
        if not file_path.exists():
            print(f"Missing required file: {file_path}")
            return False
    
    # Validate Chart.yaml
    try:
        chart_yaml_path = chart_path / "Chart.yaml"
        with open(chart_yaml_path) as f:
            chart_yaml = yaml.safe_load(f)
        
        required_fields = ["apiVersion", "name", "version"]
        for field in required_fields:
            if field not in chart_yaml:
                print(f"Missing required field in Chart.yaml: {field}")
                return False
                
        print(f"Chart validation passed for {chart_yaml.get('name', 'unknown')}")
        return True
        
    except Exception as e:
        print(f"Error validating Chart.yaml: {e}")
        return False


def lint_helm_chart(chart_dir: str) -> bool:
    """Lint a Helm chart using helm lint (if available).
    
    Args:
        chart_dir: Path to the chart directory
        
    Returns:
        True if linting passed, False otherwise
    """
    try:
        # Check if helm is available
        result = subprocess.run(
            ["helm", "version", "--short"],
            capture_output=True,
            text=True,
            timeout=10
        )
        
        if result.returncode != 0:
            print("Helm CLI not available, skipping helm lint")
            return True  # Don't fail if helm is not available
        
        # Run helm lint
        result = subprocess.run(
            ["helm", "lint", chart_dir],
            capture_output=True,
            text=True,
            timeout=30
        )
        
        if result.returncode == 0:
            print(f"Helm lint passed for {chart_dir}")
            return True
        else:
            print(f"Helm lint failed for {chart_dir}:")
            print(result.stdout)
            print(result.stderr)
            return False
            
    except subprocess.TimeoutExpired:
        print("Helm lint timed out")
        return False
    except FileNotFoundError:
        print("Helm CLI not found, skipping helm lint")
        return True  # Don't fail if helm is not available
    except Exception as e:
        print(f"Error running helm lint: {e}")
        return False


def update_chart_with_image_version(chart_dir: str, image_repo: str, image_tag: str) -> None:
    """Update a Helm chart's values.yaml with a specific image version.
    
    Args:
        chart_dir: Path to the chart directory
        image_repo: Container image repository
        image_tag: Container image tag
    """
    values_path = Path(chart_dir) / "values.yaml"
    
    if not values_path.exists():
        print(f"values.yaml not found at {values_path}")
        return
    
    try:
        # Load existing values
        with open(values_path) as f:
            values = yaml.safe_load(f) or {}
        
        # Update image configuration
        if "image" not in values:
            values["image"] = {}
        
        values["image"]["repository"] = image_repo
        values["image"]["tag"] = image_tag
        
        # Write updated values
        with open(values_path, "w") as f:
            yaml.dump(values, f, default_flow_style=False, sort_keys=False)
        
        print(f"Updated {values_path} with image {image_repo}:{image_tag}")
        
    except Exception as e:
        print(f"Error updating chart values: {e}")
        raise


def build_composite_helm_chart(
    composite_name: str,
    apps: List[str],
    chart_version: str = "0.1.0",
    domain: str = "composite",
    description: str = "",
    global_registry: str = "ghcr.io"
) -> Optional[str]:
    """Build a composite Helm chart that includes multiple apps.
    
    Args:
        composite_name: Name of the composite chart
        apps: List of app names to include
        chart_version: Version of the composite chart
        domain: Domain for the composite chart
        description: Description of the composite chart
        global_registry: Global container registry
        
    Returns:
        Path to the generated composite chart directory
    """
    try:
        # Create a temporary BUILD file for the composite chart
        import tempfile
        import os
        
        # Validate that all apps exist and have metadata
        app_metadata = []
        for app in apps:
            try:
                bazel_target = find_app_bazel_target(app)
                metadata = get_app_metadata(bazel_target)
                app_metadata.append(metadata)
            except ValueError as e:
                print(f"Error finding app '{app}': {e}")
                return None
        
        # Create temporary BUILD file for composite chart
        build_content = f'''
load("//tools:helm.bzl", "release_composite_helm_chart")

release_composite_helm_chart(
    name = "{composite_name}_composite",
    composite_name = "{composite_name}",
    description = "{description}",
    chart_version = "{chart_version}",
    domain = "{domain}",
    apps = {[app for app in apps]},
    global_registry = "{global_registry}",
)
'''
        
        # For now, return a placeholder path since the full implementation
        # would require more complex Bazel integration
        print(f"Composite chart '{composite_name}' would include:")
        for i, (app, metadata) in enumerate(zip(apps, app_metadata)):
            print(f"  {i+1}. {app} ({metadata['domain']}-{metadata['name']})")
        
        print(f"\nTo implement this composite chart:")
        print(f"1. Create a BUILD file with the composite chart definition")
        print(f"2. Run: bazel build //path/to/composite:{composite_name}_composite_chart")
        print(f"3. Package: bazel build //path/to/composite:{composite_name}_composite_package")
        
        return f"composite-{composite_name}"  # Placeholder path
        
    except Exception as e:
        print(f"Error building composite chart: {e}")
        return None