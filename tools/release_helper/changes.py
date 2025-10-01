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
    1. Build a single union query for all affected package targets
    2. For source files: Find all targets in the package containing the file
    3. For BUILD/bzl files: Find all targets in the package recursively
    4. Use somepath() queries to efficiently determine which apps depend on affected targets
    
    This is more reliable than attr() queries which can miss files based on
    how they're referenced in BUILD files. It's also much more efficient than
    making individual queries per file and per app - reducing from O(files + apps)
    queries to just 2-3 total queries.
    """
    if not changed_files:
        return []
    
    try:
        all_apps = list_all_apps()
        
        print(f"Using Bazel package-level analysis to find targets affected by {len(changed_files)} changed files...", file=sys.stderr)
        
        # Build a single union query for all affected package patterns
        package_queries = []
        
        for file_path in changed_files:
            if not file_path:
                continue
            
            file_dir = str(Path(file_path).parent) if Path(file_path).parent != Path('.') else ""
            
            if file_path.endswith(('.bzl', 'BUILD', 'BUILD.bazel')):
                # BUILD/bzl files affect all targets in their package recursively  
                package_path = f"//{file_dir}" if file_dir else "//"
                package_queries.append(f"{package_path}/...")
                print(f"Build file {file_path} will check package {package_path}/...", file=sys.stderr)
                        
            elif file_dir:  # Source file in a package directory
                # Source files affect all targets in their immediate package
                package_path = f"//{file_dir}"
                package_queries.append(f"{package_path}:*")
                print(f"Source file {file_path} will check package {package_path}:*", file=sys.stderr)
            else:
                # File in root directory - affects root package targets
                package_queries.append("//:*")
                print(f"Root file {file_path} will check root package", file=sys.stderr)
        
        if not package_queries:
            print("No valid package queries generated from changed files", file=sys.stderr)
            return []
        
        # Build a single union query for all affected targets
        # Use set() to deduplicate identical patterns
        unique_queries = list(set(package_queries))
        if len(unique_queries) == 1:
            union_query = unique_queries[0]
        else:
            # Join with + operator for union
            union_query = " + ".join(unique_queries)
        
        print(f"Running single batched query for {len(unique_queries)} unique package patterns...", file=sys.stderr)
        
        # Execute single query to get all affected targets
        result = run_bazel([
            "query", 
            union_query,
            "--output=label"
        ])
        
        if not result.stdout.strip():
            print("No targets found in affected packages", file=sys.stderr)
            return []
        
        affected_targets = set(result.stdout.strip().split('\n'))
        print(f"Total targets affected: {len(affected_targets)}", file=sys.stderr)
        
        # Build app target mapping
        app_binary_targets = {}
        
        for app in all_apps:
            try:
                # Get the app's actual binary target from metadata
                metadata = get_app_metadata(app['bazel_target'])
                binary_target = metadata['binary_target']
                
                # Resolve relative target reference to absolute
                if binary_target.startswith(':'):
                    # Extract package path from metadata target
                    metadata_target = app['bazel_target']
                    package_path = metadata_target[2:].split(':')[0]  # Remove // and split on :
                    app_target = f"//{package_path}{binary_target}"  # Combine package + relative target
                else:
                    # Already absolute
                    app_target = binary_target
                
                app_binary_targets[app['name']] = app_target
                
            except Exception as e:
                print(f"Warning: Could not get binary target for {app['name']}: {e}", file=sys.stderr)
                continue
        
        if not app_binary_targets:
            print("No valid app targets to analyze", file=sys.stderr)
            return []
        
        # Build a single somepath query to check all apps at once
        # somepath(from, to) returns paths from any target in 'from' to any target in 'to'
        # We want to find which app binaries have a path from any affected target
        
        # Build the 'from' set - all affected targets
        if len(affected_targets) == 1:
            from_expr = list(affected_targets)[0]
        else:
            from_expr = "set(" + " ".join(affected_targets) + ")"
        
        # Build the 'to' set - all app binary targets
        app_targets_list = list(app_binary_targets.values())
        if len(app_targets_list) == 1:
            to_expr = app_targets_list[0]
        else:
            to_expr = "set(" + " ".join(app_targets_list) + ")"
        
        print(f"Running single somepath query to check {len(app_binary_targets)} apps against {len(affected_targets)} affected targets...", file=sys.stderr)
        
        # Use somepath to find which app binaries depend on affected targets
        # somepath(X, Y) finds if there's any dependency path from X to Y
        # We need rdeps instead - find all targets that depend on affected targets
        
        # Actually, let's use a different approach: for each app, check if any affected
        # target is in its dependency closure. We can do this with a single intersect operation
        # by getting all reverse dependencies of the affected targets
        
        # Build set expressions for affected targets
        affected_set_expr = "set(" + " ".join(affected_targets) + ")"
        
        # For each app, we need to check: does deps(app) intersect affected_targets?
        # We can do this efficiently by building a single query that unions all checks
        
        affected_apps = []
        affected_apps_found = set()
        
        # Build somepath queries for each app to check if there's a path from affected targets to the app
        # somepath(affected_targets, app_binary) will return non-empty if app depends on any affected target
        somepath_queries = []
        for app_name, app_target in app_binary_targets.items():
            # Check if there's a path from any affected target to this app binary
            somepath_queries.append(f"somepath({affected_set_expr}, {app_target})")
        
        # Union all somepath queries
        if len(somepath_queries) == 1:
            combined_query = somepath_queries[0]
        else:
            combined_query = " + ".join(somepath_queries)
        
        print(f"Running combined somepath query for all apps...", file=sys.stderr)
        
        # Execute the combined query
        result = run_bazel([
            "query",
            combined_query,
            "--output=label"
        ])
        
        if not result.stdout.strip():
            print("No apps depend on affected targets (somepath returned empty)", file=sys.stderr)
            return []
        
        # Parse the results - any app binary that appears in the output depends on affected targets
        result_targets = set(result.stdout.strip().split('\n'))
        
        # Match result targets back to apps
        for app in all_apps:
            app_name = app['name']
            if app_name in app_binary_targets:
                app_target = app_binary_targets[app_name]
                # Check if this app's binary is in the somepath results
                if app_target in result_targets:
                    affected_apps.append(app)
                    print(f"App {app_name} affected: somepath found dependency on changed targets", file=sys.stderr)
                else:
                    print(f"App {app_name} not affected", file=sys.stderr)
        
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