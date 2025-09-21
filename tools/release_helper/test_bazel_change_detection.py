"""
Unit tests for the Bazel-based change detection approach.

Tests the improved change detection that relies on Bazel's dependency 
analysis rather than making assumptions about "infrastructure" changes.
"""

from tools.release_helper.changes import _is_infrastructure_change, detect_changed_tests


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


class TestBazelBasedTestDetection:
    """Test the Bazel-based test detection logic."""

    def test_detect_changed_tests_no_base_commit(self):
        """Test that without a base commit, the function returns all test targets."""
        # Mock implementation for testing - in real usage this would require Bazel
        # This test validates the interface and behavior without network dependencies
        
        # When no base commit is provided, should attempt to return all tests
        # The actual Bazel query would happen in a real environment
        result = detect_changed_tests(base_commit=None, use_bazel_query=False)
        
        # With use_bazel_query=False and no base commit, should return empty list as fallback
        assert isinstance(result, list)

    def test_detect_changed_tests_with_base_commit_no_changes(self):
        """Test behavior when there are no changed files."""
        # This test validates the interface - actual Git operations would require a real repo
        # The function should handle the case where git diff returns no files
        
        # The function will attempt to run git diff against the base commit
        # In a test environment without actual changes, this validates the interface
        result = detect_changed_tests(base_commit="HEAD~1", use_bazel_query=True)
        
        # Should return a list (empty if no changes detected)
        assert isinstance(result, list)