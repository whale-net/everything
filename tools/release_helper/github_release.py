"""
Backward compatibility module for github_release.py imports.

DEPRECATED: Import from tools.release_helper.github instead:
    from tools.release_helper.github import create_app_release
"""

from tools.release_helper.github.releases import *

__all__ = [
    'create_app_release',
    'create_releases_for_apps',
    'create_releases_for_apps_with_notes',
]
