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
        print(f"Bazel command failed: {' '.join(cmd)}")
        print(f"Working directory: {workspace_root}")
        if e.stderr:
            print(f"stderr: {e.stderr}")
        if e.stdout:
            print(f"stdout: {e.stdout}")
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


def tag_and_push_image(
    app_name: str, 
    version: str, 
    commit_sha: Optional[str] = None,
    dry_run: bool = False
) -> None:
    """Tag and push container images to registry."""
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
            tag_and_push_image(args.app, args.version, args.commit, args.dry_run)
    
    except Exception as e:
        print(f"Error: {e}", file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()
