"""
Core change detection using Bazel query rdeps.

This module provides the fundamental change detection logic that uses Bazel's
dependency graph to precisely determine which targets are affected by file changes.
"""

import subprocess
import sys
from pathlib import Path
from typing import List, Optional, Set

from tools.release_helper.core import run_bazel
from tools.release_helper.git import get_changed_files_since_commit


def detect_affected_targets(
    base_commit: Optional[str] = None,
    target_kind: Optional[str] = None,
    universe: str = "//...",
) -> List[str]:
    """Detect targets affected by changes using Bazel rdeps query.
    
    This is the core change detection function that uses Bazel's dependency graph
    to find all targets that transitively depend on changed files.
    
    Args:
        base_commit: Git commit to compare against. If None, returns all targets of the kind.
        target_kind: Filter for specific target kind (e.g., "test", "py_binary", "go_binary").
                    If None, returns all affected targets.
        universe: The universe of targets to search within (default: "//...").
    
    Returns:
        List of Bazel target labels that are affected by the changes.
    
    Algorithm:
        1. Get changed files from git
        2. For each changed file, determine which Bazel targets it belongs to
        3. Use `rdeps(universe, changed_targets)` to find all targets that depend on them
        4. Optionally filter by target kind
        5. Return the list of affected targets
    """
    # If no base commit, return all targets of the specified kind
    if not base_commit:
        print("No base commit specified, querying all targets", file=sys.stderr)
        return _query_all_targets(target_kind, universe)
    
    # Get changed files
    changed_files = get_changed_files_since_commit(base_commit)
    
    if not changed_files:
        print("No files changed, no targets affected", file=sys.stderr)
        return []
    
    print(f"Found {len(changed_files)} changed file(s)", file=sys.stderr)
    if len(changed_files) <= 10:
        for f in changed_files:
            print(f"  - {f}", file=sys.stderr)
    else:
        for f in changed_files[:10]:
            print(f"  - {f}", file=sys.stderr)
        print(f"  ... and {len(changed_files) - 10} more", file=sys.stderr)
    
    # Find targets that directly contain or depend on the changed files
    changed_targets = _find_targets_for_files(changed_files)
    
    if not changed_targets:
        print("No targets found for changed files", file=sys.stderr)
        return []
    
    print(f"Changed files affect {len(changed_targets)} direct target(s)", file=sys.stderr)
    
    # Use rdeps to find all targets that transitively depend on the changed targets
    affected_targets = _find_reverse_dependencies(changed_targets, universe)
    
    print(f"Total affected targets (before filtering): {len(affected_targets)}", file=sys.stderr)
    
    # Filter by target kind if specified
    if target_kind:
        affected_targets = _filter_by_kind(affected_targets, target_kind)
        print(f"Affected targets of kind '{target_kind}': {len(affected_targets)}", file=sys.stderr)
    
    return affected_targets


def _query_all_targets(target_kind: Optional[str], universe: str) -> List[str]:
    """Query all targets of a specific kind in the universe."""
    try:
        if target_kind:
            query = f'kind("{target_kind}", {universe})'
        else:
            query = universe
        
        result = run_bazel([
            "query",
            query,
            "--output=label",
            "--keep_going",
        ])
        
        if result.stdout.strip():
            return result.stdout.strip().split('\n')
        return []
        
    except subprocess.CalledProcessError as e:
        print(f"Error querying all targets: {e}", file=sys.stderr)
        return []


def _find_targets_for_files(changed_files: List[str]) -> Set[str]:
    """Find Bazel targets that contain or are affected by changed files.
    
    Strategy:
    - Group files by type (BUILD, source, etc.)
    - Make batched Bazel queries instead of one per file
    - For BUILD/bzl files: Find all targets in affected packages
    - For source files: Use package-level queries (all targets in package)
    """
    changed_targets = set()
    
    # Categorize files and collect affected packages
    build_packages = set()  # Packages with BUILD file changes
    source_packages = set()  # Packages with source file changes
    
    for file_path in changed_files:
        if not file_path or file_path.strip() == "":
            continue
        
        file_path = file_path.strip()
        path = Path(file_path)
        
        # Skip files in directories that don't have BUILD files (e.g., .github)
        if path.parts and path.parts[0] in ['.github', '.git', 'docs'] or file_path.startswith('.'):
            continue
        
        # Skip known non-source file types
        non_source_extensions = ['.md', '.txt', '.json', '.yaml', '.yml', '.sh', '.bash']
        if path.suffix.lower() in non_source_extensions:
            continue
        
        # Determine package path
        package_path = f"//{path.parent}" if path.parent != Path('.') else "//"
        
        # Categorize by file type
        if path.name in ["BUILD", "BUILD.bazel"] or path.suffix == ".bzl":
            build_packages.add(package_path)
        else:
            source_packages.add(package_path)
    
    print(f"  Found {len(build_packages)} package(s) with BUILD changes, {len(source_packages)} with source changes", file=sys.stderr)
    
    # Query BUILD packages (use recursive //package/... to get all sub-packages)
    if build_packages:
        # Build a single query for all BUILD-affected packages
        build_queries = [f"{pkg}/..." for pkg in build_packages]
        query_expr = " + ".join(build_queries)
        
        print(f"  Querying {len(build_packages)} BUILD-affected package(s)...", file=sys.stderr)
        
        try:
            result = run_bazel([
                "query",
                query_expr,
                "--output=label",
                "--keep_going",
            ])
            
            if result.stdout.strip():
                targets = result.stdout.strip().split('\n')
                changed_targets.update(targets)
                print(f"    Found {len(targets)} target(s) from BUILD changes", file=sys.stderr)
        except subprocess.CalledProcessError as e:
            print(f"  Warning: Could not query BUILD-affected packages: {e}", file=sys.stderr)
    
    # Query source packages (use //package:* to get targets in package)
    if source_packages:
        # Build a single query for all source-affected packages
        source_queries = [f"{pkg}:*" for pkg in source_packages]
        query_expr = " + ".join(source_queries)
        
        print(f"  Querying {len(source_packages)} source-affected package(s)...", file=sys.stderr)
        
        try:
            result = run_bazel([
                "query",
                query_expr,
                "--output=label",
                "--keep_going",
            ])
            
            if result.stdout.strip():
                targets = result.stdout.strip().split('\n')
                changed_targets.update(targets)
                print(f"    Found {len(targets)} target(s) from source changes", file=sys.stderr)
        except subprocess.CalledProcessError as e:
            print(f"  Warning: Could not query source-affected packages: {e}", file=sys.stderr)
    
    return changed_targets


def _find_reverse_dependencies(changed_targets: Set[str], universe: str) -> Set[str]:
    """Use Bazel rdeps to find all targets that depend on the changed targets.
    
    This is the key function that uses `rdeps(universe, targets)` to find
    all targets in the universe that transitively depend on any of the changed targets.
    """
    if not changed_targets:
        return set()
    
    try:
        # Create a set expression for the changed targets
        # For a single target: "//path:target"
        # For multiple targets: "set(//path1:target1 //path2:target2 ...)"
        
        targets_list = list(changed_targets)
        if len(targets_list) == 1:
            targets_expr = targets_list[0]
        else:
            targets_expr = f"set({' '.join(targets_list)})"
        
        # Query reverse dependencies
        # rdeps(universe, targets) finds all targets in universe that depend on targets
        query = f"rdeps({universe}, {targets_expr})"
        
        print(f"  Running: bazel query 'rdeps({universe}, <{len(targets_list)} targets>)'", file=sys.stderr)
        
        result = run_bazel([
            "query",
            query,
            "--output=label",
            "--keep_going",
        ])
        
        if result.stdout.strip():
            affected = set(result.stdout.strip().split('\n'))
            return affected
        
        return set()
        
    except subprocess.CalledProcessError as e:
        print(f"Error finding reverse dependencies: {e}", file=sys.stderr)
        if e.stderr:
            print(f"stderr: {e.stderr}", file=sys.stderr)
        # Return at least the changed targets themselves
        return changed_targets


def _filter_by_kind(targets: Set[str], target_kind: str) -> List[str]:
    """Filter targets by their kind using Bazel query.
    
    Args:
        targets: Set of target labels
        target_kind: Kind pattern to filter by (e.g., "test", ".*_test", "py_binary")
    
    Returns:
        List of targets matching the kind
    """
    if not targets:
        return []
    
    try:
        # Create a set expression for the targets
        targets_list = list(targets)
        if len(targets_list) == 1:
            targets_expr = targets_list[0]
        else:
            targets_expr = f"set({' '.join(targets_list)})"
        
        # Use kind() to filter
        query = f'kind("{target_kind}", {targets_expr})'
        
        result = run_bazel([
            "query",
            query,
            "--output=label",
            "--keep_going",
        ])
        
        if result.stdout.strip():
            return result.stdout.strip().split('\n')
        
        return []
        
    except subprocess.CalledProcessError as e:
        print(f"Error filtering by kind '{target_kind}': {e}", file=sys.stderr)
        return []
# Test comment
