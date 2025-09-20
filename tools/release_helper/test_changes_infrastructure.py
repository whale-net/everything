"""
Unit tests for infrastructure change detection improvements.

Tests the fix for change detection being too broad about infrastructure changes.
"""

from tools.release_helper.changes import _is_infrastructure_change


class TestInfrastructureChangeDetection:
    """Test the improved infrastructure change detection logic."""

    def test_release_helper_cli_not_infrastructure(self):
        """Test that changes to release helper CLI are not considered infrastructure."""
        files = ['tools/release_helper/cli.py']
        assert _is_infrastructure_change(files) is False

    def test_release_helper_github_release_not_infrastructure(self):
        """Test that changes to GitHub release functionality are not infrastructure."""
        files = ['tools/release_helper/github_release.py']
        assert _is_infrastructure_change(files) is False

    def test_multiple_release_helper_files_not_infrastructure(self):
        """Test that multiple release helper files are not infrastructure."""
        files = [
            'tools/release_helper/cli.py',
            'tools/release_helper/github_release.py',
            'tools/release_helper/release_notes.py'
        ]
        assert _is_infrastructure_change(files) is False

    def test_actual_build_tools_are_infrastructure(self):
        """Test that actual build tools are still considered infrastructure."""
        build_tool_files = [
            'tools/release.bzl',
            'tools/oci.bzl',
            'tools/BUILD.bazel',
            'tools/helm_chart_release.bzl',
            'tools/version_resolver.py'
        ]
        
        for file_path in build_tool_files:
            assert _is_infrastructure_change([file_path]) is True, f"{file_path} should be infrastructure"

    def test_ci_workflow_is_infrastructure(self):
        """Test that CI workflow is still considered infrastructure."""
        files = ['.github/workflows/ci.yml']
        assert _is_infrastructure_change(files) is True

    def test_release_workflow_not_infrastructure(self):
        """Test that release workflow is not considered infrastructure."""
        files = ['.github/workflows/release.yml']
        assert _is_infrastructure_change(files) is False

    def test_build_actions_are_infrastructure(self):
        """Test that build-related GitHub Actions are infrastructure."""
        files = ['.github/actions/setup-build-env/action.yml']
        assert _is_infrastructure_change(files) is True

    def test_non_build_actions_not_infrastructure(self):
        """Test that non-build GitHub Actions are not infrastructure."""
        files = ['.github/actions/some-other-action/action.yml']
        assert _is_infrastructure_change(files) is False

    def test_root_bazel_files_are_infrastructure(self):
        """Test that root Bazel files are still infrastructure."""
        root_files = ['MODULE.bazel', 'BUILD.bazel', '.bazelrc', 'WORKSPACE']
        
        for file_path in root_files:
            assert _is_infrastructure_change([file_path]) is True, f"{file_path} should be infrastructure"

    def test_docker_files_are_infrastructure(self):
        """Test that Docker files are still infrastructure."""
        files = ['docker/Dockerfile', 'docker/some-config.yml']
        
        for file_path in files:
            assert _is_infrastructure_change([file_path]) is True, f"{file_path} should be infrastructure"

    def test_original_problematic_case(self):
        """Test the original problematic case from the GitHub Action."""
        files = [
            '.github/workflows/release.yml',
            'tools/release_helper/cli.py',
            'tools/release_helper/github_release.py'
        ]
        # This should NOT be considered infrastructure with the fix
        assert _is_infrastructure_change(files) is False

    def test_app_files_not_infrastructure(self):
        """Test that app files are not infrastructure."""
        files = [
            'demo/hello_python/main.py',
            'demo/hello_go/main.go',
            'libs/python/utils.py'
        ]
        
        for file_path in files:
            assert _is_infrastructure_change([file_path]) is False, f"{file_path} should not be infrastructure"

    def test_documentation_not_infrastructure(self):
        """Test that documentation changes are not infrastructure."""
        files = [
            '.github/copilot-instructions.md',
            'README.md',
            'docs/something.md'
        ]
        
        for file_path in files:
            assert _is_infrastructure_change([file_path]) is False, f"{file_path} should not be infrastructure"