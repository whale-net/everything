"""
App metadata utilities for the release helper.
"""

import json
from pathlib import Path
from typing import Dict, List

from .core import run_bazel, find_workspace_root


def get_app_metadata(app_name: str) -> Dict:
    """Get release metadata for an app by building and reading its metadata target."""
    metadata_target = f"//{app_name}:{app_name}_metadata"

    # Build the metadata target
    run_bazel(["build", metadata_target])

    # Read the generated JSON file
    workspace_root = find_workspace_root()
    metadata_file = workspace_root / f"bazel-bin/{app_name}/{app_name}_metadata_metadata.json"
    if not metadata_file.exists():
        raise FileNotFoundError(f"Metadata file not found: {metadata_file}")

    with open(metadata_file) as f:
        return json.load(f)


def list_all_apps() -> List[str]:
    """List all apps in the monorepo that have release metadata."""
    # Query for all metadata targets
    result = run_bazel(["query", "kind(app_metadata, //...)", "--output=label"])

    apps = []
    for line in result.stdout.strip().split('\n'):
        if line and '_metadata' in line:
            # Extract app name from target like "//hello_python:hello_python_metadata"
            parts = line.split(':')
            if len(parts) == 2:
                target_name = parts[1]
                if target_name.endswith('_metadata'):
                    app_name = target_name[:-9]  # Remove "_metadata" suffix
                    apps.append(app_name)

    return sorted(apps)


def get_image_targets(app_name: str) -> Dict[str, str]:
    """Get all image-related targets for an app."""
    base_name = f"{app_name}_image"
    return {
        "base": f"//{app_name}:{base_name}",
        "tarball": f"//{app_name}:{base_name}_tarball",
        "amd64": f"//{app_name}:{base_name}_amd64",
        "arm64": f"//{app_name}:{base_name}_arm64",
        "amd64_tarball": f"//{app_name}:{base_name}_amd64_tarball",
        "arm64_tarball": f"//{app_name}:{base_name}_arm64_tarball",
    }