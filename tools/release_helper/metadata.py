"""
App metadata utilities for the release helper.
"""

import json
from pathlib import Path
from typing import Dict, List

from tools.release_helper.core import run_bazel, find_workspace_root

# In-process cache for app metadata to avoid redundant Bazel builds
_metadata_cache: Dict[str, Dict] = {}


def _read_metadata_file(bazel_target: str) -> Dict:
    """Read a metadata JSON file from bazel-bin without building.
    
    Args:
        bazel_target: Full bazel target path (e.g., "//path/to/app:app_metadata")
        
    Returns:
        Parsed metadata dict, or None if file doesn't exist
    """
    if not bazel_target.startswith("//"):
        raise ValueError(f"Invalid bazel target format: {bazel_target}")
    
    target_parts = bazel_target[2:].split(":")
    if len(target_parts) != 2:
        raise ValueError(f"Invalid bazel target format: {bazel_target}")
    
    package_path = target_parts[0]
    target_name = target_parts[1]

    workspace_root = find_workspace_root()
    metadata_file = workspace_root / f"bazel-bin/{package_path}/{target_name}_metadata.json"
    if not metadata_file.exists():
        return None

    with open(metadata_file) as f:
        return json.load(f)


def get_app_metadata(bazel_target: str) -> Dict:
    """Get release metadata for an app by building and reading its metadata target.
    
    Results are cached in-process to avoid redundant Bazel invocations.
    If the metadata file already exists on disk (e.g., from a batch build),
    it is read directly without invoking Bazel.
    
    Args:
        bazel_target: Full bazel target path (e.g., "//path/to/app:app_metadata")
    """
    if bazel_target in _metadata_cache:
        return _metadata_cache[bazel_target]

    # Try reading from disk first (may already be built by a batch build)
    metadata = _read_metadata_file(bazel_target)
    if metadata is None:
        # Build the metadata target
        run_bazel(["build", bazel_target])
        metadata = _read_metadata_file(bazel_target)
        if metadata is None:
            raise FileNotFoundError(f"Metadata file not found after building {bazel_target}")
    
    _metadata_cache[bazel_target] = metadata
    return metadata


def list_all_apps() -> List[Dict[str, str]]:
    """List all apps in the monorepo that have release metadata.
    
    Batch-builds all metadata targets in a single Bazel invocation to avoid
    repeated analysis overhead.
    
    Returns:
        List of dicts with full metadata for each app
    """
    # Query for all metadata targets
    result = run_bazel(["query", "kind(app_metadata, //...)", "--output=label"])

    targets = [line for line in result.stdout.strip().split('\n') if line and '_metadata' in line]
    
    if not targets:
        return []
    
    # Batch-build all metadata targets in a single Bazel invocation
    # This avoids N separate Bazel analysis phases
    run_bazel(["build"] + targets)

    apps = []
    for target in targets:
        try:
            # get_app_metadata will read from disk (already built) and cache the result
            metadata = get_app_metadata(target)
            metadata['bazel_target'] = target
            apps.append(metadata)
        except Exception as e:
            print(f"Warning: Could not get metadata for {target}: {e}")
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