"""
Unit tests for the exclude demo domain functionality.

This module tests that the `all` option correctly excludes the demo domain
by default and includes it when the --include-demo flag is used.
"""

import pytest
from unittest.mock import Mock, patch

from tools.release_helper.release import plan_release
from tools.release_helper.cli import app as typer_app
from typer.testing import CliRunner


@pytest.fixture
def mock_list_all_apps():
    """Fixture to mock list_all_apps with sample apps from different domains."""
    with patch('tools.release_helper.release.list_all_apps') as mock:
        # Mock apps from demo and manman domains
        mock.return_value = [
            {'bazel_target': '//demo/hello_python:hello-python_metadata', 'name': 'hello-python', 'domain': 'demo'},
            {'bazel_target': '//demo/hello_go:hello-go_metadata', 'name': 'hello-go', 'domain': 'demo'},
            {'bazel_target': '//demo/hello_fastapi:hello-fastapi_metadata', 'name': 'hello-fastapi', 'domain': 'demo'},
            {'bazel_target': '//manman:experience-api_metadata', 'name': 'experience-api', 'domain': 'manman'},
            {'bazel_target': '//manman:status-api_metadata', 'name': 'status-api', 'domain': 'manman'},
            {'bazel_target': '//manman:worker_metadata', 'name': 'worker', 'domain': 'manman'},
        ]
        yield mock


@pytest.fixture
def mock_list_all_helm_charts():
    """Fixture to mock list_all_helm_charts with sample charts from different domains."""
    with patch('tools.release_helper.cli.list_all_helm_charts') as mock:
        # Mock charts from demo and manman domains
        mock.return_value = [
            {'bazel_target': '//demo:fastapi_chart_metadata', 'name': 'helm-demo-hello-fastapi', 'domain': 'demo', 'namespace': 'demo', 'apps': ['hello-fastapi']},
            {'bazel_target': '//demo:worker_chart_metadata', 'name': 'helm-demo-hello-worker', 'domain': 'demo', 'namespace': 'demo', 'apps': ['hello-worker']},
            {'bazel_target': '//demo:all_types_chart_metadata', 'name': 'helm-demo-demo-all-types', 'domain': 'demo', 'namespace': 'demo', 'apps': ['hello-fastapi', 'hello-internal-api']},
            {'bazel_target': '//manman:manman_chart_metadata', 'name': 'helm-manman-host-services', 'domain': 'manman', 'namespace': 'manman', 'apps': ['experience-api', 'status-api']},
        ]
        yield mock


class TestPlanReleaseExcludeDemo:
    """Test cases for plan_release function with demo domain exclusion."""

    def test_plan_release_all_excludes_demo_by_default(self, mock_list_all_apps):
        """Test that 'all' excludes demo domain by default."""
        result = plan_release(
            event_type="workflow_dispatch",
            requested_apps="all",
            version="v1.0.0",
            include_demo=False
        )
        
        # Should only include manman apps
        app_names = [app['app'] for app in result['matrix']['include']]
        assert 'experience-api' in app_names
        assert 'status-api' in app_names
        assert 'worker' in app_names
        
        # Should not include demo apps
        assert 'hello-python' not in app_names
        assert 'hello-go' not in app_names
        assert 'hello-fastapi' not in app_names
        
        assert len(app_names) == 3  # Only 3 manman apps

    def test_plan_release_all_includes_demo_with_flag(self, mock_list_all_apps):
        """Test that 'all' includes demo domain when --include-demo is used."""
        result = plan_release(
            event_type="workflow_dispatch",
            requested_apps="all",
            version="v1.0.0",
            include_demo=True
        )
        
        # Should include all apps from both domains
        app_names = [app['app'] for app in result['matrix']['include']]
        
        # Manman apps
        assert 'experience-api' in app_names
        assert 'status-api' in app_names
        assert 'worker' in app_names
        
        # Demo apps
        assert 'hello-python' in app_names
        assert 'hello-go' in app_names
        assert 'hello-fastapi' in app_names
        
        assert len(app_names) == 6  # All 6 apps

    def test_plan_release_specific_apps_not_affected(self, mock_list_all_apps):
        """Test that specifying specific apps is not affected by include_demo flag."""
        with patch('tools.release_helper.release.validate_apps') as mock_validate:
            # Mock validate_apps to return demo apps
            mock_validate.return_value = [
                {'bazel_target': '//demo/hello_python:hello-python_metadata', 'name': 'hello-python', 'domain': 'demo'},
            ]
            
            result = plan_release(
                event_type="workflow_dispatch",
                requested_apps="hello-python",
                version="v1.0.0",
                include_demo=False  # This should not matter for specific apps
            )
            
            app_names = [app['app'] for app in result['matrix']['include']]
            assert 'hello-python' in app_names
            assert len(app_names) == 1


class TestPlanHelmReleaseExcludeDemo:
    """Test cases for plan-helm-release command with demo domain exclusion."""

    def test_plan_helm_release_all_excludes_demo_by_default(self, mock_list_all_helm_charts):
        """Test that 'all' excludes demo domain charts by default."""
        runner = CliRunner()
        result = runner.invoke(typer_app, [
            'plan-helm-release',
            '--charts', 'all',
            '--version', 'v1.0.0',
            '--format', 'json'
        ])
        
        assert result.exit_code == 0
        
        # Parse the JSON output
        import json
        output_data = json.loads(result.stdout)
        
        # Should only include manman chart
        chart_names = [chart['chart'] for chart in output_data['matrix']['include']]
        assert 'helm-manman-host-services' in chart_names
        
        # Should not include demo charts
        assert 'helm-demo-hello-fastapi' not in chart_names
        assert 'helm-demo-hello-worker' not in chart_names
        assert 'helm-demo-demo-all-types' not in chart_names
        
        assert len(chart_names) == 1  # Only 1 manman chart

    def test_plan_helm_release_all_includes_demo_with_flag(self, mock_list_all_helm_charts):
        """Test that 'all' includes demo domain charts when --include-demo is used."""
        runner = CliRunner()
        result = runner.invoke(typer_app, [
            'plan-helm-release',
            '--charts', 'all',
            '--version', 'v1.0.0',
            '--format', 'json',
            '--include-demo'
        ])
        
        assert result.exit_code == 0
        
        # Parse the JSON output
        import json
        output_data = json.loads(result.stdout)
        
        # Should include all charts from both domains
        chart_names = [chart['chart'] for chart in output_data['matrix']['include']]
        
        # Manman chart
        assert 'helm-manman-host-services' in chart_names
        
        # Demo charts
        assert 'helm-demo-hello-fastapi' in chart_names
        assert 'helm-demo-hello-worker' in chart_names
        assert 'helm-demo-demo-all-types' in chart_names
        
        assert len(chart_names) == 4  # All 4 charts

    def test_plan_helm_release_specific_charts_not_affected(self, mock_list_all_helm_charts):
        """Test that specifying specific charts is not affected by include_demo flag."""
        runner = CliRunner()
        result = runner.invoke(typer_app, [
            'plan-helm-release',
            '--charts', 'helm-demo-hello-fastapi',
            '--version', 'v1.0.0',
            '--format', 'json'
            # No --include-demo flag
        ])
        
        assert result.exit_code == 0
        
        # Parse the JSON output
        import json
        output_data = json.loads(result.stdout)
        
        # Should include the specified demo chart even without --include-demo
        chart_names = [chart['chart'] for chart in output_data['matrix']['include']]
        assert 'helm-demo-hello-fastapi' in chart_names
        assert len(chart_names) == 1

    def test_plan_helm_release_demo_domain_not_affected(self, mock_list_all_helm_charts):
        """Test that specifying 'demo' domain is not affected by include_demo flag."""
        runner = CliRunner()
        result = runner.invoke(typer_app, [
            'plan-helm-release',
            '--charts', 'demo',
            '--version', 'v1.0.0',
            '--format', 'json'
            # No --include-demo flag
        ])
        
        assert result.exit_code == 0
        
        # Parse the JSON output
        import json
        output_data = json.loads(result.stdout)
        
        # Should include all demo charts when domain is explicitly specified
        chart_names = [chart['chart'] for chart in output_data['matrix']['include']]
        assert 'helm-demo-hello-fastapi' in chart_names
        assert 'helm-demo-hello-worker' in chart_names
        assert 'helm-demo-demo-all-types' in chart_names
        assert len(chart_names) == 3  # All 3 demo charts
