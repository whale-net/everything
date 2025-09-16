"""
Validation utilities for the release helper.
"""

import os
import re
import subprocess
import sys
from typing import List

from tools.release_helper.metadata import get_app_metadata, list_all_apps


def validate_semantic_version(version: str) -> bool:
    """Validate that version follows semantic versioning format v{major}.{minor}.{patch}."""
    # Match semantic version pattern: v followed by major.minor.patch
    # Allow optional pre-release suffix like -alpha, -beta, -rc1, etc.
    pattern = r'^v(\d+)\.(\d+)\.(\d+)(?:-[a-zA-Z0-9\-\.]+)?$'
    return bool(re.match(pattern, version))


def check_version_exists_in_registry(app_name: str, version: str) -> bool:
    """Check if a version already exists in the container registry."""
    metadata = get_app_metadata(app_name)
    registry = metadata["registry"]
    repo_name = metadata["repo_name"].lower()

    # Build the image reference to check
    if registry == "ghcr.io" and "GITHUB_REPOSITORY_OWNER" in os.environ:
        owner = os.environ["GITHUB_REPOSITORY_OWNER"].lower()
        image_ref = f"{registry}/{owner}/{repo_name}:{version}"
    else:
        image_ref = f"{registry}/{repo_name}:{version}"

    try:
        # Try to pull the image manifest to check if it exists
        # Use docker manifest inspect which doesn't download the image
        result = subprocess.run(
            ["docker", "manifest", "inspect", image_ref],
            capture_output=True,
            text=True,
            check=False  # Don't raise exception on non-zero exit
        )

        if result.returncode == 0:
            return True  # Image exists
        elif any(phrase in result.stderr.lower() for phrase in ["manifest unknown", "not found", "name invalid", "unauthorized"]):
            # These errors typically mean the image doesn't exist or we don't have access
            # In CI with proper credentials, "unauthorized" shouldn't happen for existing images
            return False  # Image doesn't exist or we can't access it (assume it doesn't exist)
        else:
            # Some other error occurred, be conservative and assume it exists
            print(f"Warning: Could not definitively check if {image_ref} exists: {result.stderr}", file=sys.stderr)
            print("Proceeding with caution - this may overwrite an existing version", file=sys.stderr)
            return False

    except FileNotFoundError:
        # Docker not available, skip the check
        print("Warning: Docker not available to check for existing versions", file=sys.stderr)
        return False


def validate_release_version(app_name: str, version: str, allow_overwrite: bool = False) -> None:
    """Validate that a release version is valid and doesn't already exist."""
    # Check semantic versioning (skip for "latest" which is always valid for main builds)
    if version != "latest" and not validate_semantic_version(version):
        raise ValueError(
            f"Version '{version}' does not follow semantic versioning format. "
            f"Expected format: v{{major}}.{{minor}}.{{patch}} (e.g., v1.0.0, v2.1.3, v1.0.0-beta1)"
        )

    # Automatically allow overwriting for "latest" version (main branch workflow)
    # or when explicitly allowing overwrite
    if version == "latest":
        print(f"✓ Allowing overwrite of 'latest' tag for app '{app_name}' (main branch workflow)", file=sys.stderr)
        return

    # Check if version already exists (unless explicitly allowing overwrite)
    if not allow_overwrite:
        if check_version_exists_in_registry(app_name, version):
            raise ValueError(
                f"Version '{version}' already exists for app '{app_name}'. "
                f"Refusing to overwrite existing version. Use a different version number."
            )
        else:
            print(f"✓ Version '{version}' is available for app '{app_name}'", file=sys.stderr)
    else:
        print(f"⚠️  Allowing overwrite of version '{version}' for app '{app_name}' (if it exists)", file=sys.stderr)


def validate_apps(requested_apps: List[str]) -> List[str]:
    """Validate that requested apps exist and return the valid ones."""
    all_apps = list_all_apps()

    valid_apps = []
    invalid_apps = []

    for app in requested_apps:
        if app in all_apps:
            valid_apps.append(app)
        else:
            invalid_apps.append(app)

    if invalid_apps:
        available = ", ".join(sorted(all_apps))
        invalid = ", ".join(invalid_apps)
        raise ValueError(f"Invalid apps: {invalid}. Available apps: {available}")

    return valid_apps