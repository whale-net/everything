"""
Unit tests for Helm chart unpublishing functionality.
"""

import os
import tempfile
from pathlib import Path
import yaml
import pytest

from tools.release_helper.helm import unpublish_helm_chart_versions


class TestUnpublishHelmChartVersions:
    """Test cases for unpublishing Helm chart versions from index."""
    
    def test_unpublish_single_version(self):
        """Test unpublishing a single version from a chart."""
        # Create a temporary index.yaml
        index_data = {
            'apiVersion': 'v1',
            'entries': {
                'hello-fastapi': [
                    {'name': 'hello-fastapi', 'version': 'v1.0.0', 'urls': ['https://example.com/hello-fastapi-v1.0.0.tgz']},
                    {'name': 'hello-fastapi', 'version': 'v1.1.0', 'urls': ['https://example.com/hello-fastapi-v1.1.0.tgz']},
                    {'name': 'hello-fastapi', 'version': 'v1.2.0', 'urls': ['https://example.com/hello-fastapi-v1.2.0.tgz']},
                ]
            }
        }
        
        with tempfile.NamedTemporaryFile(mode='w', suffix='.yaml', delete=False) as f:
            yaml.safe_dump(index_data, f)
            index_path = Path(f.name)
        
        try:
            # Unpublish v1.1.0
            result = unpublish_helm_chart_versions(index_path, 'hello-fastapi', ['v1.1.0'])
            
            assert result is True
            
            # Verify the index was updated
            with open(index_path, 'r') as f:
                updated_index = yaml.safe_load(f)
            
            versions = [v['version'] for v in updated_index['entries']['hello-fastapi']]
            assert 'v1.0.0' in versions
            assert 'v1.1.0' not in versions
            assert 'v1.2.0' in versions
            assert len(versions) == 2
        finally:
            index_path.unlink()
    
    def test_unpublish_multiple_versions(self):
        """Test unpublishing multiple versions from a chart."""
        index_data = {
            'apiVersion': 'v1',
            'entries': {
                'demo-chart': [
                    {'name': 'demo-chart', 'version': 'v1.0.0', 'urls': ['https://example.com/demo-chart-v1.0.0.tgz']},
                    {'name': 'demo-chart', 'version': 'v1.1.0', 'urls': ['https://example.com/demo-chart-v1.1.0.tgz']},
                    {'name': 'demo-chart', 'version': 'v1.2.0', 'urls': ['https://example.com/demo-chart-v1.2.0.tgz']},
                    {'name': 'demo-chart', 'version': 'v2.0.0', 'urls': ['https://example.com/demo-chart-v2.0.0.tgz']},
                ]
            }
        }
        
        with tempfile.NamedTemporaryFile(mode='w', suffix='.yaml', delete=False) as f:
            yaml.safe_dump(index_data, f)
            index_path = Path(f.name)
        
        try:
            # Unpublish v1.0.0 and v1.2.0
            result = unpublish_helm_chart_versions(index_path, 'demo-chart', ['v1.0.0', 'v1.2.0'])
            
            assert result is True
            
            # Verify the index was updated
            with open(index_path, 'r') as f:
                updated_index = yaml.safe_load(f)
            
            versions = [v['version'] for v in updated_index['entries']['demo-chart']]
            assert 'v1.0.0' not in versions
            assert 'v1.1.0' in versions
            assert 'v1.2.0' not in versions
            assert 'v2.0.0' in versions
            assert len(versions) == 2
        finally:
            index_path.unlink()
    
    def test_unpublish_all_versions_removes_chart(self):
        """Test that unpublishing all versions removes the chart entry."""
        index_data = {
            'apiVersion': 'v1',
            'entries': {
                'chart-a': [
                    {'name': 'chart-a', 'version': 'v1.0.0', 'urls': ['https://example.com/chart-a-v1.0.0.tgz']},
                ],
                'chart-b': [
                    {'name': 'chart-b', 'version': 'v1.0.0', 'urls': ['https://example.com/chart-b-v1.0.0.tgz']},
                ]
            }
        }
        
        with tempfile.NamedTemporaryFile(mode='w', suffix='.yaml', delete=False) as f:
            yaml.safe_dump(index_data, f)
            index_path = Path(f.name)
        
        try:
            # Unpublish the only version of chart-a
            result = unpublish_helm_chart_versions(index_path, 'chart-a', ['v1.0.0'])
            
            assert result is True
            
            # Verify chart-a was removed from the index
            with open(index_path, 'r') as f:
                updated_index = yaml.safe_load(f)
            
            assert 'chart-a' not in updated_index['entries']
            assert 'chart-b' in updated_index['entries']
        finally:
            index_path.unlink()
    
    def test_unpublish_nonexistent_version(self):
        """Test unpublishing a version that doesn't exist."""
        index_data = {
            'apiVersion': 'v1',
            'entries': {
                'hello-fastapi': [
                    {'name': 'hello-fastapi', 'version': 'v1.0.0', 'urls': ['https://example.com/hello-fastapi-v1.0.0.tgz']},
                ]
            }
        }
        
        with tempfile.NamedTemporaryFile(mode='w', suffix='.yaml', delete=False) as f:
            yaml.safe_dump(index_data, f)
            index_path = Path(f.name)
        
        try:
            # Try to unpublish a version that doesn't exist
            result = unpublish_helm_chart_versions(index_path, 'hello-fastapi', ['v2.0.0'])
            
            # Should return False when no versions are removed
            assert result is False
            
            # Verify the index is unchanged
            with open(index_path, 'r') as f:
                updated_index = yaml.safe_load(f)
            
            versions = [v['version'] for v in updated_index['entries']['hello-fastapi']]
            assert versions == ['v1.0.0']
        finally:
            index_path.unlink()
    
    def test_unpublish_nonexistent_chart(self):
        """Test unpublishing from a chart that doesn't exist."""
        index_data = {
            'apiVersion': 'v1',
            'entries': {
                'hello-fastapi': [
                    {'name': 'hello-fastapi', 'version': 'v1.0.0', 'urls': ['https://example.com/hello-fastapi-v1.0.0.tgz']},
                ]
            }
        }
        
        with tempfile.NamedTemporaryFile(mode='w', suffix='.yaml', delete=False) as f:
            yaml.safe_dump(index_data, f)
            index_path = Path(f.name)
        
        try:
            # Try to unpublish from a chart that doesn't exist
            with pytest.raises(ValueError, match="Chart 'nonexistent' not found in index"):
                unpublish_helm_chart_versions(index_path, 'nonexistent', ['v1.0.0'])
        finally:
            index_path.unlink()
    
    def test_unpublish_missing_index_file(self):
        """Test unpublishing with a missing index file."""
        index_path = Path('/tmp/nonexistent-index.yaml')
        
        with pytest.raises(FileNotFoundError, match="Index file not found"):
            unpublish_helm_chart_versions(index_path, 'hello-fastapi', ['v1.0.0'])
    
    def test_unpublish_preserves_other_charts(self):
        """Test that unpublishing from one chart doesn't affect others."""
        index_data = {
            'apiVersion': 'v1',
            'entries': {
                'chart-a': [
                    {'name': 'chart-a', 'version': 'v1.0.0', 'urls': ['https://example.com/chart-a-v1.0.0.tgz']},
                    {'name': 'chart-a', 'version': 'v1.1.0', 'urls': ['https://example.com/chart-a-v1.1.0.tgz']},
                ],
                'chart-b': [
                    {'name': 'chart-b', 'version': 'v1.0.0', 'urls': ['https://example.com/chart-b-v1.0.0.tgz']},
                    {'name': 'chart-b', 'version': 'v2.0.0', 'urls': ['https://example.com/chart-b-v2.0.0.tgz']},
                ]
            }
        }
        
        with tempfile.NamedTemporaryFile(mode='w', suffix='.yaml', delete=False) as f:
            yaml.safe_dump(index_data, f)
            index_path = Path(f.name)
        
        try:
            # Unpublish from chart-a
            result = unpublish_helm_chart_versions(index_path, 'chart-a', ['v1.1.0'])
            
            assert result is True
            
            # Verify chart-a was updated but chart-b is unchanged
            with open(index_path, 'r') as f:
                updated_index = yaml.safe_load(f)
            
            chart_a_versions = [v['version'] for v in updated_index['entries']['chart-a']]
            chart_b_versions = [v['version'] for v in updated_index['entries']['chart-b']]
            
            assert chart_a_versions == ['v1.0.0']
            assert chart_b_versions == ['v1.0.0', 'v2.0.0']
        finally:
            index_path.unlink()
