"""
Change detection utilities for the release helper.
"""

import subprocess
import sys
from typing import List, Optional

from tools.release_helper.metadata import list_all_apps


def detect_changed_apps(since_tag: Optional[str] = None) -> List[str]:
    """Detect which apps have changed since a given tag."""
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

        # Extract directories from changed files
        changed_dirs = set()
        for file_path in changed_files:
            if file_path:
                dir_name = file_path.split('/')[0]
                changed_dirs.add(dir_name)

        # Find apps that have changes
        changed_apps = []
        for app in all_apps:
            if app in changed_dirs:
                changed_apps.append(app)

        # If no apps changed but there are infrastructure changes, release all
        if not changed_apps and changed_dirs:
            # Check if changes are in infrastructure directories
            infra_dirs = {'tools', '.github', 'libs', 'docker'}
            if any(d in infra_dirs for d in changed_dirs):
                print(f"Infrastructure changes detected in: {', '.join(changed_dirs & infra_dirs)}", file=sys.stderr)
                print("Releasing all apps due to infrastructure changes", file=sys.stderr)
                return all_apps

        return changed_apps

    except subprocess.CalledProcessError as e:
        print(f"Error detecting changes since {since_tag}: {e}", file=sys.stderr)
        # On error, return all apps to be safe
        return all_apps