"""
Image building and tagging utilities for the release helper.
"""

import os
import subprocess
from typing import Dict, List, Optional

from tools.release_helper.metadata import get_image_targets, get_app_metadata
from tools.release_helper.core import run_bazel


def format_registry_tags(domain: str, app_name: str, version: str, registry: str = "ghcr.io", commit_sha: Optional[str] = None) -> Dict[str, str]:
    """Format container registry tags for an app using domain-app:version format.
    
    Args:
        domain: App domain (from metadata)
        app_name: App name (from metadata)
        version: Version tag (semantic version)
        registry: Registry hostname (e.g., "ghcr.io")
        commit_sha: Optional commit SHA for additional tag
    """
    # Use domain-app:version format as specified
    image_name = f"{domain}-{app_name}"
    
    # For GHCR, include the repository owner
    if registry == "ghcr.io" and "GITHUB_REPOSITORY_OWNER" in os.environ:
        owner = os.environ["GITHUB_REPOSITORY_OWNER"].lower()
        base_repo = f"{registry}/{owner}/{image_name}"
    else:
        base_repo = f"{registry}/{image_name}"

    tags = {
        "latest": f"{base_repo}:latest",
        "version": f"{base_repo}:{version}",
    }

    if commit_sha:
        tags["commit"] = f"{base_repo}:{commit_sha}"

    return tags


def build_image(bazel_target: str, platform: Optional[str] = None) -> str:
    """Build and load a container image for an app using optimized oci_load targets.
    
    Args:
        bazel_target: Full bazel target path for the app metadata (e.g., "//path/to/app:app_metadata")
        platform: Optional platform specification ("amd64" or "arm64")
    """
    from tools.release_helper.core import run_bazel

    image_targets = get_image_targets(bazel_target)
    
    # Get app metadata for proper naming
    metadata = get_app_metadata(bazel_target)
    domain = metadata['domain']
    app_name = metadata['name']

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
    
    # Actually load the image into Docker with the correct name
    run_bazel(["run", target])

    # Return the expected image name in domain-app format
    return f"{domain}-{app_name}:latest"


def push_image_with_tags(bazel_target: str, tags: List[str], platform: Optional[str] = None) -> None:
    """Push a container image with multiple tags to the registry.
    
    Args:
        bazel_target: Full bazel target path for the app metadata
        tags: List of full registry tags to push (e.g., ["ghcr.io/whale-net/demo-hello_python:v0.0.6"])
        platform: Optional platform specification ("amd64" or "arm64", defaults to base/amd64)
    """
    image_targets = get_image_targets(bazel_target)
    
    # Determine which push target to use based on platform
    if platform == "amd64":
        push_target = image_targets["push_amd64"]
    elif platform == "arm64":
        push_target = image_targets["push_arm64"]
    else:
        # Default to the base push target (which is AMD64)
        push_target = image_targets["push_base"]

    print(f"Pushing {len(tags)} tags using {push_target}...")
    
    # The oci_push target is already configured with the repository via the macro
    # We just need to add any additional tags as arguments
    # Extract the tag names from the full tags
    tag_names = [tag.split(':')[-1] for tag in tags]
    
    print(f"Pushing with tags: {', '.join(tag_names)}")
    
    # Build the bazel run command with tag arguments
    bazel_args = ["run", push_target, "--"]
    
    # Add each tag as an argument (oci_push supports multiple --tag arguments)
    for tag_name in tag_names:
        bazel_args.extend(["--tag", tag_name])
    
    try:
        run_bazel(bazel_args, capture_output=False)  # Don't capture output so we can see progress
        print(f"Successfully pushed image with {len(tag_names)} tags")
    except subprocess.CalledProcessError as e:
        print(f"Failed to push image: {e}")
        raise