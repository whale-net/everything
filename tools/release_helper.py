#!/usr/bin/env python3
"""
Release helper script for the Everything monorepo.

This script provides utilities for working with release metadata and container images,
making CI/CD operations simpler and more maintainable.
"""

import json
import subprocess
import sys
import argparse
import os
from typing import Dict, List, Optional

# Import shared utilities
from shared_utils import (
    find_workspace_root, run_bazel, list_all_apps, get_app_metadata,
    validate_apps, detect_changed_files, detect_changed_apps,
    get_previous_tag, validate_semantic_version, get_image_targets,
    format_registry_tags
)


def detect_changed_apps_since_tag(since_tag: Optional[str] = None) -> List[str]:
    """Detect which apps have changed since a given tag."""
    all_apps = list_all_apps()
    
    if not since_tag:
        # No previous tag, return all apps
        return all_apps
    
    try:
        # Get changed files since the tag
        changed_files = detect_changed_files(f"{since_tag}..HEAD")
        return detect_changed_apps(changed_files)
        
    except Exception as e:
        print(f"Error detecting changes since {since_tag}: {e}")
        # On error, return all apps to be safe
        return all_apps


def build_and_load_image(app_name: str, platform: Optional[str] = None) -> str:
    """Build and load a container image for an app."""
    image_targets = get_image_targets(app_name)
    
    # Determine which tarball target to use
    if platform == "amd64":
        target = image_targets["amd64_tarball"] 
    elif platform == "arm64":
        target = image_targets["arm64_tarball"]
    else:
        target = image_targets["tarball"]  # Default (amd64)
    
    print(f"Building {target}...")
    run_bazel(["build", target])
    
    print(f"Loading image into Docker...")
    run_bazel(["run", target], capture_output=False)
    
    return f"{app_name}:latest"


def tag_and_push_image(
    app_name: str, 
    version: str, 
    commit_sha: Optional[str] = None,
    dry_run: bool = False,
    allow_overwrite: bool = False
) -> None:
    """Tag and push container images to registry."""
    # Validate version before proceeding
    validate_release_version(app_name, version, allow_overwrite)
    
    metadata = get_app_metadata(app_name)
    registry = metadata["registry"]
    repo_name = metadata["repo_name"]
    
    # Build and load the image
    original_tag = build_and_load_image(app_name)
    
    # Generate registry tags
    tags = format_registry_tags(registry, repo_name, version, commit_sha)
    
    print(f"Tagging images:")
    for tag_type, tag in tags.items():
        print(f"  - {tag}")
        subprocess.run(["docker", "tag", original_tag, tag], check=True)
    
    if dry_run:
        print("DRY RUN: Would push the following images:")
        for tag in tags.values():
            print(f"  - {tag}")
    else:
        print("Pushing to registry...")
        for tag in tags.values():
            subprocess.run(["docker", "push", tag], check=True)
        print(f"Successfully pushed {app_name} {version}")


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
                print(f"Auto-detected previous tag: {since_tag}")
        
        release_apps = detect_changed_apps_since_tag(since_tag)
    
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


def generate_release_summary(
    matrix_json: str,
    version: str,
    event_type: str,
    dry_run: bool = False,
    repository_owner: str = ""
) -> str:
    """Generate a release summary for GitHub Actions."""
    
    try:
        matrix = json.loads(matrix_json) if matrix_json else {"include": []}
    except json.JSONDecodeError:
        matrix = {"include": []}
    
    summary = []
    summary.append("## 🚀 Release Summary")
    summary.append("")
    
    if not matrix.get("include"):
        summary.append("🔍 **Result:** No apps detected for release")
    else:
        summary.append("✅ **Result:** Release completed")
        summary.append("")
        
        apps = [item["app"] for item in matrix["include"]]
        summary.append(f"📦 **Apps:** {', '.join(apps)}")
        summary.append(f"🏷️  **Version:** {version}")
        summary.append("🛠️ **System:** Consolidated Release + OCI")
        
        if event_type == "workflow_dispatch":
            summary.append("📝 **Trigger:** Manual dispatch")
            if dry_run:
                summary.append("🧪 **Mode:** Dry run (no images published)")
        else:
            summary.append("📝 **Trigger:** Git tag push")
        
        summary.append("")
        summary.append("### 🐳 Container Images")
        if dry_run:
            summary.append("**Dry run mode - no images were published**")
        else:
            summary.append("Published to GitHub Container Registry:")
            for app in apps:
                app_lower = app.lower()
                summary.append(f"- `ghcr.io/{repository_owner.lower()}/{app_lower}:{version}`")
        
        summary.append("")
        summary.append("### 🛠️ Local Development")
        summary.append("```bash")
        summary.append("# List all apps")
        summary.append("bazel run //tools:release -- list")
        summary.append("")
        summary.append("# View app metadata")
        for app in apps[:2]:  # Show first 2 apps as examples
            summary.append(f"bazel run //tools:release -- metadata {app}")
        summary.append("```")
    
    return "\n".join(summary)


def check_version_exists_in_registry(app_name: str, version: str) -> bool:
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
            print(f"Warning: Could not definitively check if {image_ref} exists: {result.stderr}")
            print("Proceeding with caution - this may overwrite an existing version")
            return False
            
    except FileNotFoundError:
        # Docker not available, skip the check
        print("Warning: Docker not available to check for existing versions")
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
        print(f"✓ Allowing overwrite of 'latest' tag for app '{app_name}' (main branch workflow)")
        return
    
    # Check if version already exists (unless explicitly allowing overwrite)
    if not allow_overwrite:
        if check_version_exists_in_registry(app_name, version):
            raise ValueError(
                f"Version '{version}' already exists for app '{app_name}'. "
                f"Refusing to overwrite existing version. Use a different version number."
            )
        else:
            print(f"✓ Version '{version}' is available for app '{app_name}'")
    else:
        print(f"⚠️  Allowing overwrite of version '{version}' for app '{app_name}' (if it exists)")


def main():
    parser = argparse.ArgumentParser(description="Release helper for Everything monorepo")
    subparsers = parser.add_subparsers(dest="command", help="Available commands")
    
    # List apps command
    list_parser = subparsers.add_parser("list", help="List all apps with release metadata")
    
    # Show metadata command
    metadata_parser = subparsers.add_parser("metadata", help="Show metadata for an app")
    metadata_parser.add_argument("app", help="App name")
    
    # Build image command
    build_parser = subparsers.add_parser("build", help="Build and load container image")
    build_parser.add_argument("app", help="App name")
    build_parser.add_argument("--platform", choices=["amd64", "arm64"], help="Target platform")
    
    # Release command
    release_parser = subparsers.add_parser("release", help="Build, tag, and push container image")
    release_parser.add_argument("app", help="App name")
    release_parser.add_argument("--version", default="latest", help="Version tag")
    release_parser.add_argument("--commit", help="Commit SHA for additional tag")
    release_parser.add_argument("--dry-run", action="store_true", help="Show what would be pushed without actually pushing")
    release_parser.add_argument("--allow-overwrite", action="store_true", help="Allow overwriting existing versions (dangerous!)")
    
    # Plan release command (for CI)
    plan_parser = subparsers.add_parser("plan", help="Plan a release and output CI matrix")
    plan_parser.add_argument("--event-type", required=True, choices=["workflow_dispatch", "tag_push"], help="Type of trigger event")
    plan_parser.add_argument("--apps", help="Comma-separated list of apps or 'all' (for manual releases)")
    plan_parser.add_argument("--version", help="Release version")
    plan_parser.add_argument("--since-tag", help="Compare changes since this tag")
    plan_parser.add_argument("--format", choices=["json", "github"], default="json", help="Output format")
    
    # Detect changes command
    changes_parser = subparsers.add_parser("changes", help="Detect changed apps since a tag")
    changes_parser.add_argument("--since-tag", help="Compare changes since this tag (defaults to previous tag)")
    
    # Validate apps command
    validate_parser = subparsers.add_parser("validate", help="Validate that apps exist")
    validate_parser.add_argument("apps", nargs="+", help="App names to validate")
    
    # Validate version command
    validate_version_parser = subparsers.add_parser("validate-version", help="Validate version format and availability")
    validate_version_parser.add_argument("app", help="App name")
    validate_version_parser.add_argument("version", help="Version to validate")
    validate_version_parser.add_argument("--allow-overwrite", action="store_true", help="Allow overwriting existing versions")
    
    # Summary command (for CI)
    summary_parser = subparsers.add_parser("summary", help="Generate release summary for GitHub Actions")
    summary_parser.add_argument("--matrix", required=True, help="Release matrix JSON")
    summary_parser.add_argument("--version", required=True, help="Release version")
    summary_parser.add_argument("--event-type", required=True, choices=["workflow_dispatch", "tag_push"], help="Event type")
    summary_parser.add_argument("--dry-run", action="store_true", help="Whether this was a dry run")
    summary_parser.add_argument("--repository-owner", default="", help="GitHub repository owner")
    
    args = parser.parse_args()
    
    if not args.command:
        parser.print_help()
        return
    
    try:
        if args.command == "list":
            apps = list_all_apps()
            for app in apps:
                print(app)
        
        elif args.command == "metadata":
            metadata = get_app_metadata(args.app)
            print(json.dumps(metadata, indent=2))
        
        elif args.command == "build":
            image_tag = build_and_load_image(args.app, args.platform)
            print(f"Image loaded as: {image_tag}")
        
        elif args.command == "release":
            tag_and_push_image(args.app, args.version, args.commit, args.dry_run, args.allow_overwrite)
        
        elif args.command == "plan":
            plan = plan_release(
                event_type=args.event_type,
                requested_apps=args.apps,
                version=args.version,
                since_tag=args.since_tag
            )
            
            if args.format == "github":
                # Output GitHub Actions format
                matrix_json = json.dumps(plan["matrix"])
                print(f"matrix={matrix_json}")
                if plan["apps"]:
                    print(f"apps={' '.join(plan['apps'])}")
                else:
                    print("apps=")
            else:
                # JSON output
                print(json.dumps(plan, indent=2))
        
        elif args.command == "changes":
            since_tag = args.since_tag or get_previous_tag()
            if since_tag:
                print(f"Detecting changes since tag: {since_tag}")
            else:
                print("No previous tag found, considering all apps as changed")
                
            changed_apps = detect_changed_apps_since_tag(since_tag)
            for app in changed_apps:
                print(app)
        
        elif args.command == "validate":
            try:
                valid_apps = validate_apps(args.apps)
                print(f"All apps are valid: {', '.join(valid_apps)}")
            except ValueError as e:
                print(f"Validation failed: {e}", file=sys.stderr)
                sys.exit(1)
        
        elif args.command == "validate-version":
            try:
                validate_release_version(args.app, args.version, args.allow_overwrite)
                print(f"✓ Version '{args.version}' is valid for app '{args.app}'")
            except ValueError as e:
                print(f"Version validation failed: {e}", file=sys.stderr)
                sys.exit(1)
        
        elif args.command == "summary":
            summary = generate_release_summary(
                matrix_json=args.matrix,
                version=args.version,
                event_type=args.event_type,
                dry_run=args.dry_run,
                repository_owner=args.repository_owner
            )
            print(summary)
    
    except Exception as e:
        print(f"Error: {e}", file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()
