"""
Release planning and execution utilities for the release helper.
"""

import json
import subprocess
from typing import Dict, List, Optional

from tools.release_helper.changes import detect_changed_apps
from tools.release_helper.git import create_git_tag, format_git_tag, get_previous_tag, push_git_tag
from tools.release_helper.images import build_image, format_registry_tags
from tools.release_helper.metadata import get_app_metadata, list_all_apps
from tools.release_helper.validation import validate_apps, validate_release_version, validate_semantic_version


def plan_release(
    event_type: str,
    requested_apps: Optional[str] = None,
    version: Optional[str] = None,
    since_tag: Optional[str] = None
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
        if not requested_apps or not version:
            raise ValueError("Manual releases require apps and version to be specified")

        if requested_apps == "all":
            release_apps = list_all_apps()
        else:
            requested = [app.strip() for app in requested_apps.split(',')]
            release_apps = validate_apps(requested)

    elif event_type == "tag_push":
        # Automatic release based on changes
        if not version:
            raise ValueError("Tag push releases require version to be specified")

        # Auto-detect previous tag if not provided
        if since_tag is None:
            since_tag = get_previous_tag()
            if since_tag:
                print(f"Auto-detected previous tag: {since_tag}", file=sys.stderr)

        release_apps = detect_changed_apps(since_tag)

    else:
        raise ValueError(f"Unknown event type: {event_type}")

    # Create matrix
    if not release_apps:
        matrix = {"include": []}
    else:
        matrix = {
            "include": [{"app": app} for app in release_apps]
        }

    return {
        "matrix": matrix,
        "apps": release_apps,
        "version": version,
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
    """Build and push container images to registry, optionally creating Git tags."""
    # Validate version before proceeding
    validate_release_version(app_name, version, allow_overwrite)

    metadata = get_app_metadata(app_name)
    registry = metadata["registry"]
    repo_name = metadata["repo_name"]
    domain = metadata.get("domain", "unknown")  # Fallback for backward compatibility

    # Build the image (but don't load into Docker)
    build_image(app_name)

    # Generate registry tags
    tags = format_registry_tags(registry, repo_name, version, commit_sha)

    if dry_run:
        print("DRY RUN: Would push the following images:")
        for tag in tags.values():
            print(f"  - {tag}")

        if create_git_tag_flag:
            git_tag = format_git_tag(domain, app_name, version)
            print(f"DRY RUN: Would create Git tag: {git_tag}")
    else:
        print("Pushing to registry...")
        # TODO: Implement direct push from OCI image using oci_push or equivalent tooling
        # For now, this is a placeholder - you may want to implement this using:
        # 1. bazel run with oci_push targets (if you add them to BUILD files)
        # 2. Direct use of crane/skopeo to push from bazel-bin OCI layout
        # 3. Custom Bazel rule that handles the push
        
        for tag_type, tag in tags.items():
            print(f"TODO: Push {tag}")
        
        print(f"Successfully would push {app_name} {version}")

        # Create and push Git tag if requested
        if create_git_tag_flag:
            git_tag = format_git_tag(domain, app_name, version)
            tag_message = f"Release {app_name} {version}"

            try:
                create_git_tag(git_tag, commit_sha, tag_message)
                push_git_tag(git_tag)
                print(f"Successfully created and pushed Git tag: {git_tag}")
            except subprocess.CalledProcessError as e:
                print(f"Warning: Failed to create/push Git tag {git_tag}: {e}", file=sys.stderr)
                # Don't fail the entire release if Git tagging fails