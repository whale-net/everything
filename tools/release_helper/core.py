"""
Backward compatibility shim for core.py imports.
Import from tools.release_helper.core.bazel instead.
"""

from tools.release_helper.core.bazel import *

__all__ = ['run_bazel', 'find_workspace_root']
