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
    file_labels = []
    for f in relevant_files:
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
    
    # Query rdeps for each file individually - this way invalid labels are naturally ignored
    # Collect all affected targets first
    all_affected_targets = set()
    
    for label in file_labels:
        try:
            result = run_bazel([
                "query",
                f"rdeps(//..., {label})",
                "--output=label"
            ])
            if result.stdout.strip():
                targets = set(result.stdout.strip().split('\n'))
                all_affected_targets.update(targets)
        except subprocess.CalledProcessError:
            # File is not a valid Bazel target (e.g., .bzl files, BUILD files)
            # This is fine - these files aren't part of the build graph
            continue
    
    if not all_affected_targets:
        print("No targets affected by changed files", file=sys.stderr)
        return []
    
    # Find which app binaries are affected, then find their corresponding metadata
    # Strategy: Find all app_metadata targets, get their binary deps, see which are affected
    try:
        # Get all app_metadata targets
        result = run_bazel([
            "query",
            "kind('app_metadata', //...)",
            "--output=label"
        ])
        all_metadata_targets = set(result.stdout.strip().split('\n')) if result.stdout.strip() else set()
        
        # For each metadata target, check if any of its binary deps are in the affected set
        all_affected_metadata = set()
        for metadata_target in all_metadata_targets:
            # Get the package and name from metadata target (e.g., //demo/hello_python:hello_python_metadata)
            # The binaries follow pattern: //demo/hello_python:hello_python_linux_*
            package = metadata_target.rsplit(':', 1)[0]
            app_name = metadata_target.rsplit(':', 1)[1].replace('_metadata', '')
            
            # Check if any affected targets are binaries for this app
            for target in all_affected_targets:
                if target.startswith(f"{package}:{app_name}_linux_") or target.startswith(f"{package}:{app_name}_base_"):
                    all_affected_metadata.add(metadata_target)
                    break
        
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