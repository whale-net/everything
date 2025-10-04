"""
Change detection utilities for the release helper.
"""

import subprocess
import sys
from typing import Dict, List, Optional

from tools.release_helper.core import run_bazel
from tools.release_helper.metadata import list_all_apps, get_app_metadata


def _get_changed_files(base_commit: str) -> List[str]:
    """Get list of changed files compared to base commit."""
    try:
        result = subprocess.run(
            ["git", "diff", "--name-only", f"{base_commit}..HEAD"],
            capture_output=True,
            text=True,
            check=True
        )
        return [f for f in result.stdout.strip().split('\n') if f.strip()]
    except subprocess.CalledProcessError as e:
        print(f"Error getting changed files against {base_commit}: {e}", file=sys.stderr)
        return []


def _should_ignore_file(file_path: str) -> bool:
    """Check if a file should be ignored for build impact analysis.
    
    Returns True if the file doesn't affect any app binary builds.
    """
    # Ignore GitHub workflow files - they're CI configuration, not code
    if file_path.startswith('.github/workflows/') or file_path.startswith('.github/actions/'):
        return True
    
    # Ignore documentation files - they don't affect builds
    if file_path.startswith('docs/') or file_path.endswith('.md'):
        return True
    
    # Ignore copilot instructions - AI configuration, not code
    if file_path.endswith('copilot-instructions.md'):
        return True
    
    return False


def detect_changed_apps(base_commit: Optional[str] = None) -> List[Dict[str, str]]:
    """Detect which apps have changed compared to a base commit.
    
    Uses Bazel query to find app binaries that depend on changed source files.
    
    Args:
        base_commit: Base commit to compare HEAD against. If None, returns all apps.
    
    Returns:
        List of app dictionaries with bazel_target, name, and domain
    """
    all_apps = list_all_apps()

    if not base_commit:
        print("No base commit specified, considering all apps as changed", file=sys.stderr)
        return all_apps

    changed_files = _get_changed_files(base_commit)
    
    if not changed_files:
        print("No files changed, no apps need to be built", file=sys.stderr)
        return []

    print(f"Changed files: {', '.join(changed_files[:10])}" + 
          (f" (and {len(changed_files)-10} more)" if len(changed_files) > 10 else ""), 
          file=sys.stderr)

    # Filter out non-build files
    relevant_files = [f for f in changed_files if not _should_ignore_file(f)]
    
    if not relevant_files:
        print("All changed files are non-build artifacts (workflows, docs, etc.). No apps need to be built.", file=sys.stderr)
        return []
    
    if len(relevant_files) < len(changed_files):
        filtered_count = len(changed_files) - len(relevant_files)
        print(f"Filtered out {filtered_count} non-build files (workflows, docs, etc.)", file=sys.stderr)
    
    print(f"Analyzing {len(relevant_files)} changed files using Bazel query...", file=sys.stderr)
    
    # Convert git file paths to Bazel labels: libs/python/utils.py → //libs/python:utils.py
    # Skip files that can never be valid targets (.bzl, BUILD files)
    file_labels = []
    for f in relevant_files:
        # Skip .bzl files - they're loaded, not built
        if f.endswith('.bzl'):
            continue
        
        # Skip BUILD files - they're configuration, not targets  
        if f.endswith(('BUILD', 'BUILD.bazel')):
            continue
        
        parts = f.split('/')
        if len(parts) < 2:
            # Root level file, skip
            continue
        package = '/'.join(parts[:-1])
        filename = parts[-1]
        file_labels.append(f"//{package}:{filename}")
    
    if not file_labels:
        print("No file labels to analyze", file=sys.stderr)
        return []
    
    # Filter to valid labels first - validate in batch for efficiency
    # This filters out deleted files and other invalid targets
    valid_labels = []
    if file_labels:
        try:
            # Try to validate all labels at once
            labels_expr = " + ".join([f"set({label})" for label in file_labels])
            result = run_bazel([
                "query",
                labels_expr,
                "--output=label"
            ])
            if result.stdout.strip():
                valid_labels = result.stdout.strip().split('\n')
        except subprocess.CalledProcessError:
            # If batch validation fails, fall back to individual validation
            # This handles cases where some (but not all) labels are invalid
            for label in file_labels:
                try:
                    result = run_bazel([
                        "query",
                        label,
                        "--output=label"
                    ])
                    if result.stdout.strip():
                        valid_labels.append(label)
                except subprocess.CalledProcessError:
                    # Label is not valid (e.g., deleted file)
                    continue
    
    if not valid_labels:
        print("No valid Bazel targets in changed files", file=sys.stderr)
        return []
    
    # Query rdeps for all valid labels in a single batched query
    # This is much more efficient than querying each file individually
    all_affected_targets = set()
    try:
        labels_expr = " + ".join([f"set({label})" for label in valid_labels])
        result = run_bazel([
            "query",
            f"rdeps(//..., {labels_expr})",
            "--output=label"
        ])
        if result.stdout.strip():
            all_affected_targets = set(result.stdout.strip().split('\n'))
    except subprocess.CalledProcessError as e:
        print(f"Error querying reverse dependencies: {e}", file=sys.stderr)
        return []
    
    if not all_affected_targets:
        print("No targets affected by changed files", file=sys.stderr)
        return []
    
    # Find app_metadata targets whose binary_target dependencies intersect with affected targets
    # Using binary_target (not image_target) because:
    # 1. Binaries are what actually compile the source code
    # 2. More reusable - tests depend on binaries, not images
    # 3. Faster queries - simpler dependency graph than images
    try:
        # Build a set expression for all affected targets
        affected_set = " + ".join([f"set({t})" for t in all_affected_targets])
        
        # Query: Get all metadata targets, find their binary_target deps, see which intersect with affected
        result = run_bazel([
            "query",
            f"let all_affected = {affected_set} in "
            f"let all_metadata = kind('app_metadata', //...) in "
            f"let metadata_binary_deps = deps($all_metadata, 1) - $all_metadata in "
            f"rdeps($all_metadata, $metadata_binary_deps intersect $all_affected, 1) intersect $all_metadata",
            "--output=label"
        ])
        
        if result.stdout.strip():
            all_affected_metadata = set(result.stdout.strip().split('\n'))
        else:
            all_affected_metadata = set()
            
    except subprocess.CalledProcessError:
        all_affected_metadata = set()
    
    if not all_affected_metadata:
        print("No apps affected by changed files", file=sys.stderr)
        return []
    
    # Bazel returned the app_metadata targets that are affected
    # Match to our app list
    affected_apps = []
    for app in all_apps:
        if app['bazel_target'] in all_affected_metadata:
            affected_apps.append(app)
            print(f"  {app['name']}: affected by changes", file=sys.stderr)
    
    return affected_apps