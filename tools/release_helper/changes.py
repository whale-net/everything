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
    
    This approach uses Bazel's attr() queries to directly find which targets
    reference the changed files, rather than making package-based assumptions.
    Only apps that depend on the actually affected targets are rebuilt.
    """
    if not changed_files:
        return []
    
    try:
        all_apps = list_all_apps()
        affected_apps = []
        
        print(f"Using Bazel attr queries to find targets affected by {len(changed_files)} changed files...", file=sys.stderr)
        
        # Find all targets that are directly affected by the changed files
        affected_targets = set()
        
        for file_path in changed_files:
            if not file_path:
                continue
                
            try:
                # Use Bazel attr query to find targets that reference this file
                result = run_bazel([
                    "query", 
                    f"attr('srcs', '{file_path}', //...)",
                    "--output=label"
                ])
                
                if result.stdout.strip():
                    file_targets = result.stdout.strip().split('\n')
                    affected_targets.update(file_targets)
                    print(f"File {file_path} directly affects {len(file_targets)} targets", file=sys.stderr)
                
                # For BUILD/bzl files, all targets in the package are affected
                if file_path.endswith(('.bzl', 'BUILD', 'BUILD.bazel')):
                    file_dir = str(Path(file_path).parent) if Path(file_path).parent != Path('.') else ""
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
                    
            except subprocess.CalledProcessError as e:
                print(f"Warning: Could not query targets for file {file_path}: {e}", file=sys.stderr)
        
        print(f"Total targets affected: {len(affected_targets)}", file=sys.stderr)
        
        # Now check which apps depend on any of the affected targets
        for app in all_apps:
            try:
                # Get the app's actual binary target from metadata
                metadata = get_app_metadata(app['bazel_target'])
                app_target = metadata['binary_target']
                
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
                        overlapping_targets = affected_targets.intersection(app_deps)
                        print(f"App {app['name']} affected: depends on {len(overlapping_targets)} changed targets", file=sys.stderr)
                    else:
                        print(f"App {app['name']} not affected: no dependency on changed targets", file=sys.stderr)
                    
            except Exception as e:
                print(f"Warning: Could not analyze dependencies for {app['name']}: {e}", file=sys.stderr)
                # If we can't analyze this app, don't assume it's affected
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
        
        # Special handling for .github directory - be more selective about what triggers rebuilds
        if file_path.startswith('.github/'):
            # CI build workflows affect all apps  
            if file_path.startswith('.github/workflows/ci.yml'):
                return True
            # Build-related GitHub Actions affect all apps
            if file_path.startswith('.github/actions/') and (
                'setup-build' in file_path or 'build' in file_path.lower()
            ):
                return True
            # Other workflows (like release.yml) and documentation should not trigger full rebuild
            # They should use normal dependency analysis
    
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