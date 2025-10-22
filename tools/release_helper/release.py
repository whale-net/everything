"""
Release planning and execution utilities for the release helper.
"""

import json
import subprocess
import sys
from typing import Dict, List, Optional

from tools.release_helper.changes import detect_changed_apps
from tools.release_helper.git import auto_increment_version, create_git_tag, format_git_tag, get_previous_tag, push_git_tag
from tools.release_helper.images import build_image, format_registry_tags, push_image_with_tags
from tools.release_helper.metadata import get_app_metadata, list_all_apps
from tools.release_helper.validation import validate_apps, validate_release_version, validate_semantic_version


def find_app_bazel_target(app_name: str) -> str:
    """Find the bazel target for an app by name.
    
    Supports multiple naming formats:
    - Full format: domain-appname (e.g., "demo-hello_python") - RECOMMENDED
    - Path format: domain/appname (e.g., "demo/hello_python")
    - Short format: appname (e.g., "hello_python") - only if unambiguous
    
    Args:
        app_name: Name of the app to find (supports multiple formats)
        
    Returns:
        Full bazel target path for the app's metadata
        
    Raises:
        ValueError: If app not found or name is ambiguous
    """
    # Use validate_apps which already handles all naming formats and ambiguity
    try:
        validated_apps = validate_apps([app_name])
        if len(validated_apps) == 1:
            return validated_apps[0]['bazel_target']
        elif len(validated_apps) > 1:
            # This shouldn't happen as validate_apps handles ambiguity, but be safe
            raise ValueError(f"Multiple apps matched '{app_name}': {[app['name'] for app in validated_apps]}")
        else:
            raise ValueError(f"App '{app_name}' not found")
    except ValueError as e:
        # Re-raise with original error message from validate_apps
        raise ValueError(str(e))


def plan_release(
    event_type: str,
    requested_apps: Optional[str] = None,
    version: Optional[str] = None,
    version_mode: Optional[str] = None,
    base_commit: Optional[str] = None,
    include_demo: bool = False
) -> Dict:
    """Plan a release and return the matrix configuration for CI."""

    # Validate version format if provided
    if version and version != "latest":
        if not validate_semantic_version(version):
            raise ValueError(
                f"Version '{version}' does not follow semantic versioning format. "
                f"Expected format: v{{major}}.{{minor}}.{{patch}} (e.g., v1.0.0, v2.1.3, v1.0.0-beta1)"
            )

    if event_type == "workflow_dispatch":
        # Manual release
        if not requested_apps:
            raise ValueError("Manual releases require apps to be specified")
        
        # Version validation based on mode
        if version_mode == "specific":
            if not version:
                raise ValueError("Specific version mode requires version to be specified")
        elif version_mode in ["increment_minor", "increment_patch"]:
            if version:
                raise ValueError(f"Version should not be specified when using {version_mode} mode")
        elif not version_mode:
            # Legacy mode - require version
            if not version:
                raise ValueError("Manual releases require version to be specified (or use --increment-minor/--increment-patch)")

        if requested_apps == "all":
            release_apps = list_all_apps()
            # Exclude demo domain unless explicitly included
            if not include_demo:
                release_apps = [app for app in release_apps if app['domain'] != 'demo']
                print("Excluding demo domain apps from 'all' (use --include-demo to include)", file=sys.stderr)
        else:
            requested = [app.strip() for app in requested_apps.split(',')]
            release_apps = validate_apps(requested)
            
        # For increment modes, calculate versions for each app
        if version_mode in ["increment_minor", "increment_patch"]:
            increment_type = version_mode.replace("increment_", "")
            for app in release_apps:
                metadata = get_app_metadata(app['bazel_target'])
                app_version = auto_increment_version(metadata['domain'], metadata['name'], increment_type)
                app['version'] = app_version
                print(f"Auto-incremented {metadata['domain']}/{metadata['name']} to {app_version}", file=sys.stderr)
        else:
            # Use provided version for all apps
            for app in release_apps:
                app['version'] = version

    elif event_type == "tag_push":
        # Automatic release based on changes
        if not version:
            raise ValueError("Tag push releases require version to be specified")

        # Auto-detect previous tag if no base commit provided
        if base_commit is None:
            base_commit = get_previous_tag()
            if base_commit:
                print(f"Auto-detected previous tag: {base_commit}", file=sys.stderr)

        release_apps = detect_changed_apps(base_commit)
        
        # For tag push, use the provided version for all apps
        for app in release_apps:
            app['version'] = version

    elif event_type in ["pull_request", "push", "fallback"]:
        # CI builds - detect changed apps
        if event_type == "fallback" or base_commit is None:
            # Fallback: build all apps
            print("Fallback mode: building all apps", file=sys.stderr)
            release_apps = list_all_apps()
        else:
            # Detect changed apps using the provided base commit
            print(f"CI build: detecting changes against {base_commit}", file=sys.stderr)
            release_apps = detect_changed_apps(base_commit)

    else:
        raise ValueError(f"Unknown event type: {event_type}")

    # Create matrix
    if not release_apps:
        matrix = {"include": []}
    else:
        matrix = {
            "include": [
                {
                    "app": app["name"], 
                    "domain": app["domain"],
                    "bazel_target": app["bazel_target"],
                    "version": app.get("version", version),
                    "domain": app["domain"]
                } 
                for app in release_apps
            ]
        }

    return {
        "matrix": matrix,
        "apps": [f"{app['domain']}-{app['name']}" for app in release_apps],  # Return full domain-name format to avoid ambiguity
        "version": version,  # For legacy compatibility, may be None for increment modes
        "versions": {f"{app['domain']}-{app['name']}": app.get("version", version) for app in release_apps} if release_apps else {},  # Individual app versions with domain-app keys
        "event_type": event_type
    }


def tag_and_push_image(
    app_name: str,
    version: str,
    commit_sha: Optional[str] = None,
    dry_run: bool = False,
    allow_overwrite: bool = False,
    create_git_tag_flag: bool = False
) -> None:
    """Build and push container images to registry, optionally creating Git tags.
    
    Args:
        app_name: Name of the app (will be converted to bazel target)
        version: Version to release
        commit_sha: Optional commit SHA for tagging
        dry_run: Whether to perform a dry run
        allow_overwrite: Whether to allow overwriting existing versions
        create_git_tag_flag: Whether to create a git tag
    """
    # Find the bazel target for this app
    bazel_target = find_app_bazel_target(app_name)
    
    # Validate version before proceeding
    validate_release_version(bazel_target, version, allow_overwrite)

    metadata = get_app_metadata(bazel_target)
    registry = metadata["registry"]
    domain = metadata["domain"]
    actual_app_name = metadata["name"]

    # Build the image (but don't load into Docker)
    build_image(bazel_target)

    # Generate registry tags using the new domain-app:version format
    tags = format_registry_tags(domain, actual_app_name, version, registry, commit_sha)

    if dry_run:
        print("DRY RUN: Would push the following images:")
        for tag in tags.values():
            print(f"  - {tag}")

        if create_git_tag_flag:
            git_tag = format_git_tag(domain, actual_app_name, version)
            print(f"DRY RUN: Would create Git tag: {git_tag}")
    else:
        print("Pushing to registry...")
        
        # Collect all tags to push
        tag_list = list(tags.values())
        
        # Push the image with all tags
        try:
            push_image_with_tags(bazel_target, tag_list)
            print(f"Successfully pushed {actual_app_name} {version}")
        except Exception as e:
            print(f"Failed to push {actual_app_name} {version}: {e}", file=sys.stderr)
            raise

        # Create and push Git tag if requested
        if create_git_tag_flag:
            git_tag = format_git_tag(domain, actual_app_name, version)
            tag_message = f"Release {actual_app_name} {version}"

            try:
                create_git_tag(git_tag, commit_sha, tag_message)
                push_git_tag(git_tag)
                print(f"Successfully created and pushed Git tag: {git_tag}")
            except subprocess.CalledProcessError as e:
                print(f"Warning: Failed to create/push Git tag {git_tag}: {e}", file=sys.stderr)
                # Don't fail the entire release if Git tagging fails