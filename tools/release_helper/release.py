"""
Backward compatibility module for release.py imports.

DEPRECATED: Import from tools.release_helper.containers instead:
    from tools.release_helper.containers import plan_release
"""

from tools.release_helper.containers.release_ops import *

__all__ = [
    'find_app_bazel_target',
    'plan_release',
    'tag_and_push_image',
]
