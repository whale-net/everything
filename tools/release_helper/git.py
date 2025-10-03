"""
Backward compatibility module for git.py imports.

DEPRECATED: Import from tools.release_helper.core instead:
    from tools.release_helper.core import get_previous_tag
"""

from tools.release_helper.core.git_ops import *

# Re-export for backward compatibility
__all__ = [
    'get_previous_tag',
    'get_latest_app_version',
    'get_latest_helm_chart_version',
    'auto_increment_version',
    'format_git_tag',
    'create_git_tag',
    'push_git_tag',
]
