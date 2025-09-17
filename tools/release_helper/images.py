"""
Image building and tagging utilities for the release helper.
"""

import os
import subprocess
from typing import Dict, Optional

from tools.release_helper.metadata import get_image_targets


def format_registry_tags(registry: str, repo_name: str, version: str, commit_sha: Optional[str] = None) -> Dict[str, str]:
    """Format container registry tags for an app."""
    repo_lower = repo_name.lower()

    # For GHCR, include the repository owner
    if registry == "ghcr.io" and "GITHUB_REPOSITORY_OWNER" in os.environ:
        owner = os.environ["GITHUB_REPOSITORY_OWNER"].lower()
        base_repo = f"{registry}/{owner}/{repo_lower}"
    else:
        base_repo = f"{registry}/{repo_lower}"

    tags = {
        "latest": f"{base_repo}:latest",
        "version": f"{base_repo}:{version}",
    }

    if commit_sha:
        tags["commit"] = f"{base_repo}:{commit_sha}"

    return tags


def build_image(app_name: str, platform: Optional[str] = None) -> str:
    """Build and load a container image for an app using optimized oci_load targets."""
    from tools.release_helper.core import run_bazel

    image_targets = get_image_targets(app_name)

    # Determine which image target to use (prefer oci_load targets for efficiency)
    if platform == "amd64":
        # Use oci_load target which is more efficient than direct image building
        load_target = image_targets["amd64"] + "_load"
        target = load_target
    elif platform == "arm64":
        load_target = image_targets["arm64"] + "_load"
        target = load_target
    else:
        load_target = image_targets["base"] + "_load"
        target = load_target

    print(f"Building and loading {target} (using optimized oci_load)...")
    # Build the image first to create the OCI layout
    image_target = target.replace("_load", "")
    run_bazel(["build", image_target])

    return f"{app_name}:latest"