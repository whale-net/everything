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
    
    This approach:
    1. Gets all app metadata targets
    2. For each app, gets its dependencies 
    3. Checks if any changed files are in the dependency paths
    """
    if not changed_files:
        return []
    
    try:
        # Get all app metadata targets first
        all_apps = list_all_apps()
        affected_apps = []
        
        print(f"Analyzing {len(all_apps)} apps for dependency changes...", file=sys.stderr)
        
        for app in all_apps:
            # Get all dependencies for this app's image target
            # Extract package path from metadata target to find the actual app target
            metadata_target = app['bazel_target']
            package_path = metadata_target[2:].split(':')[0]  # Remove // and :target
            app_target = f"//{package_path}:{app['name']}"
            
            try:
                # Query all dependencies of this app
                result = run_bazel([
                    "query", 
                    f"deps({app_target})", 
                    "--output=package"
                ])
                
                dep_packages = set(result.stdout.strip().split('\n')) if result.stdout.strip() else set()
                
                # Check if any changed files are in packages that this app depends on
                app_affected = False
                for file_path in changed_files:
                    if not file_path:
                        continue
                    
                    # Determine the package of the changed file
                    file_package = str(Path(file_path).parent) if Path(file_path).parent != Path('.') else ""
                    
                    # Check if this file's package is in the app's dependencies
                    if file_package in dep_packages or "" in dep_packages:  # "" means root package
                        app_affected = True
                        print(f"App {app['name']} affected by change in {file_path} (package: {file_package or 'root'})", file=sys.stderr)
                        break
                    
                    # Also check if the file is in a parent directory of any dependency
                    for dep_pkg in dep_packages:
                        if dep_pkg.startswith(file_package + '/') or file_package.startswith(dep_pkg + '/'):
                            app_affected = True
                            print(f"App {app['name']} affected by change in {file_path} (affects dependency package: {dep_pkg})", file=sys.stderr)
                            break
                    
                    if app_affected:
                        break
                
                if app_affected:
                    affected_apps.append(app)
                    
            except subprocess.CalledProcessError as e:
                print(f"Warning: Could not analyze dependencies for {app['name']}: {e}", file=sys.stderr)
                # If we can't analyze dependencies, assume the app is affected to be safe
                affected_apps.append(app)
        
        return affected_apps
        
    except Exception as e:
        print(f"Error in Bazel dependency analysis: {e}", file=sys.stderr)
        # Fall back to file-based detection
        return _detect_changed_apps_file_based(changed_files)


def _detect_changed_apps_file_based(changed_files: List[str]) -> List[Dict[str, str]]:
    """Fallback file-based change detection (original implementation)."""
    all_apps = list_all_apps()
    
    # Extract directories from changed files and map to bazel targets
    changed_dirs = set()
    for file_path in changed_files:
        if file_path:
            # Get all directory components for proper bazel path matching
            parts = file_path.split('/')
            for i in range(1, len(parts) + 1):
                changed_dirs.add('/'.join(parts[:i]))

    # Find apps that have changes by checking if any changed path
    # is a prefix of the app's bazel target path
    changed_apps = []
    for app in all_apps:
        # Extract package path from bazel target like "//path/to/app:target"
        bazel_path = app['bazel_target'][2:].split(':')[0]
        
        # Check if any changed directory affects this app
        if any(bazel_path.startswith(changed_dir) or changed_dir.startswith(bazel_path) 
               for changed_dir in changed_dirs):
            changed_apps.append(app)

    return changed_apps


def _is_infrastructure_change(changed_files: List[str]) -> bool:
    """Check if changes are in infrastructure directories that affect all apps.
    
    Infrastructure changes are changes that could affect the build, deployment, or runtime
    of all applications in the repository, requiring all apps to be rebuilt as a safety measure.
    
    This function distinguishes between:
    - TRUE infrastructure changes (CI workflows, build tools, core configs) -> rebuild all apps
    - Documentation/config changes that don't affect builds -> use normal dependency analysis
    
    Args:
        changed_files: List of file paths that have changed
        
    Returns:
        True if any change is considered infrastructure that affects all apps,
        False if changes should use normal dependency analysis
        
    Infrastructure triggers:
        - tools/ BUILD and macro files: tools/release.bzl, tools/oci.bzl, tools/BUILD.bazel
        - docker/: Container configurations 
        - .github/workflows/: CI workflow definitions
        - .github/actions/: Reusable GitHub Actions
        - Root Bazel files: MODULE.bazel, BUILD.bazel, WORKSPACE*, .bazelrc
        
    NOT infrastructure triggers (use dependency analysis instead):
        - tools/release_helper/: CLI tools for release automation (don't affect builds)
        - .github/copilot-instructions.md and other documentation
        - libs/: Handled by Bazel dependency analysis
        - app directories: Handled by Bazel dependency analysis
        
    Example:
        # These trigger full rebuild
        _is_infrastructure_change(['.github/workflows/ci.yml']) -> True
        _is_infrastructure_change(['tools/release.bzl']) -> True
        _is_infrastructure_change(['MODULE.bazel']) -> True
        
        # These use dependency analysis 
        _is_infrastructure_change(['tools/release_helper/cli.py']) -> False
        _is_infrastructure_change(['.github/copilot-instructions.md']) -> False
        _is_infrastructure_change(['demo/hello_go/main.go']) -> False
    """
    # Root-level files that affect everything
    root_infra_files = {'MODULE.bazel', 'WORKSPACE', 'BUILD.bazel', 'WORKSPACE.bazel', '.bazelrc'}
    
    # Build tool files in tools/ that affect all apps
    build_tool_files = {
        'tools/release.bzl',
        'tools/oci.bzl', 
        'tools/BUILD.bazel',
        'tools/helm_chart_release.bzl',
        'tools/version_resolver.py'
    }
    
    for file_path in changed_files:
        if not file_path:
            continue
        
        # Check root-level Bazel files that affect everything
        if file_path in root_infra_files:
            return True
        
        # Check specific build tool files that affect all apps
        if file_path in build_tool_files:
            return True
        
        # Docker configurations affect all containerized apps
        if file_path.startswith('docker/') or file_path == 'docker':
            return True
        
        # Special handling for .github directory - only CI/build files should trigger full rebuild
        if file_path.startswith('.github/'):
            # CI workflows and actions affect all apps
            if (file_path.startswith('.github/workflows/') or 
                file_path.startswith('.github/actions/')):
                return True
            # Documentation files (like copilot-instructions.md) should not trigger full rebuild
            # Let them be handled by normal dependency analysis
    
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

    # If no apps detected but there are changes, be conservative and build all
    if not changed_apps and changed_files:
        print("No specific apps detected as changed, but files were modified. Building all apps to be safe.", 
              file=sys.stderr)
        return all_apps

    return changed_apps