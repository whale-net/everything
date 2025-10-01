"""
Change detection utilities for the release helper.
"""

import subprocess
import sys
from pathlib import Path
from typing import Dict, List, Optional, Set

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


def _query_affected_apps_bazel(changed_files: List[str]) -> List[Dict[str, str]]:
    """Use Bazel query to find apps affected by changed files.
    
    This approach determines which targets are affected by file changes by:
    1. For source files: Find all targets in the package containing the file
    2. For BUILD/bzl files: Find all targets in the package recursively
    3. Use rdeps() to find app metadata targets that depend on the affected targets
    
    This is more efficient than querying deps() for each app individually,
    and avoids building metadata for all apps unnecessarily.
    """
    if not changed_files:
        return []
    
    try:
        print(f"Using Bazel reverse dependency analysis to find apps affected by {len(changed_files)} changed files...", file=sys.stderr)
        
        # Find all targets that are directly affected by the changed files
        affected_targets = set()
        
        for file_path in changed_files:
            if not file_path:
                continue
                
            try:
                # For source files, find all targets in the package containing the file
                file_dir = str(Path(file_path).parent) if Path(file_path).parent != Path('.') else ""
                
                if file_path.endswith(('.bzl', 'BUILD', 'BUILD.bazel')):
                    # BUILD/bzl files affect all targets in their package recursively  
                    package_path = f"//{file_dir}" if file_dir else "//"
                    
                    result = run_bazel([
                        "query", 
                        f"{package_path}/...",
                        "--output=label"
                    ])
                    
                    if result.stdout.strip():
                        package_targets = result.stdout.strip().split('\n')
                        affected_targets.update(package_targets)
                        print(f"Build file {file_path} affects all {len(package_targets)} targets in package {package_path}", file=sys.stderr)
                        
                elif file_dir:  # Source file in a package directory
                    # Source files affect all targets in their immediate package
                    package_path = f"//{file_dir}"
                    
                    result = run_bazel([
                        "query", 
                        f"{package_path}:*",
                        "--output=label"
                    ])
                    
                    if result.stdout.strip():
                        package_targets = result.stdout.strip().split('\n')
                        affected_targets.update(package_targets)
                        print(f"Source file {file_path} affects {len(package_targets)} targets in package {package_path}", file=sys.stderr)
                else:
                    # File in root directory - affects root package targets
                    result = run_bazel([
                        "query", 
                        "//:*",
                        "--output=label"
                    ])
                    
                    if result.stdout.strip():
                        root_targets = result.stdout.strip().split('\n')
                        affected_targets.update(root_targets)
                        print(f"Root file {file_path} affects {len(root_targets)} targets in root package", file=sys.stderr)
                    
            except subprocess.CalledProcessError as e:
                print(f"Warning: Could not query targets for file {file_path}: {e}", file=sys.stderr)
        
        if not affected_targets:
            print("No targets affected by changed files", file=sys.stderr)
            return []
        
        print(f"Total targets affected: {len(affected_targets)}", file=sys.stderr)
        
        # Use rdeps() to find all app_metadata targets that depend on any affected target
        # This is much faster than querying deps() for each app individually
        # Build the query expression - set() takes space-separated targets
        affected_targets_list = " ".join(affected_targets)
        
        # Query for app_metadata targets that depend on any of the affected targets
        result = run_bazel([
            "query",
            f"kind(app_metadata, rdeps(//..., set({affected_targets_list})))",
            "--output=label"
        ])
        
        if not result.stdout.strip():
            print("No app metadata targets depend on the affected targets", file=sys.stderr)
            return []
        
        # Get the list of affected app metadata targets
        affected_metadata_targets = [line for line in result.stdout.strip().split('\n') if line]
        print(f"Found {len(affected_metadata_targets)} affected app(s)", file=sys.stderr)
        
        # Now we only need to build metadata for the affected apps
        affected_apps = []
        for metadata_target in affected_metadata_targets:
            try:
                metadata = get_app_metadata(metadata_target)
                affected_apps.append({
                    'bazel_target': metadata_target,
                    'name': metadata['name'],
                    'domain': metadata['domain']
                })
                print(f"App {metadata['name']} is affected by changes", file=sys.stderr)
            except Exception as e:
                print(f"Warning: Could not get metadata for {metadata_target}: {e}", file=sys.stderr)
                continue
        
        return affected_apps
        
    except Exception as e:
        print(f"Error in Bazel dependency analysis: {e}", file=sys.stderr)
        # Fall back to file-based detection
        return _detect_changed_apps_file_based(changed_files)


def _detect_changed_apps_file_based(changed_files: List[str]) -> List[Dict[str, str]]:
    """Fallback file-based change detection (original implementation)."""
    all_apps = list_all_apps()
    
    # Find apps that have changes by checking if any changed file
    # is within the app's directory structure
    changed_apps = []
    for app in all_apps:
        # Extract package path from bazel target like "//path/to/app:target"
        bazel_path = app['bazel_target'][2:].split(':')[0]
        
        # Check if any changed file is within this app's directory
        app_affected = False
        for file_path in changed_files:
            if not file_path:
                continue
            
            # Check if the file is in this app's directory (or subdirectory)
            file_dir = str(Path(file_path).parent) if Path(file_path).parent != Path('.') else ""
            
            # An app is affected if:
            # 1. The changed file is directly in the app's directory
            # 2. The changed file is in a subdirectory of the app's directory
            if file_path.startswith(bazel_path + '/') or file_dir == bazel_path:
                app_affected = True
                break
        
        if app_affected:
            changed_apps.append(app)

    return changed_apps


def _is_infrastructure_change(changed_files: List[str]) -> bool:
    """DEPRECATED: Check if changes are in infrastructure directories that affect all apps.
    
    This function is deprecated as we now rely entirely on Bazel dependency analysis
    to determine which apps are affected by changes, rather than making assumptions
    about what constitutes infrastructure.
    
    This function is kept for backward compatibility with existing tests but always
    returns False to ensure Bazel dependency analysis is used instead.
    """
    # Always return False - let Bazel dependency analysis handle everything
    return False


def detect_changed_apps(base_commit: Optional[str] = None, use_bazel_query: bool = True) -> List[Dict[str, str]]:
    """Detect which apps have changed compared to a base commit.
    
    Args:
        base_commit: Base commit to compare HEAD against. If None, returns all apps.
        use_bazel_query: Whether to use Bazel query for precise dependency analysis.
                        If False, falls back to file-based detection.
    
    Returns:
        List of app dictionaries with bazel_target, name, and domain
    """
    all_apps = list_all_apps()

    if not base_commit:
        # No base commit specified, return all apps
        print("No base commit specified, considering all apps as changed", file=sys.stderr)
        return all_apps

    # Get changed files
    changed_files = _get_changed_files(base_commit)
    
    if not changed_files:
        print("No files changed, no apps need to be built", file=sys.stderr)
        return []

    print(f"Changed files: {', '.join(changed_files[:10])}" + 
          (f" (and {len(changed_files)-10} more)" if len(changed_files) > 10 else ""), 
          file=sys.stderr)

    # Check for infrastructure changes that affect all apps
    if _is_infrastructure_change(changed_files):
        print("Infrastructure changes detected - all apps may be affected", file=sys.stderr)
        return all_apps

    # Use Bazel query for precise dependency analysis
    if use_bazel_query:
        changed_apps = _query_affected_apps_bazel(changed_files)
    else:
        changed_apps = _detect_changed_apps_file_based(changed_files)

    # If no apps detected but there are changes, only build all if using fallback file-based detection
    if not changed_apps and changed_files:
        if use_bazel_query:
            print("Bazel dependency analysis determined no apps are affected by the changes.", 
                  file=sys.stderr)
            return []
        else:
            print("No specific apps detected as changed using file-based detection, but files were modified. Building all apps to be safe.", 
                  file=sys.stderr)
            return all_apps

    return changed_apps