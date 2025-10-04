"""
Tests for Helm git interaction optimizations.
"""

import subprocess
from pathlib import Path
from unittest.mock import MagicMock, patch, call
import pytest

from tools.release_helper.helm import has_chart_changed


class TestHasChartChangedOptimization:
    """Test the optimized has_chart_changed function."""

    @patch('tools.release_helper.helm.list_all_helm_charts')
    @patch('tools.release_helper.helm.subprocess.run')
    def test_has_chart_changed_uses_exit_code(self, mock_run, mock_list_charts):
        """Test that has_chart_changed uses --exit-code flag."""
        # Mock the chart list
        mock_list_charts.return_value = [{
            'bazel_target': '//demo:test_chart_chart_metadata',
            'name': 'test-chart',
            'domain': 'demo',
            'namespace': 'demo',
            'apps': [],
            'chart_target': ':test_chart',
        }]
        
        # Mock git diff to return exit code 1 (changes found)
        mock_result = MagicMock()
        mock_result.returncode = 1
        mock_result.stdout = ""
        mock_run.return_value = mock_result
        
        result = has_chart_changed('test-chart')
        
        # Should return True (changes found)
        assert result is True
        
        # Verify git diff was called with --exit-code flag
        mock_run.assert_called_once()
        call_args = mock_run.call_args[0][0]
        assert '--exit-code' in call_args
        assert call_args == [
            "git", "diff", "--name-only", "--exit-code", 
            "HEAD~1", "HEAD", "--", "demo/"
        ]

    @patch('tools.release_helper.helm.list_all_helm_charts')
    @patch('tools.release_helper.helm.subprocess.run')
    def test_has_chart_changed_no_changes(self, mock_run, mock_list_charts):
        """Test has_chart_changed when no changes exist."""
        # Mock the chart list
        mock_list_charts.return_value = [{
            'bazel_target': '//demo:test_chart_chart_metadata',
            'name': 'test-chart',
            'domain': 'demo',
            'namespace': 'demo',
            'apps': [],
            'chart_target': ':test_chart',
        }]
        
        # Mock git diff to return exit code 0 (no changes)
        mock_result = MagicMock()
        mock_result.returncode = 0
        mock_result.stdout = ""
        mock_run.return_value = mock_result
        
        result = has_chart_changed('test-chart')
        
        # Should return False (no changes)
        assert result is False

    @patch('tools.release_helper.helm.list_all_helm_charts')
    @patch('tools.release_helper.helm.subprocess.run')
    def test_has_chart_changed_with_custom_base_commit(self, mock_run, mock_list_charts):
        """Test has_chart_changed with custom base commit."""
        # Mock the chart list
        mock_list_charts.return_value = [{
            'bazel_target': '//demo:test_chart_chart_metadata',
            'name': 'test-chart',
            'domain': 'demo',
            'namespace': 'demo',
            'apps': [],
            'chart_target': ':test_chart',
        }]
        
        # Mock git diff to return exit code 1 (changes found)
        mock_result = MagicMock()
        mock_result.returncode = 1
        mock_result.stdout = ""
        mock_run.return_value = mock_result
        
        result = has_chart_changed('test-chart', base_commit='main')
        
        # Should return True (changes found)
        assert result is True
        
        # Verify git diff was called with custom base commit
        call_args = mock_run.call_args[0][0]
        assert 'main' in call_args
        assert call_args == [
            "git", "diff", "--name-only", "--exit-code", 
            "main", "HEAD", "--", "demo/"
        ]

    @patch('tools.release_helper.helm.list_all_helm_charts')
    def test_has_chart_changed_chart_not_found(self, mock_list_charts):
        """Test has_chart_changed when chart is not found."""
        # Mock empty chart list
        mock_list_charts.return_value = []
        
        result = has_chart_changed('nonexistent-chart')
        
        # Should return True (assume changed for safety)
        assert result is True

    @patch('tools.release_helper.helm.list_all_helm_charts')
    @patch('tools.release_helper.helm.subprocess.run')
    def test_has_chart_changed_handles_exceptions(self, mock_run, mock_list_charts):
        """Test has_chart_changed handles exceptions gracefully."""
        # Mock the chart list
        mock_list_charts.return_value = [{
            'bazel_target': '//demo:test_chart_chart_metadata',
            'name': 'test-chart',
            'domain': 'demo',
            'namespace': 'demo',
            'apps': [],
            'chart_target': ':test_chart',
        }]
        
        # Mock subprocess to raise an exception
        mock_run.side_effect = Exception("Git error")
        
        result = has_chart_changed('test-chart')
        
        # Should return True (assume changed for safety)
        assert result is True


class TestPublishHelmRepoGitOptimizations:
    """Test git optimizations in publish_helm_repo_to_github_pages."""

    def test_git_clone_uses_single_branch_flag(self):
        """Test that git clone uses --single-branch flag."""
        # This is a documentation test - the actual flag usage is verified
        # by inspecting the source code in helm.py
        # The flag should be present in lines 651 and 661
        from tools.release_helper.helm import publish_helm_repo_to_github_pages
        import inspect
        
        source = inspect.getsource(publish_helm_repo_to_github_pages)
        
        # Verify --single-branch flag is present in git clone commands
        assert '--single-branch' in source
        
        # Count occurrences - should appear at least twice
        # (once for gh-pages clone, once for orphan branch clone)
        count = source.count('--single-branch')
        assert count >= 2, f"Expected at least 2 occurrences of --single-branch, found {count}"

    def test_file_removal_does_not_use_git_rm(self):
        """Test that orphan branch file removal doesn't use git rm."""
        from tools.release_helper.helm import publish_helm_repo_to_github_pages
        import inspect
        
        source = inspect.getsource(publish_helm_repo_to_github_pages)
        
        # Verify we're using direct file operations instead of git rm
        # The comment should indicate we're using manual removal
        assert 'Remove files manually instead of using git rm' in source
        assert 'shutil.rmtree' in source or 'item_path.unlink()' in source

    def test_git_diff_staged_uses_exit_code(self):
        """Test that git diff --staged uses --exit-code flag."""
        from tools.release_helper.helm import publish_helm_repo_to_github_pages
        import inspect
        
        source = inspect.getsource(publish_helm_repo_to_github_pages)
        
        # Verify --exit-code is used with git diff --staged
        assert '--exit-code' in source
        assert 'git", "diff", "--staged", "--quiet", "--exit-code"' in source
        
        # Verify capture_output is set to False for efficiency
        assert 'capture_output=False' in source
