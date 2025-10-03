"""
Backward compatibility module for release_notes.py imports.

DEPRECATED: Import from tools.release_helper.github instead:
    from tools.release_helper.github import generate_release_notes
"""

from tools.release_helper.github.notes import *

__all__ = [
    'generate_release_notes',
    'generate_release_notes_for_all_apps',
]
