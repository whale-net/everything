"""
Change detection utilities for the release helper.

This module provides app-specific change detection by wrapping the core
change_detection module and filtering for app_metadata targets.
"""

import sys
from typing import Dict, List, Optional

from tools.release_helper.change_detection import detect_affected_targets
from tools.release_helper.metadata import list_all_apps, get_app_metadata


def _is_infrastructure_change(changed_files: List[str]) -> bool:
    """Deprecated: Infrastructure detection is no longer used.
    
    This function is kept for backwards compatibility with tests but always
    returns False. We now rely entirely on Bazel's dependency analysis (rdeps)
    to determine which targets are affected by changes, rather than making
    assumptions about "infrastructure" files.
    
    Args:
        changed_files: List of changed file paths (ignored)
    
    Returns:
        Always returns False
    """
    return False


def detect_changed_apps(base_commit: Optional[str] = None, use_bazel_query: bool = True) -> List[Dict[str, str]]:
    """Detect which apps have changed compared to a base commit.
    
    This function uses the core change_detection module with Bazel rdeps to find
    affected apps. It queries for targets that depend on changed files and filters
    for app_metadata targets.
    
    Args:
        base_commit: Base commit to compare HEAD against. If None, returns all apps.
        use_bazel_query: Whether to use Bazel query for precise dependency analysis.
                        If False, falls back to returning all apps.
    
    Returns:
        List of app dictionaries with bazel_target, name, and domain
    """
    all_apps = list_all_apps()

    if not base_commit:
        # No base commit specified, return all apps
        print("No base commit specified, considering all apps as changed", file=sys.stderr)
        return all_apps

    if not use_bazel_query:
        # Legacy mode: return all apps when not using Bazel query
        print("Bazel query disabled, returning all apps", file=sys.stderr)
        return all_apps

    # Use the core change detection to find all affected targets
    # We don't filter by kind here because we want to find apps whose dependencies changed
    print(f"Detecting changes against {base_commit}...", file=sys.stderr)
    affected_targets = detect_affected_targets(base_commit=base_commit, target_kind=None, universe="//...")
    
    if not affected_targets:
        print("No targets affected by changes", file=sys.stderr)
        return []
    
    # Convert to set for faster lookup
    affected_set = set(affected_targets)
    
    # Check which apps are affected
    # An app is affected if:
    # 1. Its metadata target is in the affected set, OR
    # 2. Its binary target is in the affected set, OR
    # 3. Any of its dependencies are in the affected set
    changed_apps = []
    
    for app in all_apps:
        metadata_target = app['bazel_target']
        
        # Check if the app's metadata target is affected
        if metadata_target in affected_set:
            changed_apps.append(app)
            print(f"App {app['name']}: metadata target affected", file=sys.stderr)
            continue
        
        # Get the app's binary target and check if it's affected
        try:
            metadata = get_app_metadata(metadata_target)
            binary_target = metadata['binary_target']
            
            # Resolve relative target reference to absolute
            if binary_target.startswith(':'):
                # Extract package path from metadata target
                package_path = metadata_target[2:].split(':')[0]  # Remove // and split on :
                binary_target = f"//{package_path}{binary_target}"
            
            if binary_target in affected_set:
                changed_apps.append(app)
                print(f"App {app['name']}: binary target affected", file=sys.stderr)
                continue
            
        except Exception as e:
            print(f"Warning: Could not check binary target for {app['name']}: {e}", file=sys.stderr)
            # If we can't check, be conservative and include it
            changed_apps.append(app)
    
    if not changed_apps:
        print("Bazel dependency analysis determined no apps are affected by the changes.", 
              file=sys.stderr)
    
    return changed_apps
