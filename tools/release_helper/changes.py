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
    
    # Build file queries for changed files, batching to avoid command line length limits
    # Bazel can handle large queries, but we batch at 50 files to be safe
    BATCH_SIZE = 50
    all_affected_metadata = set()
    
    for i in range(0, len(relevant_files), BATCH_SIZE):
        batch = relevant_files[i:i+BATCH_SIZE]
        file_queries = [f"attr('srcs', '{f}', //...)" for f in batch if f]
        
        if not file_queries:
            continue
        
        changed_targets_query = " + ".join(file_queries)
        
        try:
            # Single Bazel query that does everything:
            # 1. Find all app_metadata targets
            # 2. Get their binary targets via deps()
            # 3. Find targets using changed files in this batch
            # 4. Filter out platform/config_setting
            # 5. Find which app binaries depend on the changed targets
            # 6. Return the metadata targets for those apps
            result = run_bazel([
                "query",
                f"let changed_files = {changed_targets_query} in "
                f"let changed = $changed_files - kind('platform|config_setting', $changed_files) in "
                f"let all_metadata = kind('app_metadata', //...) in "
                f"let all_binaries = deps($all_metadata, 1) - $all_metadata in "
                f"let affected_binaries = rdeps($all_binaries, $changed) in "
                f"rdeps($all_metadata, $affected_binaries, 1) - $affected_binaries",
                "--output=label"
            ])
            
            if result.stdout.strip():
                batch_affected = set(result.stdout.strip().split('\n'))
                all_affected_metadata.update(batch_affected)
                
        except subprocess.CalledProcessError:
            # Batch might not affect any apps
            continue
    
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