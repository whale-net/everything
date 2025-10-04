"""
Unit tests for helm chart change detection.
"""

import subprocess
import sys
from unittest.mock import MagicMock, patch, call
import pytest

from tools.release_helper.changes import detect_changed_helm_charts


class TestDetectChangedHelmCharts:
    """Test the detect_changed_helm_charts function."""

    @patch('tools.release_helper.changes.list_all_helm_charts')
    def test_no_base_commit_returns_all_charts(self, mock_list_all_helm_charts):
        """Test that without base commit, all charts are returned."""
        mock_charts = [
            {'bazel_target': '//demo:chart1_metadata', 'name': 'chart1', 'domain': 'demo'},
            {'bazel_target': '//demo:chart2_metadata', 'name': 'chart2', 'domain': 'demo'},
        ]
        mock_list_all_helm_charts.return_value = mock_charts
        
        result = detect_changed_helm_charts(base_commit=None)
        
        assert result == mock_charts
        mock_list_all_helm_charts.assert_called_once()

    @patch('tools.release_helper.changes.list_all_helm_charts')
    @patch('tools.release_helper.changes._get_changed_files')
    def test_no_changed_files(self, mock_get_changed_files, mock_list_all_helm_charts):
        """Test when no files have changed."""
        mock_list_all_helm_charts.return_value = [
            {'bazel_target': '//demo:chart1_metadata', 'name': 'chart1', 'domain': 'demo'},
        ]
        mock_get_changed_files.return_value = []
        
        result = detect_changed_helm_charts(base_commit="main")
        
        assert result == []

    @patch('tools.release_helper.changes.list_all_helm_charts')
    @patch('tools.release_helper.changes._get_changed_files')
    @patch('tools.release_helper.changes._should_ignore_file')
    def test_all_files_ignored(self, mock_should_ignore, mock_get_changed_files, mock_list_all_helm_charts):
        """Test when all changed files should be ignored."""
        mock_list_all_helm_charts.return_value = [
            {'bazel_target': '//demo:chart1_metadata', 'name': 'chart1', 'domain': 'demo'},
        ]
        mock_get_changed_files.return_value = ['.github/workflows/ci.yml', 'README.md']
        mock_should_ignore.return_value = True
        
        result = detect_changed_helm_charts(base_commit="main")
        
        assert result == []

    @patch('tools.release_helper.changes.list_all_helm_charts')
    @patch('tools.release_helper.changes._get_changed_files')
    @patch('tools.release_helper.changes._should_ignore_file')
    @patch('tools.release_helper.changes.run_bazel')
    def test_changed_chart_detected(self, mock_run_bazel, mock_should_ignore, mock_get_changed_files, mock_list_all_helm_charts):
        """Test that a changed chart is correctly detected."""
        mock_charts = [
            {'bazel_target': '//demo:chart1_chart_metadata', 'name': 'chart1', 'domain': 'demo'},
            {'bazel_target': '//demo:chart2_chart_metadata', 'name': 'chart2', 'domain': 'demo'},
        ]
        mock_list_all_helm_charts.return_value = mock_charts
        mock_get_changed_files.return_value = ['demo/app1/main.py']
        mock_should_ignore.return_value = False
        
        # Mock bazel queries
        def bazel_side_effect(args):
            mock_result = MagicMock()
            if 'kind' in args and 'helm_chart_metadata' in args[1]:
                # Return all chart metadata targets
                mock_result.stdout = '//demo:chart1_chart_metadata\n//demo:chart2_chart_metadata'
            elif args[0] == 'query' and 'rdeps' in args[1]:
                # Return affected chart metadata
                mock_result.stdout = '//demo:chart1_chart_metadata'
            else:
                # Validate file labels
                mock_result.stdout = '//demo/app1:main.py'
            return mock_result
        
        mock_run_bazel.side_effect = bazel_side_effect
        
        result = detect_changed_helm_charts(base_commit="main")
        
        assert len(result) == 1
        assert result[0]['name'] == 'chart1'

    @patch('tools.release_helper.changes.list_all_helm_charts')
    @patch('tools.release_helper.changes._get_changed_files')
    @patch('tools.release_helper.changes._should_ignore_file')
    @patch('tools.release_helper.changes.run_bazel')
    def test_multiple_charts_affected(self, mock_run_bazel, mock_should_ignore, mock_get_changed_files, mock_list_all_helm_charts):
        """Test that multiple affected charts are detected."""
        mock_charts = [
            {'bazel_target': '//demo:chart1_chart_metadata', 'name': 'chart1', 'domain': 'demo'},
            {'bazel_target': '//demo:chart2_chart_metadata', 'name': 'chart2', 'domain': 'demo'},
            {'bazel_target': '//manman:chart3_chart_metadata', 'name': 'chart3', 'domain': 'manman'},
        ]
        mock_list_all_helm_charts.return_value = mock_charts
        mock_get_changed_files.return_value = ['libs/python/utils.py']
        mock_should_ignore.return_value = False
        
        # Mock bazel queries
        def bazel_side_effect(args):
            mock_result = MagicMock()
            if 'kind' in args and 'helm_chart_metadata' in args[1]:
                # Return all chart metadata targets
                mock_result.stdout = '//demo:chart1_chart_metadata\n//demo:chart2_chart_metadata\n//manman:chart3_chart_metadata'
            elif args[0] == 'query' and 'rdeps' in args[1]:
                # All charts depend on shared library
                mock_result.stdout = '//demo:chart1_chart_metadata\n//demo:chart2_chart_metadata\n//manman:chart3_chart_metadata'
            else:
                # Validate file labels
                mock_result.stdout = '//libs/python:utils.py'
            return mock_result
        
        mock_run_bazel.side_effect = bazel_side_effect
        
        result = detect_changed_helm_charts(base_commit="main")
        
        assert len(result) == 3
        assert {r['name'] for r in result} == {'chart1', 'chart2', 'chart3'}

    @patch('tools.release_helper.changes.list_all_helm_charts')
    @patch('tools.release_helper.changes._get_changed_files')
    @patch('tools.release_helper.changes._should_ignore_file')
    @patch('tools.release_helper.changes.run_bazel')
    def test_build_file_change_affects_package(self, mock_run_bazel, mock_should_ignore, mock_get_changed_files, mock_list_all_helm_charts):
        """Test that BUILD file changes affect the entire package."""
        mock_charts = [
            {'bazel_target': '//demo:chart1_chart_metadata', 'name': 'chart1', 'domain': 'demo'},
        ]
        mock_list_all_helm_charts.return_value = mock_charts
        mock_get_changed_files.return_value = ['demo/BUILD.bazel']
        mock_should_ignore.return_value = False
        
        # Mock bazel queries
        def bazel_side_effect(args):
            mock_result = MagicMock()
            if 'kind' in args and 'helm_chart_metadata' in args[1]:
                mock_result.stdout = '//demo:chart1_chart_metadata'
            elif args[0] == 'query' and 'rdeps' in args[1]:
                # Check that we're querying with package wildcard
                assert '//demo/...' in args[1]
                mock_result.stdout = '//demo:chart1_chart_metadata'
            else:
                # No individual file labels for BUILD files
                mock_result.stdout = ''
            return mock_result
        
        mock_run_bazel.side_effect = bazel_side_effect
        
        result = detect_changed_helm_charts(base_commit="main")
        
        assert len(result) == 1
        assert result[0]['name'] == 'chart1'

    @patch('tools.release_helper.changes.list_all_helm_charts')
    @patch('tools.release_helper.changes._get_changed_files')
    @patch('tools.release_helper.changes._should_ignore_file')
    @patch('tools.release_helper.changes.run_bazel')
    def test_no_charts_affected(self, mock_run_bazel, mock_should_ignore, mock_get_changed_files, mock_list_all_helm_charts):
        """Test when changes don't affect any charts."""
        mock_charts = [
            {'bazel_target': '//demo:chart1_chart_metadata', 'name': 'chart1', 'domain': 'demo'},
        ]
        mock_list_all_helm_charts.return_value = mock_charts
        mock_get_changed_files.return_value = ['other/file.py']
        mock_should_ignore.return_value = False
        
        # Mock bazel queries
        def bazel_side_effect(args):
            mock_result = MagicMock()
            if 'kind' in args and 'helm_chart_metadata' in args[1]:
                mock_result.stdout = '//demo:chart1_chart_metadata'
            elif args[0] == 'query' and 'rdeps' in args[1]:
                # No charts affected
                mock_result.stdout = ''
            else:
                mock_result.stdout = '//other:file.py'
            return mock_result
        
        mock_run_bazel.side_effect = bazel_side_effect
        
        result = detect_changed_helm_charts(base_commit="main")
        
        assert result == []

    @patch('tools.release_helper.changes.list_all_helm_charts')
    @patch('tools.release_helper.changes._get_changed_files')
    @patch('tools.release_helper.changes._should_ignore_file')
    @patch('tools.release_helper.changes.run_bazel')
    def test_bazel_query_error_handling(self, mock_run_bazel, mock_should_ignore, mock_get_changed_files, mock_list_all_helm_charts):
        """Test error handling when bazel query fails."""
        mock_charts = [
            {'bazel_target': '//demo:chart1_chart_metadata', 'name': 'chart1', 'domain': 'demo'},
        ]
        mock_list_all_helm_charts.return_value = mock_charts
        mock_get_changed_files.return_value = ['demo/main.py']
        mock_should_ignore.return_value = False
        
        # Mock bazel queries - simulate error
        def bazel_side_effect(args):
            if 'kind' in args and 'helm_chart_metadata' in args[1]:
                raise subprocess.CalledProcessError(1, "bazel query")
            return MagicMock()
        
        mock_run_bazel.side_effect = bazel_side_effect
        
        result = detect_changed_helm_charts(base_commit="main")
        
        # Should return empty list on error
        assert result == []
