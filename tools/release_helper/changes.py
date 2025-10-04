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


def _should_ignore_file(file_path: str) -> bool:
    """Check if a file should be ignored for build impact analysis.
    
    Returns True if the file doesn't affect any app binary builds.
    Note: This filters out changes that clearly don't affect any builds.
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
    
    # Ignore release helper tools - they're release automation, not app code
    # These are the CLI tools used by CI/CD workflows to manage releases
    if file_path.startswith('tools/release_helper/'):
        return True
    
    return False


def _query_affected_apps_bazel(changed_files: List[str]) -> List[Dict[str, str]]:
    """Use Bazel query to find apps affected by changed files.
    
    Strategy: For each changed file, find the Bazel targets that use it (via source files),
    then use rdeps to find which app binaries depend on those targets.
    """
    if not changed_files:
        return []
    
    # Filter out files that don't affect builds
    relevant_files = [f for f in changed_files if not _should_ignore_file(f)]
    
    if not relevant_files:
        print("All changed files are non-build artifacts (workflows, docs, etc.). No apps need to be built.", file=sys.stderr)
        return []
    
    if len(relevant_files) < len(changed_files):
        filtered_count = len(changed_files) - len(relevant_files)
        print(f"Filtered out {filtered_count} non-build files (workflows, docs, etc.)", file=sys.stderr)
    
    try:
        all_apps = list_all_apps()
        workspace_root = find_workspace_root()
        
        # Build set of all app binary targets for rdeps query
        app_targets = set()
        app_by_target = {}
        for app in all_apps:
            try:
                metadata = get_app_metadata(app['bazel_target'])
                binary_target = metadata['binary_target']
                
                # Resolve relative target reference to absolute
                if binary_target.startswith(':'):
                    metadata_target = app['bazel_target']
                    package_path = metadata_target[2:].split(':')[0]
                    binary_target = f"//{package_path}{binary_target}"
                
                app_targets.add(binary_target)
                app_by_target[binary_target] = app
            except Exception as e:
                print(f"Warning: Could not get metadata for {app['name']}: {e}", file=sys.stderr)
                continue
        
        if not app_targets:
            print("No app targets found", file=sys.stderr)
            return []
        
        # Group files by their package to minimize queries
        files_by_package = {}
        for file_path in relevant_files:
            if not file_path:
                continue
            
            # Get package path (directory containing the file)
            file_dir = str(Path(file_path).parent) if Path(file_path).parent != Path('.') else ""
            
            # Check if this directory has a BUILD file
            has_build = False
            if file_dir:
                build_file = workspace_root / file_dir / "BUILD.bazel"
                alt_build_file = workspace_root / file_dir / "BUILD"
                has_build = build_file.exists() or alt_build_file.exists()
                package = f"//{file_dir}"
            else:
                has_build = (workspace_root / "BUILD.bazel").exists() or (workspace_root / "BUILD").exists()
                package = "//"
            
            if has_build:
                if package not in files_by_package:
                    files_by_package[package] = []
                files_by_package[package].append(file_path)
        
        if not files_by_package:
            print("No Bazel packages affected by changes", file=sys.stderr)
            return []
        
        print(f"Analyzing {len(files_by_package)} affected packages...", file=sys.stderr)
        
        # For each package, use rdeps to find which apps depend on it
        affected_apps = set()
        
        for package_path, files in sorted(files_by_package.items()):
            try:
                # Use rdeps to find all app targets that depend on anything in this package
                # Exclude platform and config_setting targets - they're build configuration,
                # not actual code dependencies (apps reference them for cross-compilation)
                app_targets_str = " + ".join(app_targets)
                result = run_bazel([
                    "query",
                    f"rdeps({app_targets_str}, {package_path}/...) - kind('platform|config_setting', {package_path}/...)",
                    "--output=label"
                ])
                
                if result.stdout.strip():
                    dependent_targets = set(result.stdout.strip().split('\n'))
                    # Find which app binaries are in the result
                    for target in dependent_targets:
                        if target in app_by_target:
                            affected_apps.add(app_by_target[target]['name'])
                            print(f"  {app_by_target[target]['name']}: affected by changes in {package_path}", file=sys.stderr)
            except subprocess.CalledProcessError as e:
                # Package might not have any targets that apps depend on
                print(f"  {package_path}: no app dependencies found", file=sys.stderr)
                continue
        
        # Return the affected apps
        return [app for app in all_apps if app['name'] in affected_apps]
        
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