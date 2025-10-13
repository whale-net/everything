"""Tests for release_helper CLI commands."""

from unittest.mock import MagicMock, patch

import pytest
from typer.testing import CliRunner

from tools.release_helper.cli import app, plan_openapi_builds


runner = CliRunner()


class TestPlanOpenapiBuilds:
    """Test cases for plan_openapi_builds command."""

    @patch('tools.release_helper.cli.list_all_apps')
    def test_parses_space_separated_apps(self, mock_list_all_apps):
        """Test that plan_openapi_builds correctly parses space-separated app list."""
        # Setup mock to return apps with OpenAPI spec targets
        mock_list_all_apps.return_value = [
            {
                'name': 'app1',
                'domain': 'test',
                'openapi_spec_target': '//test:app1_openapi_spec'
            },
            {
                'name': 'app2',
                'domain': 'test',
                'openapi_spec_target': '//test:app2_openapi_spec'
            },
            {
                'name': 'app3',
                'domain': 'test',
                # No openapi_spec_target
            },
        ]
        
        # Call with space-separated list (as workflow does)
        result = runner.invoke(app, ['plan-openapi-builds', '--apps', 'app1 app2 app3', '--format', 'github'])
        
        # Should succeed
        assert result.exit_code == 0
        
        # Should output matrix with app1 and app2 (which have openapi_spec_target)
        assert 'matrix=' in result.stdout
        assert '"app": "app1"' in result.stdout
        assert '"app": "app2"' in result.stdout
        assert '"app": "app3"' not in result.stdout  # No openapi_spec_target
        
        # Should output apps list
        assert 'apps=app1 app2' in result.stdout

    @patch('tools.release_helper.cli.list_all_apps')
    def test_handles_empty_result(self, mock_list_all_apps):
        """Test that plan_openapi_builds handles case where no apps have OpenAPI specs."""
        # Setup mock to return apps without OpenAPI spec targets
        mock_list_all_apps.return_value = [
            {
                'name': 'app1',
                'domain': 'test',
                # No openapi_spec_target
            },
        ]
        
        # Call with app that has no OpenAPI spec
        result = runner.invoke(app, ['plan-openapi-builds', '--apps', 'app1', '--format', 'github'])
        
        # Should succeed
        assert result.exit_code == 0
        
        # Should output empty matrix
        assert 'matrix={}' in result.stdout
        assert 'apps=' in result.stdout

    @patch('tools.release_helper.cli.list_all_apps')
    def test_regression_comma_separated_would_fail(self, mock_list_all_apps):
        """Regression test: comma-separated parsing would treat entire list as one app name."""
        # Setup mock
        mock_list_all_apps.return_value = [
            {
                'name': 'app1',
                'domain': 'test',
                'openapi_spec_target': '//test:app1_openapi_spec'
            },
        ]
        
        # If we were using comma split, this would be treated as one app: "app1 app2"
        # which wouldn't match any real app
        result = runner.invoke(app, ['plan-openapi-builds', '--apps', 'app1 app2', '--format', 'github'])
        
        # Should succeed and find app1
        assert result.exit_code == 0
        assert '"app": "app1"' in result.stdout
