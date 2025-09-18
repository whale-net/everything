"""
Validation utilities for the release helper.
"""

import os
import re
import subprocess
import sys
from typing import Dict, List

from tools.release_helper.metadata import get_app_metadata, list_all_apps


def validate_semantic_version(version: str) -> bool:
    """Validate that version follows semantic versioning format v{major}.{minor}.{patch}."""
    # Match semantic version pattern: v followed by major.minor.patch
    # Allow optional pre-release suffix like -alpha, -beta, -rc1, etc.
    pattern = r'^v(\d+)\.(\d+)\.(\d+)(?:-[a-zA-Z0-9\-\.]+)?$'
    return bool(re.match(pattern, version))


def check_version_exists_in_registry(bazel_target: str, version: str) -> bool:
    """Check if a version already exists in the container registry.
    
    Args:
        bazel_target: Full bazel target path for the app metadata
        version: Version to check
    """
    metadata = get_app_metadata(bazel_target)
    registry = metadata["registry"]
    domain = metadata["domain"]
    app_name = metadata["name"]

    # Build the image reference using domain-app:version format
    image_name = f"{domain}-{app_name}"
    
    if registry == "ghcr.io" and "GITHUB_REPOSITORY_OWNER" in os.environ:
        owner = os.environ["GITHUB_REPOSITORY_OWNER"].lower()
        image_ref = f"{registry}/{owner}/{image_name}:{version}"
    else:
        image_ref = f"{registry}/{image_name}:{version}"

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


def validate_release_version(bazel_target: str, version: str, allow_overwrite: bool = False) -> None:
    """Validate that a release version is valid and doesn't already exist.
    
    Args:
        bazel_target: Full bazel target path for the app metadata
        version: Version to validate
        allow_overwrite: Whether to allow overwriting existing versions
    """
    metadata = get_app_metadata(bazel_target)
    app_name = metadata['name']
    
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
        if check_version_exists_in_registry(bazel_target, version):
            raise ValueError(
                f"Version '{version}' already exists for app '{app_name}'. "
                f"Refusing to overwrite existing version. Use a different version number."
            )
        else:
            print(f"✓ Version '{version}' is available for app '{app_name}'", file=sys.stderr)
    else:
        print(f"⚠️  Allowing overwrite of version '{version}' for app '{app_name}' (if it exists)", file=sys.stderr)


def _get_app_full_name(app: Dict[str, str]) -> str:
    """Get the full domain-appname format for an app."""
    return f"{app['domain']}-{app['name']}"


def validate_apps(requested_apps: List[str]) -> List[Dict[str, str]]:
    """Validate that requested apps exist and return the valid ones.
    
    Apps can be referenced in multiple formats:
    - Full format: domain-appname (e.g., "demo-hello_python")
    - Short format: appname (e.g., "hello_python") - only if unambiguous
    - Path format: domain/appname (e.g., "demo/hello_python")
    
    Args:
        requested_apps: List of app names to validate
        
    Returns:
        List of app dictionaries with bazel_target, name, and domain
    """
    all_apps = list_all_apps()
    
    # Create multiple lookup tables for different app reference formats
    full_name_lookup = {}  # domain-name -> app
    short_name_lookup = {}  # name -> [apps] (may have multiple for same name)
    path_lookup = {}  # domain/name -> app
    
    for app in all_apps:
        domain = app['domain']
        name = app['name']
        
        # Full format: domain-name
        full_name = _get_app_full_name(app)
        full_name_lookup[full_name] = app
        
        # Path format: domain/name
        path_name = f"{domain}/{name}"
        path_lookup[path_name] = app
        
        # Short format: name (may have collisions)
        if name not in short_name_lookup:
            short_name_lookup[name] = []
        short_name_lookup[name].append(app)

    valid_apps = []
    invalid_apps = []

    for requested_app in requested_apps:
        app = None
        
        # Try full format first (domain-name)
        if requested_app in full_name_lookup:
            app = full_name_lookup[requested_app]
        # Try path format (domain/name)
        elif requested_app in path_lookup:
            app = path_lookup[requested_app]
        # Try short format (name only) - only if unambiguous
        elif requested_app in short_name_lookup:
            matching_apps = short_name_lookup[requested_app]
            if len(matching_apps) == 1:
                app = matching_apps[0]
            else:
                # Multiple apps with same name - show all options
                ambiguous_apps = [_get_app_full_name(a) for a in matching_apps]
                invalid_apps.append(f"{requested_app} (ambiguous, could be: {', '.join(ambiguous_apps)})")
                continue
        
        if app:
            valid_apps.append(app)
        else:
            invalid_apps.append(requested_app)

    if invalid_apps:
        # Show available apps in full format for consistency
        available_full = sorted(_get_app_full_name(app) for app in all_apps)
        available_display = ", ".join(available_full)
        invalid = ", ".join(invalid_apps)
        raise ValueError(
            f"Invalid apps: {invalid}.\n"
            f"Available apps: {available_display}\n"
            f"You can use: full format (domain-appname, e.g. demo-hello_python), "
            f"path format (domain/appname, e.g. demo/hello_python), or short format (appname, e.g. hello_python, if unambiguous)"
        )

    return valid_apps