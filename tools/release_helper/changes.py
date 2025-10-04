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
    # For BUILD file changes, track the package paths
    file_labels = []
    changed_packages = set()
    
    for f in relevant_files:
        # Skip .bzl files - they're loaded, not built
        if f.endswith('.bzl'):
            continue
        
        # BUILD file changes affect the entire package
        if f.endswith(('BUILD', 'BUILD.bazel')):
            parts = f.split('/')
            if len(parts) > 1:
                package = '/'.join(parts[:-1])
                changed_packages.add(f"//{package}")
            else:
                changed_packages.add("//")
            continue
        
        parts = f.split('/')
        if len(parts) < 2:
            # Root level file: emit //:filename
            file_labels.append(f"//:{f}")
        else:
            package = '/'.join(parts[:-1])
            filename = parts[-1]
            file_labels.append(f"//{package}:{filename}")
    
    if not file_labels:
        print("No file labels to analyze", file=sys.stderr)
        return []
    
    # Filter to valid labels first - validate in batch for efficiency
    # This filters out deleted files and other invalid targets
    valid_labels = []
    if file_labels:
        try:
            # Try to validate all labels at once using union operator
            labels_expr = " + ".join(file_labels)
            result = run_bazel([
                "query",
                labels_expr,
                "--output=label"
            ])
            if result.stdout.strip():
                valid_labels = result.stdout.strip().split('\n')
        except subprocess.CalledProcessError:
            # If batch validation fails, fall back to individual validation
            # This handles cases where some (but not all) labels are invalid
            for label in file_labels:
                try:
                    result = run_bazel([
                        "query",
                        label,
                        "--output=label"
                    ])
                    if result.stdout.strip():
                        valid_labels.append(label)
                except subprocess.CalledProcessError:
                    # Label is not valid (e.g., deleted file)
                    continue
    
    if not valid_labels and not changed_packages:
        print("No valid Bazel targets in changed files", file=sys.stderr)
        return []
    
    # Get all app_metadata targets first - this scopes our rdeps query
    # OPTIMIZATION: Query metadata targets before rdeps to limit the scope
    try:
        result = run_bazel([
            "query",
            "kind('app_metadata', //...)",
            "--output=label"
        ])
        all_metadata_targets = set(result.stdout.strip().split('\n')) if result.stdout.strip() else set()
        
        if not all_metadata_targets:
            print("No app_metadata targets found", file=sys.stderr)
            return []
    except subprocess.CalledProcessError as e:
        print(f"Error querying app_metadata targets: {e}", file=sys.stderr)
        return []
    
    # Build query for file labels and package changes
    query_parts = []
    if valid_labels:
        query_parts.append(" + ".join(valid_labels))
    if changed_packages:
        # For changed packages, query all targets in those packages
        for pkg in changed_packages:
            query_parts.append(f"{pkg}/...")
    
    if not query_parts:
        print("No query parts to analyze", file=sys.stderr)
        return []
    
    # OPTIMIZATION: Query rdeps only within the scope of app_metadata targets
    # This is much faster than rdeps(//..., changed_files) because we only look
    # at dependencies of metadata targets, not all targets in the repository
    try:
        metadata_expr = " + ".join(all_metadata_targets)
        labels_expr = " + ".join(query_parts)
        
        result = run_bazel([
            "query",
            f"rdeps({metadata_expr}, {labels_expr})",
            "--output=label"
        ])
        
        if result.stdout.strip():
            all_affected_metadata = set(result.stdout.strip().split('\n'))
        else:
            all_affected_metadata = set()
            
    except subprocess.CalledProcessError as e:
        print(f"Error querying reverse dependencies: {e}", file=sys.stderr)
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


def detect_changed_helm_charts(base_commit: Optional[str] = None) -> List[Dict[str, str]]:
    """Detect which helm charts have changed compared to a base commit.
    
    Uses Bazel query to find helm chart metadata targets that depend on changed source files.
    This uses the same optimized rdeps approach as detect_changed_apps.
    
    Args:
        base_commit: Base commit to compare HEAD against. If None, returns all charts.
    
    Returns:
        List of chart dictionaries with bazel_target, name, domain, namespace, and apps
    """
    # Import here to avoid circular dependency
    from tools.release_helper.helm import list_all_helm_charts
    
    all_charts = list_all_helm_charts()

    if not base_commit:
        print("No base commit specified, considering all helm charts as changed", file=sys.stderr)
        return all_charts

    changed_files = _get_changed_files(base_commit)
    
    if not changed_files:
        print("No files changed, no helm charts need to be built", file=sys.stderr)
        return []

    print(f"Changed files: {', '.join(changed_files[:10])}" + 
          (f" (and {len(changed_files)-10} more)" if len(changed_files) > 10 else ""), 
          file=sys.stderr)

    # Filter out non-build files
    relevant_files = [f for f in changed_files if not _should_ignore_file(f)]
    
    if not relevant_files:
        print("All changed files are non-build artifacts (workflows, docs, etc.). No helm charts need to be built.", file=sys.stderr)
        return []
    
    if len(relevant_files) < len(changed_files):
        filtered_count = len(changed_files) - len(relevant_files)
        print(f"Filtered out {filtered_count} non-build files (workflows, docs, etc.)", file=sys.stderr)
    
    print(f"Analyzing {len(relevant_files)} changed files using Bazel query...", file=sys.stderr)
    
    # Convert git file paths to Bazel labels
    file_labels = []
    changed_packages = set()
    
    for f in relevant_files:
        # Skip .bzl files - they're loaded, not built
        if f.endswith('.bzl'):
            continue
        
        # BUILD file changes affect the entire package
        if f.endswith(('BUILD', 'BUILD.bazel')):
            parts = f.split('/')
            if len(parts) > 1:
                package = '/'.join(parts[:-1])
                changed_packages.add(f"//{package}")
            else:
                changed_packages.add("//")
            continue
        
        parts = f.split('/')
        if len(parts) < 2:
            # Root level file: emit //:filename
            file_labels.append(f"//:{f}")
        else:
            package = '/'.join(parts[:-1])
            filename = parts[-1]
            file_labels.append(f"//{package}:{filename}")
    
    if not file_labels and not changed_packages:
        print("No file labels to analyze", file=sys.stderr)
        return []
    
    # Filter to valid labels first - validate in batch for efficiency
    valid_labels = []
    if file_labels:
        try:
            # Try to validate all labels at once using union operator
            labels_expr = " + ".join(file_labels)
            result = run_bazel([
                "query",
                labels_expr,
                "--output=label"
            ])
            if result.stdout.strip():
                valid_labels = result.stdout.strip().split('\n')
        except subprocess.CalledProcessError:
            # If batch validation fails, fall back to individual validation
            for label in file_labels:
                try:
                    result = run_bazel([
                        "query",
                        label,
                        "--output=label"
                    ])
                    if result.stdout.strip():
                        valid_labels.append(label)
                except subprocess.CalledProcessError:
                    # Label is not valid (e.g., deleted file)
                    continue
    
    if not valid_labels and not changed_packages:
        print("No valid Bazel targets in changed files", file=sys.stderr)
        return []
    
    # Get all helm_chart_metadata targets first - this scopes our rdeps query
    # OPTIMIZATION: Query metadata targets before rdeps to limit the scope
    try:
        result = run_bazel([
            "query",
            "kind('helm_chart_metadata', //...)",
            "--output=label"
        ])
        all_chart_metadata_targets = set(result.stdout.strip().split('\n')) if result.stdout.strip() else set()
        
        if not all_chart_metadata_targets:
            print("No helm_chart_metadata targets found", file=sys.stderr)
            return []
    except subprocess.CalledProcessError as e:
        print(f"Error querying helm_chart_metadata targets: {e}", file=sys.stderr)
        return []
    
    # Build query for file labels and package changes
    query_parts = []
    if valid_labels:
        query_parts.append(" + ".join(valid_labels))
    if changed_packages:
        # For changed packages, query all targets in those packages
        for pkg in changed_packages:
            query_parts.append(f"{pkg}/...")
    
    if not query_parts:
        print("No query parts to analyze", file=sys.stderr)
        return []
    
    # OPTIMIZATION: Query rdeps only within the scope of helm_chart_metadata targets
    try:
        metadata_expr = " + ".join(all_chart_metadata_targets)
        labels_expr = " + ".join(query_parts)
        
        result = run_bazel([
            "query",
            f"rdeps({metadata_expr}, {labels_expr})",
            "--output=label"
        ])
        
        if result.stdout.strip():
            all_affected_metadata = set(result.stdout.strip().split('\n'))
        else:
            all_affected_metadata = set()
            
    except subprocess.CalledProcessError as e:
        print(f"Error querying reverse dependencies: {e}", file=sys.stderr)
        all_affected_metadata = set()
    
    if not all_affected_metadata:
        print("No helm charts affected by changed files", file=sys.stderr)
        return []
    
    # Match affected metadata targets to our chart list
    affected_charts = []
    for chart in all_charts:
        if chart['bazel_target'] in all_affected_metadata:
            affected_charts.append(chart)
            print(f"  {chart['name']}: affected by changes", file=sys.stderr)
    
    return affected_charts