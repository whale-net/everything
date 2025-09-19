"""
Tests for infrastructure change detection.

This module tests the logic that determines whether file changes should trigger
a full rebuild of all applications or just affected ones.
"""

try:
    import pytest
except ImportError:
    # pytest not available - tests can still be run manually
    pytest = None

from tools.release_helper.changes import _is_infrastructure_change


class TestInfrastructureChangeDetection:
    """Test cases for _is_infrastructure_change function."""

    def test_github_workflows_trigger_rebuild(self):
        """CI workflow changes should trigger full rebuild."""
        assert _is_infrastructure_change(['.github/workflows/ci.yml'])
        assert _is_infrastructure_change(['.github/workflows/new-workflow.yml'])
        assert _is_infrastructure_change(['.github/workflows/subdir/workflow.yml'])

    def test_github_actions_trigger_rebuild(self):
        """GitHub Actions changes should trigger full rebuild."""
        assert _is_infrastructure_change(['.github/actions/setup/action.yml'])
        assert _is_infrastructure_change(['.github/actions/new-action/action.yml'])

    def test_github_documentation_does_not_trigger_rebuild(self):
        """Documentation files in .github should NOT trigger full rebuild."""
        # This was the bug - copilot instructions triggered full rebuild
        assert not _is_infrastructure_change(['.github/copilot-instructions.md'])
        assert not _is_infrastructure_change(['.github/README.md'])
        assert not _is_infrastructure_change(['.github/issue_template.md'])
        assert not _is_infrastructure_change(['.github/pull_request_template.md'])

    def test_tools_directory_triggers_rebuild(self):
        """Changes to tools directory should trigger full rebuild."""
        assert _is_infrastructure_change(['tools/release.bzl'])
        assert _is_infrastructure_change(['tools/new_tool.py'])
        assert _is_infrastructure_change(['tools/subdir/script.sh'])

    def test_docker_directory_triggers_rebuild(self):
        """Changes to docker directory should trigger full rebuild."""
        assert _is_infrastructure_change(['docker/Dockerfile'])
        assert _is_infrastructure_change(['docker/config.json'])

    def test_root_bazel_files_trigger_rebuild(self):
        """Root-level Bazel configuration files should trigger full rebuild."""
        assert _is_infrastructure_change(['MODULE.bazel'])
        assert _is_infrastructure_change(['BUILD.bazel'])
        assert _is_infrastructure_change(['WORKSPACE'])
        assert _is_infrastructure_change(['WORKSPACE.bazel'])
        assert _is_infrastructure_change(['.bazelrc'])

    def test_app_and_lib_changes_do_not_trigger_rebuild(self):
        """Regular app and library changes should not trigger full rebuild."""
        assert not _is_infrastructure_change(['demo/hello_go/main.go'])
        assert not _is_infrastructure_change(['demo/hello_python/main.py'])
        assert not _is_infrastructure_change(['libs/python/utils.py'])
        assert not _is_infrastructure_change(['manman/src/config.py'])

    def test_mixed_changes_with_infrastructure(self):
        """If any change is infrastructure, should trigger full rebuild."""
        # Mix of documentation and infrastructure
        files = ['.github/copilot-instructions.md', '.github/workflows/ci.yml']
        assert _is_infrastructure_change(files)
        
        # Mix of app code and infrastructure
        files = ['demo/hello_go/main.go', 'tools/release.bzl']
        assert _is_infrastructure_change(files)

    def test_mixed_changes_without_infrastructure(self):
        """If no changes are infrastructure, should not trigger full rebuild."""
        # Mix of documentation and app code
        files = ['.github/copilot-instructions.md', 'demo/hello_go/main.go']
        assert not _is_infrastructure_change(files)
        
        # Mix of app and library code
        files = ['demo/hello_go/main.go', 'libs/python/utils.py', 'manman/src/config.py']
        assert not _is_infrastructure_change(files)

    def test_pr30_regression(self):
        """Regression test for PR #30 issue - copilot instructions + manman changes."""
        # This specific combination was triggering full rebuild incorrectly
        pr30_files = [
            '.github/copilot-instructions.md',
            'manman/BUILD.bazel',
            'manman/Tiltfile',
            'manman/__init__.py',
            'manman/src/config.py'
        ]
        assert not _is_infrastructure_change(pr30_files)

    def test_empty_and_none_values(self):
        """Handle edge cases with empty or None values."""
        assert not _is_infrastructure_change([])
        assert not _is_infrastructure_change([''])
        assert not _is_infrastructure_change([None])  # Should be handled gracefully