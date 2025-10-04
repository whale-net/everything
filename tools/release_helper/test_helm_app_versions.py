"""
Unit tests for Helm chart app version resolution functionality.
"""

import os
import tempfile
from pathlib import Path
import yaml
import pytest
from unittest.mock import patch, MagicMock

from tools.release_helper.helm import (
    resolve_app_versions_for_chart,
    package_chart_with_version,
)


class TestResolveAppVersionsForChart:
    """Test cases for resolving app versions in Helm charts."""
    
    @patch('tools.release_helper.helm.get_latest_app_version')
    @patch('tools.release_helper.helm.get_app_metadata')
    @patch('tools.release_helper.helm.find_app_bazel_target')
    def test_resolve_versions_with_released_versions(self, mock_find_target, mock_get_metadata, mock_get_version):
        """Test resolving app versions using git tags."""
        # Setup mocks
        mock_find_target.return_value = "//demo/hello_python:hello_python_metadata"
        mock_get_metadata.return_value = {
            'domain': 'demo',
            'name': 'hello_python',
        }
        mock_get_version.return_value = "v1.2.3"
        
        chart_metadata = {
            'name': 'helm-demo-test-chart',
            'apps': ['hello_python'],
        }
        
        # Test with use_released_versions=True
        result = resolve_app_versions_for_chart(chart_metadata, use_released_versions=True)
        
        assert result == {'hello_python': 'v1.2.3'}
        mock_get_version.assert_called_once_with('demo', 'hello_python')
    
    @patch('tools.release_helper.helm.get_latest_app_version')
    @patch('tools.release_helper.helm.get_app_metadata')
    @patch('tools.release_helper.helm.find_app_bazel_target')
    def test_resolve_versions_without_released_versions(self, mock_find_target, mock_get_metadata, mock_get_version):
        """Test that latest is used when use_released_versions=False."""
        chart_metadata = {
            'name': 'helm-demo-test-chart',
            'apps': ['hello_python'],
        }
        
        # Test with use_released_versions=False
        result = resolve_app_versions_for_chart(chart_metadata, use_released_versions=False)
        
        assert result == {'hello_python': 'latest'}
        mock_get_version.assert_not_called()
    
    @patch('tools.release_helper.helm.get_latest_app_version')
    @patch('tools.release_helper.helm.get_app_metadata')
    @patch('tools.release_helper.helm.find_app_bazel_target')
    def test_resolve_versions_fallback_to_latest_when_no_tag(self, mock_find_target, mock_get_metadata, mock_get_version):
        """Test fallback to latest when no git tag is found."""
        # Setup mocks
        mock_find_target.return_value = "//demo/hello_python:hello_python_metadata"
        mock_get_metadata.return_value = {
            'domain': 'demo',
            'name': 'hello_python',
        }
        mock_get_version.return_value = None  # No version found
        
        chart_metadata = {
            'name': 'helm-demo-test-chart',
            'apps': ['hello_python'],
        }
        
        # Test with use_released_versions=True but no version found
        result = resolve_app_versions_for_chart(chart_metadata, use_released_versions=True)
        
        assert result == {'hello_python': 'latest'}


class TestPackageChartWithAppVersions:
    """Test cases for packaging Helm charts with app version injection."""
    
    def test_package_chart_with_app_versions_updates_values_yaml(self):
        """Test that app_versions parameter updates imageTag in values.yaml."""
        # Create a temporary chart directory structure
        with tempfile.TemporaryDirectory() as tmpdir:
            chart_dir = Path(tmpdir) / "test-chart"
            chart_dir.mkdir()
            
            # Create Chart.yaml
            chart_yaml = chart_dir / "Chart.yaml"
            chart_data = {
                'apiVersion': 'v2',
                'name': 'test-chart',
                'version': '0.1.0',
                'description': 'Test chart',
            }
            with open(chart_yaml, 'w') as f:
                yaml.safe_dump(chart_data, f)
            
            # Create values.yaml with apps using "latest" imageTag
            values_yaml = chart_dir / "values.yaml"
            values_data = {
                'global': {
                    'namespace': 'test',
                    'environment': 'production',
                },
                'apps': {
                    'hello_python': {
                        'type': 'external-api',
                        'image': 'ghcr.io/whale-net/demo-hello_python',
                        'imageTag': 'latest',
                        'port': 8000,
                        'replicas': 2,
                    },
                    'hello_go': {
                        'type': 'worker',
                        'image': 'ghcr.io/whale-net/demo-hello_go',
                        'imageTag': 'latest',
                        'replicas': 1,
                    },
                },
            }
            with open(values_yaml, 'w') as f:
                yaml.safe_dump(values_data, f)
            
            # Create output directory
            output_dir = Path(tmpdir) / "output"
            output_dir.mkdir()
            
            # Package with app_versions
            app_versions = {
                'hello_python': 'v1.2.3',
                'hello_go': 'v2.0.0',
            }
            
            with patch('tools.release_helper.helm.subprocess.run') as mock_run:
                # Mock helm package success
                mock_run.return_value = MagicMock(returncode=0)
                
                # Create the expected output file
                packaged_file = output_dir / "test-chart-v1.0.0.tgz"
                packaged_file.touch()
                
                result = package_chart_with_version(
                    chart_dir=chart_dir,
                    chart_name='test-chart',
                    chart_version='v1.0.0',
                    output_dir=output_dir,
                    app_versions=app_versions
                )
                
                # Verify helm package was called
                assert mock_run.called
                
                # Verify the result path
                assert result == packaged_file
    
    def test_package_chart_without_app_versions_preserves_original(self):
        """Test that values.yaml is not modified when app_versions is None."""
        # Create a temporary chart directory structure
        with tempfile.TemporaryDirectory() as tmpdir:
            chart_dir = Path(tmpdir) / "test-chart"
            chart_dir.mkdir()
            
            # Create Chart.yaml
            chart_yaml = chart_dir / "Chart.yaml"
            chart_data = {
                'apiVersion': 'v2',
                'name': 'test-chart',
                'version': '0.1.0',
            }
            with open(chart_yaml, 'w') as f:
                yaml.safe_dump(chart_data, f)
            
            # Create values.yaml
            values_yaml = chart_dir / "values.yaml"
            original_values = {
                'apps': {
                    'hello_python': {
                        'imageTag': 'latest',
                    },
                },
            }
            with open(values_yaml, 'w') as f:
                yaml.safe_dump(original_values, f)
            
            # Create output directory
            output_dir = Path(tmpdir) / "output"
            output_dir.mkdir()
            
            # Package without app_versions
            with patch('tools.release_helper.helm.subprocess.run') as mock_run:
                mock_run.return_value = MagicMock(returncode=0)
                
                # Create the expected output file
                packaged_file = output_dir / "test-chart-v1.0.0.tgz"
                packaged_file.touch()
                
                result = package_chart_with_version(
                    chart_dir=chart_dir,
                    chart_name='test-chart',
                    chart_version='v1.0.0',
                    output_dir=output_dir,
                    app_versions=None
                )
                
                assert result == packaged_file
