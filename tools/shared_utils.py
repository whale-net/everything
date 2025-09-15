#!/usr/bin/env python3
"""
Shared utilities for the Everything monorepo tools.

This module provides common functionality used by both the release helper
and test helper to avoid code duplication.
"""

import json
import subprocess
import sys
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
        print(f"Bazel command failed: {' '.join(cmd)}")
        print(f"Working directory: {workspace_root}")
        if e.stderr:
            print(f"stderr: {e.stderr}")
        if e.stdout:
            print(f"stdout: {e.stdout}")
        raise


def run_git(args: List[str], capture_output: bool = True) -> subprocess.CompletedProcess:
    """Run a git command with consistent configuration."""
    workspace_root = find_workspace_root()
    cmd = ["git"] + args
    try:
        return subprocess.run(
            cmd,
            capture_output=capture_output,
            text=True,
            check=True,
            cwd=workspace_root
        )
    except subprocess.CalledProcessError as e:
        print(f"Git command failed: {' '.join(cmd)}")
        print(f"Working directory: {workspace_root}")
        if e.stderr:
            print(f"stderr: {e.stderr}")
        if e.stdout:
            print(f"stdout: {e.stdout}")
        raise


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


def detect_changed_files(since_commit: str) -> List[str]:
    """Detect files changed since a given commit."""
    try:
        result = run_git(["diff", "--name-only", f"{since_commit}..HEAD"])
        return [line.strip() for line in result.stdout.strip().split('\n') if line.strip()]
    except subprocess.CalledProcessError as e:
        print(f"Error detecting changes since {since_commit}: {e}")
        return []


def detect_changed_apps(changed_files: List[str]) -> List[str]:
    """Detect which apps have changed based on modified files."""
    all_apps = list_all_apps()
    changed_dirs = set()

    # Extract top-level directories from changed files
    for file_path in changed_files:
        if file_path:
            parts = file_path.split('/')
            if len(parts) > 0:
                changed_dirs.add(parts[0])

    # Find apps that have changes
    changed_apps = []
    for app in all_apps:
        if app in changed_dirs:
            changed_apps.append(app)

    # Check for infrastructure changes that affect all apps
    infra_dirs = {'tools', '.github', 'libs', 'docker', 'MODULE.bazel', 'BUILD.bazel'}
    if not changed_apps and any(d in infra_dirs for d in changed_dirs):
        print(f"🔧 Infrastructure changes detected in: {', '.join(changed_dirs & infra_dirs)}")
        print("Releasing all apps due to infrastructure changes")
        return all_apps

    return changed_apps


def get_previous_commit() -> Optional[str]:
    """Get the previous commit on current branch."""
    try:
        result = run_git(["rev-parse", "HEAD~1"])
        return result.stdout.strip()
    except subprocess.CalledProcessError:
        print("Warning: Could not get previous commit")
        return None


def get_previous_tag() -> Optional[str]:
    """Get the previous Git tag."""
    try:
        result = run_git(["describe", "--tags", "--abbrev=0", "HEAD^"])
        return result.stdout.strip()
    except subprocess.CalledProcessError:
        return None


def get_base_commit_for_pr() -> Optional[str]:
    """Get the base commit for a PR (merge base with main)."""
    try:
        # Try to get the merge base with main branch
        result = run_git(["merge-base", "HEAD", "origin/main"])
        return result.stdout.strip()
    except subprocess.CalledProcessError:
        try:
            # Fallback to main branch directly
            result = run_git(["rev-parse", "origin/main"])
            return result.stdout.strip()
        except subprocess.CalledProcessError:
            print("Warning: Could not determine base commit for PR")
            return None


def validate_semantic_version(version: str) -> bool:
    """Validate that version follows semantic versioning format v{major}.{minor}.{patch}."""
    # Match semantic version pattern: v followed by major.minor.patch
    # Allow optional pre-release suffix like -alpha, -beta, -rc1, etc.
    pattern = r'^v(\d+)\.(\d+)\.(\d+)(?:-[a-zA-Z0-9\-\.]+)?$'
    return bool(re.match(pattern, version))


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