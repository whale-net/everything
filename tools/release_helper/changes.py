"""
Change detection utilities for the release helper.
"""

import subprocess
import sys
from pathlib import Path
from typing import Dict, List, Optional, Set

from tools.release_helper.core import run_bazel
from tools.release_helper.metadata import list_all_apps


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
    
    This approach uses Bazel's precise dependency graph to determine which apps
    are actually affected by file changes, rather than making assumptions about
    directory structure or "infrastructure" changes.
    
    Approach:
    1. Get all app targets
    2. For each changed file, find which Bazel targets contain or depend on that file
    3. Check if any app depends on those affected targets
    """
    if not changed_files:
        return []
    
    try:
        all_apps = list_all_apps()
        affected_apps = set()
        
        print(f"Analyzing {len(all_apps)} apps against {len(changed_files)} changed files using Bazel dependency graph...", file=sys.stderr)
        
        # For each changed file, find all targets that might be affected
        affected_targets = set()
        for file_path in changed_files:
            if not file_path:
                continue
                
            try:
                # Find which packages contain this file
                file_dir = str(Path(file_path).parent) if Path(file_path).parent != Path('.') else ""
                package_path = f"//{file_dir}" if file_dir else "//"
                
                # Query all targets in the package containing this file
                result = run_bazel([
                    "query", 
                    f"{package_path}/...",
                    "--output=label"
                ])
                
                if result.stdout.strip():
                    package_targets = result.stdout.strip().split('\n')
                    affected_targets.update(package_targets)
                    print(f"File {file_path} affects package {package_path} with {len(package_targets)} targets", file=sys.stderr)
                    
            except subprocess.CalledProcessError as e:
                print(f"Warning: Could not query targets for file {file_path}: {e}", file=sys.stderr)
                # If we can't query this file, we'll check apps individually below
        
        # Now check which apps depend on any of the affected targets
        for app in all_apps:
            try:
                # Get the app's binary target
                metadata_target = app['bazel_target']
                package_path = metadata_target[2:].split(':')[0]
                app_target = f"//{package_path}:{app['name']}"
                
                # Query all dependencies of this app
                result = run_bazel([
                    "query", 
                    f"deps({app_target})",
                    "--output=label"
                ])
                
                if result.stdout.strip():
                    app_deps = set(result.stdout.strip().split('\n'))
                    
                    # Check if this app depends on any affected targets
                    if affected_targets.intersection(app_deps):
                        affected_apps.add(app['name'])
                        overlapping_targets = affected_targets.intersection(app_deps)
                        print(f"App {app['name']} is affected (depends on {len(overlapping_targets)} changed targets)", file=sys.stderr)
                    
                    # Also check if any changed files are directly in the app's package
                    app_package = f"//{package_path}"
                    for file_path in changed_files:
                        if not file_path:
                            continue
                        file_dir = str(Path(file_path).parent) if Path(file_path).parent != Path('.') else ""
                        file_package = f"//{file_dir}" if file_dir else "//"
                        
                        if file_package == app_package or file_path.startswith(package_path + '/'):
                            affected_apps.add(app['name'])
                            print(f"App {app['name']} is affected (file {file_path} is in app package)", file=sys.stderr)
                            break
                    
            except subprocess.CalledProcessError as e:
                print(f"Warning: Could not analyze dependencies for {app['name']}: {e}", file=sys.stderr)
                # If we can't analyze this app, don't assume it's affected
                continue
        
        # Convert back to the expected format
        result_apps = [app for app in all_apps if app['name'] in affected_apps]
        return result_apps
        
    except Exception as e:
        print(f"Error in Bazel dependency analysis: {e}", file=sys.stderr)
        # Fall back to file-based detection
        return _detect_changed_apps_file_based(changed_files)


def _detect_changed_apps_file_based(changed_files: List[str]) -> List[Dict[str, str]]:
    """Fallback file-based change detection when Bazel query is unavailable.
    
    This is a simplified approach that only affects apps when files are directly
    in their directory structure. It does not make assumptions about "infrastructure"
    and trusts that if no direct file changes affect an app, it doesn't need rebuilding.
    """
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
                print(f"App {app['name']} affected by file change: {file_path}", file=sys.stderr)
                break
        
        if app_affected:
            changed_apps.append(app)

    return changed_apps


def _is_infrastructure_change(changed_files: List[str]) -> bool:
    """DEPRECATED: Check if changes are in infrastructure directories that affect all apps.
    
    This function is deprecated as we now rely entirely on Bazel's dependency analysis
    to determine which apps are affected by changes, rather than making assumptions
    about what constitutes "infrastructure".
    
    This function is kept for backward compatibility with existing tests but always
    returns False to ensure Bazel dependency analysis is used instead.
    """
    # Always return False - let Bazel dependency analysis handle everything
    return False


def detect_changed_apps(base_commit: Optional[str] = None, use_bazel_query: bool = True) -> List[Dict[str, str]]:
    """Detect which apps have changed compared to a base commit.
    
    This function relies primarily on Bazel's dependency analysis to determine
    which apps are affected by changes. It trusts Bazel's understanding of the
    build graph rather than making assumptions about what constitutes "infrastructure".
    
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

    # Use Bazel query for precise dependency analysis - this is the primary approach
    if use_bazel_query:
        print("Using Bazel dependency analysis to determine affected apps...", file=sys.stderr)
        changed_apps = _query_affected_apps_bazel(changed_files)
    else:
        print("Using file-based fallback detection (Bazel unavailable)...", file=sys.stderr)
        changed_apps = _detect_changed_apps_file_based(changed_files)

    # Trust Bazel's analysis - if no apps are affected, don't build anything
    if not changed_apps and changed_files:
        if use_bazel_query:
            print("Bazel dependency analysis determined no apps are affected by the changes.", 
                  file=sys.stderr)
            return []
        else:
            print("File-based detection found no affected apps.", file=sys.stderr)
            return []

    print(f"Apps affected by changes: {[app['name'] for app in changed_apps]}", file=sys.stderr)
    return changed_apps