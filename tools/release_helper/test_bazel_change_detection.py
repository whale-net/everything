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

    def test_tools_test_files_are_filtered(self):
        """Test that test files in tools/ directory are filtered out."""
        # Test files in tools/ test the build infrastructure itself, not app code
        # They should be filtered to avoid triggering unnecessary rebuilds
        test_files = [
            'tools/helm/composer_test.go',
            'tools/helm/types_test.go',
            'tools/release_helper/test_changes.py',
            'tools/release_helper/test_metadata.py',
            'tools/release_helper/test_bazel_change_detection.py',
        ]
        
        for file_path in test_files:
            assert _should_ignore_file(file_path) is True, f"Test file should be filtered: {file_path}"
    
    def test_tools_source_files_not_filtered(self):
        """Test that actual source files in tools/ are NOT filtered."""
        # Real infrastructure code should trigger Bazel analysis
        source_files = [
            'tools/helm/composer.go',
            'tools/helm/types.go',
            'tools/helm/helm.bzl',
            'tools/release.bzl',
            'tools/release_helper/changes.py',
            'tools/release_helper/cli.py',
            'tools/version_resolver.py',
        ]
        
        for file_path in source_files:
            assert _should_ignore_file(file_path) is False, f"Source file should NOT be filtered: {file_path}"
    
    def test_app_test_files_not_filtered(self):
        """Test that app test files are NOT filtered."""
        # App tests should trigger their app rebuilds
        app_test_files = [
            'demo/hello_python/test_main.py',
            'demo/hello_go/main_test.go',
            'manman/src/worker/subscriber_test.py',
        ]
        
        for file_path in app_test_files:
            assert _should_ignore_file(file_path) is False, f"App test should NOT be filtered: {file_path}"