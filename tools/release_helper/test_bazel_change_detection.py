"""
Unit tests for the Bazel-based change detection approach.

Tests the improved change detection that relies on Bazel's dependency 
analysis rather than making assumptions about "infrastructure" changes.
"""

from tools.release_helper.changes import _is_infrastructure_change, _should_ignore_file


class TestBazelBasedChangeDetection:
    """Test the improved Bazel-based change detection logic."""

    def test_infrastructure_detection_always_false(self):
        """Test that infrastructure detection is deprecated and always returns False."""
        # Even files that were previously considered "infrastructure" should return False
        # since we now rely entirely on Bazel dependency analysis
        test_cases = [
            ['tools/release.bzl'],
            ['MODULE.bazel'],
            ['.github/workflows/ci.yml'],
            ['docker/Dockerfile'],
            ['tools/release_helper/cli.py'],
            ['.github/workflows/release.yml'],
            ['demo/hello_python/main.py'],
            []  # empty list
        ]
        
        for files in test_cases:
            assert _is_infrastructure_change(files) is False, f"Infrastructure detection should always return False for {files}"

    def test_original_problematic_case_uses_bazel_analysis(self):
        """Test that the original problematic case now uses Bazel analysis."""
        # The original issue: these files triggered infrastructure detection
        # and caused all apps to be built
        problematic_files = [
            '.github/workflows/release.yml',
            'tools/release_helper/cli.py',
            'tools/release_helper/github_release.py'
        ]
        
        # With the new approach, these should NOT be considered infrastructure
        assert _is_infrastructure_change(problematic_files) is False
        
        # Instead, Bazel dependency analysis will determine if any apps
        # actually depend on these files. Since these are release automation
        # files that don't affect app builds, Bazel should return 0 affected apps.

    def test_release_helper_files_are_ignored(self):
        """Test that release helper files are properly ignored."""
        # These files are part of the release automation system and should not trigger app builds
        release_helper_files = [
            'tools/release_helper/cli.py',
            'tools/release_helper/helm.py',
            'tools/release_helper/github_release.py',
            'tools/release_helper/changes.py',
            'tools/release_helper/release.py',
            'tools/release_helper/metadata.py',
        ]
        
        for file_path in release_helper_files:
            assert _should_ignore_file(file_path) is True, f"Release helper file should be ignored: {file_path}"
    
    def test_build_files_are_not_ignored(self):
        """Test that actual build files (bzl, BUILD, etc.) are not ignored."""
        # These files affect app builds and should NOT be ignored
        build_files = [
            'tools/release.bzl',
            'tools/helm/helm.bzl',
            'tools/container_image.bzl',
            'tools/python_binary.bzl',
            'tools/go_binary.bzl',
            'tools/BUILD.bazel',
            'BUILD.bazel',
            'MODULE.bazel',
        ]
        
        for file_path in build_files:
            assert _should_ignore_file(file_path) is False, f"Build file should NOT be ignored: {file_path}"
    
    def test_app_code_files_are_not_ignored(self):
        """Test that app source code files are not ignored."""
        # App code should never be ignored
        app_files = [
            'demo/hello_python/main.py',
            'demo/hello_go/main.go',
            'demo/hello_fastapi/main.py',
            'manman/src/worker/main.py',
            'libs/python/utils.py',
        ]
        
        for file_path in app_files:
            assert _should_ignore_file(file_path) is False, f"App code should NOT be ignored: {file_path}"
    
    def test_workflow_and_doc_files_are_ignored(self):
        """Test that workflows and documentation files are ignored."""
        ignored_files = [
            '.github/workflows/ci.yml',
            '.github/workflows/release.yml',
            '.github/actions/setup/action.yml',
            'docs/HELM_RELEASE.md',
            'docs/README.md',
            'README.md',
            'AGENT.md',
        ]
        
        for file_path in ignored_files:
            assert _should_ignore_file(file_path) is True, f"File should be ignored: {file_path}"