"""
Change detection utilities for the release helper.
"""

import subprocess
import sys
from typing import Dict, List, Optional

from tools.release_helper.metadata import list_all_apps


def detect_changed_apps(since_tag: Optional[str] = None) -> List[Dict[str, str]]:
    """Detect which apps have changed since a given tag.
    
    Returns:
        List of app dictionaries with bazel_target, name, and domain
    """
    all_apps = list_all_apps()

    if not since_tag:
        # No previous tag, return all apps
        return all_apps

    try:
        # Get changed files since the tag
        result = subprocess.run(
            ["git", "diff", "--name-only", f"{since_tag}..HEAD"],
            capture_output=True,
            text=True,
            check=True
        )

        changed_files = result.stdout.strip().split('\n') if result.stdout.strip() else []

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

        # If no apps changed but there are infrastructure changes, release all
        if not changed_apps and changed_dirs:
            # Check if changes are in infrastructure directories
            infra_dirs = {'tools', '.github', 'libs', 'docker'}
            if any(any(changed_dir.startswith(infra_dir) for changed_dir in changed_dirs) 
                   for infra_dir in infra_dirs):
                print(f"Infrastructure changes detected", file=sys.stderr)
                print("Releasing all apps due to infrastructure changes", file=sys.stderr)
                return all_apps

        return changed_apps

    except subprocess.CalledProcessError as e:
        print(f"Error detecting changes since {since_tag}: {e}", file=sys.stderr)
        # On error, return all apps to be safe
        return all_apps