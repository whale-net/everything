"""
Change detection utilities for the release helper.
"""

import subprocess
import sys
from pathlib import Path
from typing import Dict, List, Optional, Set

from tools.release_helper.core import run_bazel, find_workspace_root
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
    
    Strategy: Identify which packages have changes, query them once each,
    then check which apps depend on those targets.
    """
    if not changed_files:
        return []
    
    try:
        all_apps = list_all_apps()
        workspace_root = find_workspace_root()
        
        # Extract unique packages from changed files
        changed_packages = set()
        for file_path in changed_files:
            if not file_path:
                continue
            
            # Get package path (directory containing the file)
            file_dir = str(Path(file_path).parent) if Path(file_path).parent != Path('.') else ""
            
            # Check if this directory has a BUILD file
            if file_dir:
                build_file = workspace_root / file_dir / "BUILD.bazel"
                alt_build_file = workspace_root / file_dir / "BUILD"
                if build_file.exists() or alt_build_file.exists():
                    changed_packages.add(f"//{file_dir}")
            else:
                # Root directory file
                if (workspace_root / "BUILD.bazel").exists() or (workspace_root / "BUILD").exists():
                    changed_packages.add("//")
        
        if not changed_packages:
            print("No Bazel packages affected by changes", file=sys.stderr)
            return []
        
        print(f"Querying {len(changed_packages)} affected packages...", file=sys.stderr)
        
        # Query all targets in affected packages
        affected_targets = set()
        for package_path in sorted(changed_packages):
            try:
                # Query all targets in the package and its subpackages
                result = run_bazel([
                    "query",
                    f"{package_path}/...",
                    "--output=label"
                ])
                
                if result.stdout.strip():
                    targets = result.stdout.strip().split('\n')
                    affected_targets.update(targets)
                    print(f"  {package_path}: {len(targets)} targets", file=sys.stderr)
            except subprocess.CalledProcessError:
                # Package doesn't exist or has no targets - that's fine, skip it
                pass
        
        if not affected_targets:
            print("No Bazel targets affected", file=sys.stderr)
            return []
        
        print(f"Total: {len(affected_targets)} targets affected", file=sys.stderr)
        
        # Check which apps depend on any of the affected targets
        affected_apps = []
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
                        affected_apps.append(app)
                        print(f"  {app['name']}: depends on {len(affected_targets.intersection(app_deps))} changed targets", file=sys.stderr)
                    
            except Exception as e:
                print(f"Warning: Could not analyze {app['name']}: {e}", file=sys.stderr)
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