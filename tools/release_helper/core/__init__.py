"""Core utilities for release helper."""

# Lazy imports to avoid circular dependencies
def __getattr__(name):
    if name in ('run_bazel', 'find_workspace_root'):
        from tools.release_helper.core import bazel
        return getattr(bazel, name)
    elif name in ('get_previous_tag', 'get_latest_app_version', 'get_latest_helm_chart_version',
                  'auto_increment_version', 'format_git_tag', 'create_git_tag', 'push_git_tag'):
        from tools.release_helper.core import git_ops
        return getattr(git_ops, name)
    elif name in ('validate_release_version', 'validate_semantic_version', 'validate_apps'):
        from tools.release_helper.core import validate
        return getattr(validate, name)
    raise AttributeError(f"module {__name__!r} has no attribute {name!r}")

__all__ = [
    'run_bazel',
    'find_workspace_root',
    'get_previous_tag',
    'get_latest_app_version',
    'get_latest_helm_chart_version',
    'auto_increment_version',
    'format_git_tag',
    'create_git_tag',
    'push_git_tag',
    'validate_release_version',
    'validate_semantic_version',
    'validate_apps',
]
