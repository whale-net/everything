"""
Backward compatibility module for images.py imports.

DEPRECATED: Import from tools.release_helper.containers instead:
    from tools.release_helper.containers import build_image
"""

# Lazy import to avoid circular dependencies
def __getattr__(name):
    from tools.release_helper.containers import image_ops
    return getattr(image_ops, name)

__all__ = [
    'build_image',
    'format_registry_tags',
    'push_image_with_tags',
    'get_image_targets',
]

