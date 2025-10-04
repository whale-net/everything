"""
Tests to verify the rdeps query optimization in change detection.

These tests ensure that we query rdeps scoped to metadata targets,
not the entire repository (//...).
"""

import subprocess
from unittest.mock import MagicMock, patch, call
import pytest

from tools.release_helper.changes import detect_changed_apps, detect_changed_helm_charts


class TestRdepsOptimization:
    """Tests to verify the rdeps query optimization."""

    @patch('tools.release_helper.changes.list_all_apps')
    @patch('tools.release_helper.changes._get_changed_files')
    @patch('tools.release_helper.changes._should_ignore_file')
    @patch('tools.release_helper.changes.run_bazel')
    def test_app_detection_uses_scoped_rdeps(self, mock_run_bazel, mock_should_ignore, mock_get_changed_files, mock_list_all_apps):
        """Verify that app detection uses rdeps scoped to app_metadata targets."""
        mock_list_all_apps.return_value = [
            {'bazel_target': '//demo:app1_metadata', 'name': 'app1', 'domain': 'demo'},
        ]
        mock_get_changed_files.return_value = ['demo/main.py']
        mock_should_ignore.return_value = False
        
        # Mock bazel queries
        bazel_calls = []
        def bazel_side_effect(args):
            bazel_calls.append(args)
            mock_result = MagicMock()
            if 'kind' in args and 'app_metadata' in args[1]:
                mock_result.stdout = '//demo:app1_metadata'
            elif args[0] == 'query' and 'rdeps' in args[1]:
                # This is the critical query - should be scoped
                mock_result.stdout = '//demo:app1_metadata'
            else:
                mock_result.stdout = '//demo:main.py'
            return mock_result
        
        mock_run_bazel.side_effect = bazel_side_effect
        
        result = detect_changed_apps(base_commit="main")
        
        # Find the rdeps query call
        rdeps_calls = [call for call in bazel_calls if len(call) > 1 and 'rdeps' in call[1]]
        assert len(rdeps_calls) == 1, "Should have exactly one rdeps query"
        
        rdeps_query = rdeps_calls[0][1]
        # CRITICAL: Verify we're NOT querying rdeps(//..., ...)
        assert 'rdeps(//' not in rdeps_query or 'rdeps(//...' not in rdeps_query, \
            f"Should not use rdeps(//..., ...), got: {rdeps_query}"
        
        # Verify we ARE querying with metadata targets
        # The query should be: rdeps(metadata_targets, changed_files)
        assert 'metadata' in rdeps_query.lower() or len(rdeps_query) > 50, \
            f"Should scope rdeps to metadata targets, got: {rdeps_query}"

    @patch('tools.release_helper.changes.list_all_helm_charts')
    @patch('tools.release_helper.changes._get_changed_files')
    @patch('tools.release_helper.changes._should_ignore_file')
    @patch('tools.release_helper.changes.run_bazel')
    def test_helm_detection_uses_scoped_rdeps(self, mock_run_bazel, mock_should_ignore, mock_get_changed_files, mock_list_all_helm_charts):
        """Verify that helm chart detection uses rdeps scoped to helm_chart_metadata targets."""
        mock_list_all_helm_charts.return_value = [
            {'bazel_target': '//demo:chart1_chart_metadata', 'name': 'chart1', 'domain': 'demo'},
        ]
        mock_get_changed_files.return_value = ['demo/main.py']
        mock_should_ignore.return_value = False
        
        # Mock bazel queries
        bazel_calls = []
        def bazel_side_effect(args):
            bazel_calls.append(args)
            mock_result = MagicMock()
            if 'kind' in args and 'helm_chart_metadata' in args[1]:
                mock_result.stdout = '//demo:chart1_chart_metadata'
            elif args[0] == 'query' and 'rdeps' in args[1]:
                # This is the critical query - should be scoped
                mock_result.stdout = '//demo:chart1_chart_metadata'
            else:
                mock_result.stdout = '//demo:main.py'
            return mock_result
        
        mock_run_bazel.side_effect = bazel_side_effect
        
        result = detect_changed_helm_charts(base_commit="main")
        
        # Find the rdeps query call
        rdeps_calls = [call for call in bazel_calls if len(call) > 1 and 'rdeps' in call[1]]
        assert len(rdeps_calls) == 1, "Should have exactly one rdeps query"
        
        rdeps_query = rdeps_calls[0][1]
        # CRITICAL: Verify we're NOT querying rdeps(//..., ...)
        assert 'rdeps(//' not in rdeps_query or 'rdeps(//...' not in rdeps_query, \
            f"Should not use rdeps(//..., ...), got: {rdeps_query}"
        
        # Verify we ARE querying with metadata targets
        assert 'metadata' in rdeps_query.lower() or len(rdeps_query) > 50, \
            f"Should scope rdeps to metadata targets, got: {rdeps_query}"

    @patch('tools.release_helper.changes.list_all_apps')
    @patch('tools.release_helper.changes._get_changed_files')
    @patch('tools.release_helper.changes._should_ignore_file')
    @patch('tools.release_helper.changes.run_bazel')
    def test_app_detection_queries_metadata_first(self, mock_run_bazel, mock_should_ignore, mock_get_changed_files, mock_list_all_apps):
        """Verify that we query for metadata targets BEFORE the rdeps query."""
        mock_list_all_apps.return_value = [
            {'bazel_target': '//demo:app1_metadata', 'name': 'app1', 'domain': 'demo'},
        ]
        mock_get_changed_files.return_value = ['demo/main.py']
        mock_should_ignore.return_value = False
        
        # Track the order of bazel queries
        query_sequence = []
        def bazel_side_effect(args):
            if 'kind' in args and 'app_metadata' in args[1]:
                query_sequence.append('metadata_query')
                mock_result = MagicMock()
                mock_result.stdout = '//demo:app1_metadata'
                return mock_result
            elif 'rdeps' in args[1]:
                query_sequence.append('rdeps_query')
                mock_result = MagicMock()
                mock_result.stdout = '//demo:app1_metadata'
                return mock_result
            else:
                mock_result = MagicMock()
                mock_result.stdout = '//demo:main.py'
                return mock_result
        
        mock_run_bazel.side_effect = bazel_side_effect
        
        result = detect_changed_apps(base_commit="main")
        
        # Verify query order: metadata query should come BEFORE rdeps query
        assert 'metadata_query' in query_sequence, "Should query for metadata targets"
        assert 'rdeps_query' in query_sequence, "Should perform rdeps query"
        
        metadata_idx = query_sequence.index('metadata_query')
        rdeps_idx = query_sequence.index('rdeps_query')
        
        assert metadata_idx < rdeps_idx, \
            "Metadata query should come BEFORE rdeps query for optimization"

    @patch('tools.release_helper.changes.list_all_apps')
    @patch('tools.release_helper.changes._get_changed_files')
    @patch('tools.release_helper.changes._should_ignore_file')
    @patch('tools.release_helper.changes.run_bazel')
    def test_optimization_skips_unnecessary_queries(self, mock_run_bazel, mock_should_ignore, mock_get_changed_files, mock_list_all_apps):
        """Verify that with optimization, we don't query all repository targets."""
        mock_list_all_apps.return_value = [
            {'bazel_target': '//demo:app1_metadata', 'name': 'app1', 'domain': 'demo'},
        ]
        mock_get_changed_files.return_value = ['demo/main.py']
        mock_should_ignore.return_value = False
        
        bazel_calls = []
        def bazel_side_effect(args):
            bazel_calls.append(args)
            mock_result = MagicMock()
            if 'kind' in args and 'app_metadata' in args[1]:
                mock_result.stdout = '//demo:app1_metadata'
            elif args[0] == 'query' and 'rdeps' in args[1]:
                mock_result.stdout = '//demo:app1_metadata'
            else:
                mock_result.stdout = '//demo:main.py'
            return mock_result
        
        mock_run_bazel.side_effect = bazel_side_effect
        
        result = detect_changed_apps(base_commit="main")
        
        # Count rdeps queries
        rdeps_queries = [call for call in bazel_calls if len(call) > 1 and 'rdeps' in call[1]]
        
        # OPTIMIZATION: Should only have ONE rdeps query (scoped to metadata)
        # The old approach would have TWO: rdeps(//..., files) + rdeps(metadata, all_targets)
        assert len(rdeps_queries) == 1, \
            f"Optimization should use only 1 rdeps query, found {len(rdeps_queries)}"
