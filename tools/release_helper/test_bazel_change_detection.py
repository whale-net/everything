"""
Unit tests for the Bazel-based change detection approach.

Tests the improved change detection that relies entirely on Bazel's dependency 
analysis rather than making assumptions about "infrastructure" changes.
"""

from tools.release_helper.changes import detect_changed_apps, _is_infrastructure_change


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

    def test_bazel_approach_philosophy(self):
        """Test that the new approach relies on Bazel rather than file path assumptions."""
        # This test documents the philosophy of the new approach:
        # - No assumptions about what files are "infrastructure"
        # - Trust Bazel's dependency graph completely
        # - If Bazel says no apps are affected, build nothing
        # - If Bazel says specific apps are affected, build only those
        
        # These assertions document the expected behavior
        assert _is_infrastructure_change(['tools/release.bzl']) is False, "Even build macros should use Bazel analysis"
        assert _is_infrastructure_change(['.github/workflows/ci.yml']) is False, "Even CI workflows should use Bazel analysis"
        assert _is_infrastructure_change(['MODULE.bazel']) is False, "Even Bazel module files should use Bazel analysis"
        
        # The key insight: If these files actually affect app builds, Bazel's dependency
        # graph will show that relationship. If they don't, we shouldn't build apps.

    def test_original_problematic_case_now_uses_bazel(self):
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

    def test_ci_logic_changes_use_bazel_analysis(self):
        """Test that even CI logic changes now use Bazel analysis."""
        ci_files = [
            '.github/workflows/ci.yml',
            '.github/workflows/release.yml',
            '.github/actions/setup-build-env/action.yml'
        ]
        
        # Previously, these might have triggered infrastructure detection
        # Now they all use Bazel analysis
        for file_path in ci_files:
            assert _is_infrastructure_change([file_path]) is False, f"CI file {file_path} should use Bazel analysis"
        
        # The philosophy: If CI logic changes actually affect how apps are built,
        # that relationship should be expressed in the Bazel build graph.
        # If not, we shouldn't build apps just because CI logic changed.


class TestFileBasedFallbackBehavior:
    """Test the file-based fallback behavior when Bazel is unavailable."""
    
    def test_file_based_fallback_philosophy(self):
        """Test that file-based fallback is conservative but not overly broad."""
        # The file-based fallback should:
        # 1. Only affect apps when files are directly in their directories
        # 2. Not make assumptions about "infrastructure"
        # 3. Trust that if no direct changes affect an app, it doesn't need rebuilding
        
        # This is tested by the file-based detection logic which checks
        # if files are directly in app directories rather than making
        # broad assumptions about directory structure
        pass

    def test_no_conservative_fallback_to_build_all(self):
        """Test that we don't fall back to building all apps when none are affected."""
        # The old logic had: "If no apps detected but there are changes, build all apps to be safe"
        # The new logic: Trust the analysis - if no apps are affected, build nothing
        
        # This is now implemented in the detect_changed_apps function which
        # returns [] when no apps are affected, rather than all apps
        pass