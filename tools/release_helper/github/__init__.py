"""GitHub release operations."""

# Lazy imports to avoid circular dependencies
def __getattr__(name):
    if name in ('create_app_release', 'create_releases_for_apps', 'create_releases_for_apps_with_notes'):
        from tools.release_helper.github import releases
        return getattr(releases, name)
    elif name in ('generate_release_notes', 'generate_release_notes_for_all_apps'):
        from tools.release_helper.github import notes
        return getattr(notes, name)
    raise AttributeError(f"module {__name__!r} has no attribute {name!r}")

__all__ = [
    'create_app_release',
    'create_releases_for_apps',
    'create_releases_for_apps_with_notes',
    'generate_release_notes',
    'generate_release_notes_for_all_apps',
]
