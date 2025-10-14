"""
Unit tests for the CLI plan_openapi_builds command.

This module tests the plan_openapi_builds CLI command to ensure it outputs
apps in the correct domain-app format for GitHub Actions workflows.
"""

import pytest
from unittest.mock import patch, MagicMock
from typer.testing import CliRunner

from tools.release_helper.cli import app


@pytest.fixture
def mock_validated_apps_with_openapi():
    """Mock validated apps with OpenAPI spec targets."""
    return [
        {
            'name': 'hello-fastapi',
            'domain': 'demo',
            'openapi_spec_target': '//demo/hello-fastapi:hello-fastapi_openapi_spec'
        },
        {
            'name': 'experience-api',
            'domain': 'manman',
            'openapi_spec_target': '//manman/experience-api:experience-api_openapi_spec'
        }
    ]


@pytest.fixture
def mock_validated_apps_without_openapi():
    """Mock validated apps without OpenAPI spec targets."""
    return [
        {
            'name': 'hello-python',
            'domain': 'demo',
        }
    ]


class TestPlanOpenAPIBuilds:
    """Test cases for plan_openapi_builds CLI command."""

    @patch('tools.release_helper.cli.validate_apps')
    def test_plan_openapi_builds_uses_domain_app_format(self, mock_validate_apps, mock_validated_apps_with_openapi):
        """Test that plan_openapi_builds outputs apps in domain-app format."""
        mock_validate_apps.return_value = mock_validated_apps_with_openapi
        
        runner = CliRunner()
        result = runner.invoke(app, ['plan-openapi-builds', '--apps', 'hello-fastapi,experience-api', '--format', 'github'])
        
        assert result.exit_code == 0
        output = result.stdout
        
        # Verify the apps output uses domain-app format
        assert 'apps=demo-hello-fastapi manman-experience-api' in output
        
        # Verify it doesn't use short names
        assert 'apps=hello-fastapi' not in output
        assert 'apps=experience-api' not in output

    @patch('tools.release_helper.cli.validate_apps')
    def test_plan_openapi_builds_filters_apps_without_specs(self, mock_validate_apps, mock_validated_apps_without_openapi):
        """Test that apps without OpenAPI specs are filtered out."""
        mock_validate_apps.return_value = mock_validated_apps_without_openapi
        
        runner = CliRunner()
        result = runner.invoke(app, ['plan-openapi-builds', '--apps', 'hello-python', '--format', 'github'])
        
        assert result.exit_code == 0
        output = result.stdout
        
        # Should output empty matrix and apps
        assert 'matrix={}' in output
        assert 'apps=' in output
        # Verify no apps are listed
        lines = output.strip().split('\n')
        apps_line = [line for line in lines if line.startswith('apps=')][0]
        assert apps_line == 'apps='

    @patch('tools.release_helper.cli.validate_apps')
    def test_plan_openapi_builds_mixed_apps(self, mock_validate_apps):
        """Test with mixed apps (some with specs, some without)."""
        mixed_apps = [
            {
                'name': 'hello-fastapi',
                'domain': 'demo',
                'openapi_spec_target': '//demo/hello-fastapi:hello-fastapi_openapi_spec'
            },
            {
                'name': 'hello-python',
                'domain': 'demo',
                # No openapi_spec_target
            }
        ]
        mock_validate_apps.return_value = mixed_apps
        
        runner = CliRunner()
        result = runner.invoke(app, ['plan-openapi-builds', '--apps', 'hello-fastapi,hello-python', '--format', 'github'])
        
        assert result.exit_code == 0
        output = result.stdout
        
        # Should only include the app with OpenAPI spec
        assert 'apps=demo-hello-fastapi' in output
        # Should not include the app without spec
        assert 'demo-hello-python' not in output

    @patch('tools.release_helper.cli.validate_apps')
    def test_plan_openapi_builds_matrix_format(self, mock_validate_apps, mock_validated_apps_with_openapi):
        """Test that the matrix output includes domain field."""
        mock_validate_apps.return_value = mock_validated_apps_with_openapi
        
        runner = CliRunner()
        result = runner.invoke(app, ['plan-openapi-builds', '--apps', 'hello-fastapi,experience-api', '--format', 'github'])
        
        assert result.exit_code == 0
        output = result.stdout
        
        # Verify matrix includes both app and domain fields
        assert '"app": "hello-fastapi"' in output
        assert '"domain": "demo"' in output
        assert '"app": "experience-api"' in output
        assert '"domain": "manman"' in output
        assert '"openapi_target":' in output
