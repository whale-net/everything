"""Container image building and release utilities."""

# Lazy imports to avoid circular dependencies
def __getattr__(name):
    if name in ('build_image', 'format_registry_tags', 'push_image_with_tags', 'get_image_targets'):
        from tools.release_helper.containers import image_ops
        return getattr(image_ops, name)
    elif name in ('find_app_bazel_target', 'plan_release', 'tag_and_push_image'):
        from tools.release_helper.containers import release_ops
        return getattr(release_ops, name)
    raise AttributeError(f"module {__name__!r} has no attribute {name!r}")

__all__ = [
    'build_image',
    'format_registry_tags',
    'push_image_with_tags',
    'get_image_targets',
    'find_app_bazel_target',
    'plan_release',
    'tag_and_push_image',
]
