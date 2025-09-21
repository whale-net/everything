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
    3. Check which apps depend on the affected targets
    
    This is more reliable than attr() queries which can miss files based on
    how they're referenced in BUILD files.
    """
    if not changed_files:
        return []
    
    try:
        all_apps = list_all_apps()
        affected_apps = []
        
        print(f"Using Bazel package-level analysis to find targets affected by {len(changed_files)} changed files...", file=sys.stderr)
        
        # Build query expressions for all affected packages to reduce subprocess calls
        package_queries = set()
        
        for file_path in changed_files:
            if not file_path:
                continue
                
            # For source files, find all targets in the package containing the file
            file_dir = str(Path(file_path).parent) if Path(file_path).parent != Path('.') else ""
            
            if file_path.endswith(('.bzl', 'BUILD', 'BUILD.bazel')):
                # BUILD/bzl files affect all targets in their package recursively  
                package_path = f"//{file_dir}" if file_dir else "//"
                package_queries.add(f"{package_path}/...")
                        
            elif file_dir:  # Source file in a package directory
                # Source files affect all targets in their immediate package
                package_path = f"//{file_dir}"
                package_queries.add(f"{package_path}:*")
            else:
                # File in root directory - affects root package targets
                package_queries.add("//:*")
        
        # Consolidate all package queries into a single Bazel call
        affected_targets = set()
        if package_queries:
            # Combine all queries with union operator
            combined_query = " + ".join(package_queries)
            
            try:
                result = run_bazel([
                    "query", 
                    combined_query,
                    "--output=label"
                ])
                
                if result.stdout.strip():
                    package_targets = result.stdout.strip().split('\n')
                    affected_targets.update(package_targets)
                    print(f"Found {len(package_targets)} targets affected by changed files", file=sys.stderr)
                    
            except subprocess.CalledProcessError as e:
                print(f"Warning: Could not query targets for changed files: {e}", file=sys.stderr)
        
        print(f"Total targets affected: {len(affected_targets)}", file=sys.stderr)
        
        # Now check which apps depend on any of the affected targets
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


def _query_affected_tests_bazel(changed_files: List[str]) -> List[str]:
    """Use Bazel query to find test targets affected by changed files.
    
    This approach determines which test targets are affected by file changes by:
    1. For source files: Find all targets in the package containing the file
    2. For BUILD/bzl files: Find all targets in the package recursively
    3. Query all test targets that depend on the affected targets
    
    Returns:
        List of test target labels (e.g., ["//demo/hello_python:test_main"])
    """
    if not changed_files:
        return []
    
    try:
        print(f"Using Bazel package-level analysis to find test targets affected by {len(changed_files)} changed files...", file=sys.stderr)
        
        # Build query expressions for all affected packages to reduce subprocess calls
        package_queries = set()
        
        for file_path in changed_files:
            if not file_path:
                continue
                
            # For source files, find all targets in the package containing the file
            file_dir = str(Path(file_path).parent) if Path(file_path).parent != Path('.') else ""
            
            if file_path.endswith(('.bzl', 'BUILD', 'BUILD.bazel')):
                # BUILD/bzl files affect all targets in their package recursively  
                package_path = f"//{file_dir}" if file_dir else "//"
                package_queries.add(f"{package_path}/...")
                        
            elif file_dir:  # Source file in a package directory
                # Source files affect all targets in their immediate package
                package_path = f"//{file_dir}"
                package_queries.add(f"{package_path}:*")
            else:
                # File in root directory - affects root package targets
                package_queries.add("//:*")
        
        # Consolidate all package queries into a single Bazel call
        affected_targets = set()
        if package_queries:
            # Combine all queries with union operator
            combined_query = " + ".join(package_queries)
            
            try:
                result = run_bazel([
                    "query", 
                    combined_query,
                    "--output=label"
                ])
                
                if result.stdout.strip():
                    package_targets = result.stdout.strip().split('\n')
                    affected_targets.update(package_targets)
                    print(f"Found {len(package_targets)} targets affected by changed files", file=sys.stderr)
                    
            except subprocess.CalledProcessError as e:
                print(f"Warning: Could not query targets for changed files: {e}", file=sys.stderr)
        
        print(f"Total targets affected: {len(affected_targets)}", file=sys.stderr)
        
        # Now find all test targets that depend on any of the affected targets
        affected_tests = set()
        
        if affected_targets:
            # Use a single query to find tests that depend on ANY of the affected targets
            # Convert target set to space-separated string for the query
            targets_expr = " + ".join(affected_targets)
            
            try:
                # Query: find all test targets that depend on any of the affected targets
                deps_query = f'kind(".*test", rdeps(//..., {targets_expr}))'
                
                deps_result = run_bazel([
                    "query", 
                    deps_query,
                    "--output=label"
                ])
                
                if deps_result.stdout.strip():
                    affected_tests.update(deps_result.stdout.strip().split('\n'))
                    print(f"Found {len(affected_tests)} test targets that depend on changed targets", file=sys.stderr)
                    
            except subprocess.CalledProcessError as e:
                print(f"Warning: Could not query test dependencies efficiently, trying alternative rdeps approach: {e}", file=sys.stderr)
                
                # Alternative approach: try rdeps with individual targets instead of combined expression
                for target in affected_targets:
                    try:
                        # Query: find all test targets that depend on this specific target
                        deps_query = f'kind(".*test", rdeps(//..., {target}))'
                        
                        deps_result = run_bazel([
                            "query", 
                            deps_query,
                            "--output=label"
                        ])
                        
                        if deps_result.stdout.strip():
                            target_tests = deps_result.stdout.strip().split('\n')
                            affected_tests.update(target_tests)
                            print(f"Found {len(target_tests)} test targets that depend on {target}", file=sys.stderr)
                            
                    except subprocess.CalledProcessError as e:
                        print(f"Warning: Could not query test dependencies for {target}: {e}", file=sys.stderr)
                        continue
                
                # Only fall back to the expensive individual test loop if rdeps completely fails
                if not affected_tests:
                    print("rdeps queries failed, falling back to individual test dependency analysis", file=sys.stderr)
                    try:
                        all_tests_result = run_bazel([
                            "query",
                            'kind(".*test", //...)',
                            "--output=label"
                        ])
                        
                        if all_tests_result.stdout.strip():
                            all_tests = all_tests_result.stdout.strip().split('\n')
                            print(f"Found {len(all_tests)} total test targets, checking dependencies individually", file=sys.stderr)
                            
                            # For each test, check if it depends on any affected targets
                            for test_target in all_tests:
                                try:
                                    # Query all dependencies of this test
                                    deps_result = run_bazel([
                                        "query", 
                                        f"deps({test_target})",
                                        "--output=label"
                                    ])
                                    
                                    if deps_result.stdout.strip():
                                        test_deps = set(deps_result.stdout.strip().split('\n'))
                                        
                                        # Check if this test depends on any affected targets
                                        if affected_targets.intersection(test_deps):
                                            affected_tests.add(test_target)
                                            overlapping_targets = affected_targets.intersection(test_deps)
                                            print(f"Test {test_target} affected: depends on {len(overlapping_targets)} changed targets", file=sys.stderr)
                                            
                                except subprocess.CalledProcessError as e:
                                    print(f"Warning: Could not analyze dependencies for test {test_target}: {e}", file=sys.stderr)
                                    continue
                                    
                    except subprocess.CalledProcessError as e:
                        print(f"Error querying all test targets: {e}", file=sys.stderr)
        
        result_list = sorted(list(affected_tests))
        print(f"Total test targets affected: {len(result_list)}", file=sys.stderr)
        return result_list
        
    except Exception as e:
        print(f"Error in Bazel test dependency analysis: {e}", file=sys.stderr)
        # Fall back to returning all tests
        try:
            all_tests_result = run_bazel([
                "query",
                'kind(".*test", //...)',
                "--output=label"
            ])
            if all_tests_result.stdout.strip():
                all_tests = all_tests_result.stdout.strip().split('\n')
                print(f"Fallback: returning all {len(all_tests)} test targets", file=sys.stderr)
                return all_tests
        except Exception as fallback_error:
            print(f"Error in fallback test query: {fallback_error}", file=sys.stderr)
        
        return []


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


def detect_changed_tests(base_commit: Optional[str] = None, use_bazel_query: bool = True) -> List[str]:
    """Detect which test targets have changed compared to a base commit.
    
    Args:
        base_commit: Base commit to compare HEAD against. If None, returns all test targets.
        use_bazel_query: Whether to use Bazel query for precise dependency analysis.
                        If False, returns all test targets.
    
    Returns:
        List of test target labels (e.g., ["//demo/hello_python:test_main"])
    """
    if not base_commit:
        # No base commit specified, return all test targets
        print("No base commit specified, considering all test targets as needing to run", file=sys.stderr)
        try:
            all_tests_result = run_bazel([
                "query",
                'kind(".*test", //...)',
                "--output=label"
            ])
            if all_tests_result.stdout.strip():
                all_tests = all_tests_result.stdout.strip().split('\n')
                print(f"Found {len(all_tests)} total test targets", file=sys.stderr)
                return all_tests
        except Exception as e:
            print(f"Error querying all test targets: {e}", file=sys.stderr)
        return []

    # Get changed files
    changed_files = _get_changed_files(base_commit)
    
    if not changed_files:
        print("No files changed, no tests need to be run", file=sys.stderr)
        return []

    print(f"Changed files: {', '.join(changed_files[:10])}" + 
          (f" (and {len(changed_files)-10} more)" if len(changed_files) > 10 else ""), 
          file=sys.stderr)

    # Use Bazel query for precise dependency analysis
    if use_bazel_query:
        changed_tests = _query_affected_tests_bazel(changed_files)
    else:
        # Fallback: return all tests when not using Bazel query
        print("Not using Bazel query, returning all test targets to be safe", file=sys.stderr)
        try:
            all_tests_result = run_bazel([
                "query",
                'kind(".*test", //...)',
                "--output=label"
            ])
            if all_tests_result.stdout.strip():
                all_tests = all_tests_result.stdout.strip().split('\n')
                print(f"Fallback: returning all {len(all_tests)} test targets", file=sys.stderr)
                return all_tests
        except Exception as e:
            print(f"Error in fallback test query: {e}", file=sys.stderr)
        return []

    # If no tests detected but there are changes using Bazel analysis
    if not changed_tests and changed_files and use_bazel_query:
        print("Bazel dependency analysis determined no tests are affected by the changes.", 
              file=sys.stderr)

    return changed_tests