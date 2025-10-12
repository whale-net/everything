"""
App metadata utilities for the release helper.
"""

import json
from pathlib import Path
from typing import Dict, List

from tools.release_helper.core import run_bazel, find_workspace_root


def get_app_metadata(bazel_target: str) -> Dict:
    """Get release metadata for an app by building and reading its metadata target.
    
    Args:
        bazel_target: Full bazel target path (e.g., "//path/to/app:app_metadata")
    """
    # Build the metadata target
    run_bazel(["build", bazel_target])

    # Extract path from target for finding the generated file
    # Target format: //path/to/app:target_name
    if not bazel_target.startswith("//"):
        raise ValueError(f"Invalid bazel target format: {bazel_target}")
    
    target_parts = bazel_target[2:].split(":")
    if len(target_parts) != 2:
        raise ValueError(f"Invalid bazel target format: {bazel_target}")
    
    package_path = target_parts[0]
    target_name = target_parts[1]

    # Read the generated JSON file
    workspace_root = find_workspace_root()
    metadata_file = workspace_root / f"bazel-bin/{package_path}/{target_name}_metadata.json"
    if not metadata_file.exists():
        raise FileNotFoundError(f"Metadata file not found: {metadata_file}")

    with open(metadata_file) as f:
        return json.load(f)


def list_all_apps() -> List[Dict[str, str]]:
    """List all apps in the monorepo that have release metadata.
    
    Returns:
        List of dicts with full metadata for each app
    """
    # Query for all metadata targets
    result = run_bazel(["query", "kind(app_metadata, //...)", "--output=label"])

    apps = []
    for line in result.stdout.strip().split('\n'):
        if line and '_metadata' in line:
            # Get metadata to extract app info
            try:
                metadata = get_app_metadata(line)
                # Add the bazel_target to the metadata
                metadata['bazel_target'] = line
                apps.append(metadata)
            except Exception as e:
                print(f"Warning: Could not get metadata for {line}: {e}")
                continue

    return sorted(apps, key=lambda x: x['name'])


def get_image_targets(bazel_target: str) -> Dict[str, str]:
    """Get all image-related targets for an app.
    
    Args:
        bazel_target: Full bazel target path (e.g., "//path/to/app:app_metadata")
    """
    # Extract package path from metadata target
    target_parts = bazel_target[2:].split(":")
    package_path = target_parts[0]
    
    # Get the metadata to find the actual image target name
    metadata = get_app_metadata(bazel_target)
    image_target_name = metadata['image_target']
    
    # Build full image target paths
    base_image_target = f"//{package_path}:{image_target_name}"
    
    return {
        "base": base_image_target,
        "push": f"{base_image_target}_push",
    }