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
    """Build a container image for an app."""
    from tools.release_helper.core import run_bazel

    image_targets = get_image_targets(app_name)

    # Determine which image target to use
    if platform == "amd64":
        target = image_targets["amd64"]
    elif platform == "arm64":
        target = image_targets["arm64"]
    else:
        target = image_targets["base"]  # Default (amd64)

    print(f"Building {target}...")
    run_bazel(["build", target])

    return f"{app_name}:latest"