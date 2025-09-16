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
import re
from pathlib import Path
from typing import Dict, List, Optional


def find_workspace_root() -> Path:
    """Find the workspace root directory."""
    # When run via bazel run, BUILD_WORKSPACE_DIRECTORY is set to the workspace root
    if "BUILD_WORKSPACE_DIRECTORY" in os.environ:
        return Path(os.environ["BUILD_WORKSPACE_DIRECTORY"])
    
    # When run directly, look for workspace markers
    current = Path.cwd()
    for path in [current] + list(current.parents):
        if (path / "WORKSPACE").exists() or (path / "MODULE.bazel").exists():
            return path
    
    # As a last resort, assume current directory
    return current


def run_bazel(args: List[str], capture_output: bool = True) -> subprocess.CompletedProcess:
    """Run a bazel command with consistent configuration."""
    workspace_root = find_workspace_root()
    cmd = ["bazel"] + args
    try:
        return subprocess.run(
            cmd, 
            capture_output=capture_output, 
            text=True, 
            check=True,
            cwd=workspace_root
        )
    except subprocess.CalledProcessError as e:
        print(f"Bazel command failed: {' '.join(cmd)}", file=sys.stderr)
        print(f"Working directory: {workspace_root}", file=sys.stderr)
        if e.stderr:
            print(f"stderr: {e.stderr}", file=sys.stderr)
        if e.stdout:
            print(f"stdout: {e.stdout}", file=sys.stderr)
        raise


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


def format_git_tag(domain: str, app_name: str, version: str) -> str:
    """Format a Git tag in the domain-app-name-version format."""
    return f"{domain}-{app_name}-{version}"


def create_git_tag(tag_name: str, commit_sha: Optional[str] = None, message: Optional[str] = None) -> None:
    """Create a Git tag on the specified commit."""
    cmd = ["git", "tag"]
    
    if message:
        cmd.extend(["-a", tag_name, "-m", message])
    else:
        cmd.append(tag_name)
    
    if commit_sha:
        cmd.append(commit_sha)
    
    print(f"Creating Git tag: {tag_name}")
    subprocess.run(cmd, check=True)


def push_git_tag(tag_name: str) -> None:
    """Push a Git tag to the remote repository."""
    print(f"Pushing Git tag: {tag_name}")
    subprocess.run(["git", "push", "origin", tag_name], check=True)


def tag_and_push_image(
    app_name: str, 
    version: str, 
    commit_sha: Optional[str] = None,
    dry_run: bool = False,
    allow_overwrite: bool = False,
    create_git_tag_flag: bool = False
) -> None:
    """Tag and push container images to registry, optionally creating Git tags."""
    # Validate version before proceeding
    validate_release_version(app_name, version, allow_overwrite)
    
    metadata = get_app_metadata(app_name)
    registry = metadata["registry"]
    repo_name = metadata["repo_name"]
    domain = metadata.get("domain", "unknown")  # Fallback for backward compatibility
    
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
        
        if create_git_tag_flag:
            git_tag = format_git_tag(domain, app_name, version)
            print(f"DRY RUN: Would create Git tag: {git_tag}")
    else:
        print("Pushing to registry...")
        for tag in tags.values():
            subprocess.run(["docker", "push", tag], check=True)
        print(f"Successfully pushed {app_name} {version}")
        
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


def detect_changed_apps(since_tag: Optional[str] = None) -> List[str]:
    """Detect which apps have changed since a given tag."""
    all_apps = list_all_apps()
    
    if not since_tag:
        # No previous tag, return all apps
        return all_apps
    
    try:
        # Get changed files since the tag
        result = subprocess.run(
            ["git", "diff", "--name-only", f"{since_tag}..HEAD"],
            capture_output=True,
            text=True,
            check=True
        )
        
        changed_files = result.stdout.strip().split('\n') if result.stdout.strip() else []
        
        # Extract directories from changed files
        changed_dirs = set()
        for file_path in changed_files:
            if file_path:
                dir_name = file_path.split('/')[0]
                changed_dirs.add(dir_name)
        
        # Find apps that have changes
        changed_apps = []
        for app in all_apps:
            if app in changed_dirs:
                changed_apps.append(app)
        
        # If no apps changed but there are infrastructure changes, release all
        if not changed_apps and changed_dirs:
            # Check if changes are in infrastructure directories
            infra_dirs = {'tools', '.github', 'libs', 'docker'}
            if any(d in infra_dirs for d in changed_dirs):
                print(f"Infrastructure changes detected in: {', '.join(changed_dirs & infra_dirs)}", file=sys.stderr)
                print("Releasing all apps due to infrastructure changes", file=sys.stderr)
                return all_apps
        
        return changed_apps
        
    except subprocess.CalledProcessError as e:
        print(f"Error detecting changes since {since_tag}: {e}", file=sys.stderr)
        # On error, return all apps to be safe
        return all_apps


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


def get_previous_tag() -> Optional[str]:
    """Get the previous Git tag."""
    try:
        result = subprocess.run(
            ["git", "describe", "--tags", "--abbrev=0", "HEAD^"],
            capture_output=True,
            text=True,
            check=True
        )
        return result.stdout.strip()
    except subprocess.CalledProcessError:
        return None


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
    release_parser.add_argument("--create-git-tag", action="store_true", help="Create and push a Git tag for this release")
    
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
            tag_and_push_image(args.app, args.version, args.commit, args.dry_run, args.allow_overwrite, args.create_git_tag)
        
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
                print(f"Detecting changes since tag: {since_tag}", file=sys.stderr)
            else:
                print("No previous tag found, considering all apps as changed", file=sys.stderr)
                
            changed_apps = detect_changed_apps(since_tag)
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
