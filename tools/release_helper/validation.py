"""
Backward compatibility module for validation.py imports.

DEPRECATED: Import from tools.release_helper.core instead:
    from tools.release_helper.core import validate_release_version
"""

from tools.release_helper.core.validate import *

__all__ = [
    'validate_release_version',
    'validate_semantic_version',
    'validate_apps',
]
