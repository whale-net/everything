"""
Tests for the Helm chart composer.
"""

import json
import tempfile
from pathlib import Path

import pytest
import yaml

from tools.release_helper.charts.composer import HelmComposer
from tools.release_helper.charts.types import AppType


class TestHelmComposer:
    """Test cases for HelmComposer."""
    
    def test_external_api_chart_generation(self, tmp_path):
        """Test generating a chart for an external-api app."""
        # Create test metadata
        metadata_file = tmp_path / "app_metadata.json"
        metadata = {
            "name": "test_api",
            "app_type": "external-api",
            "version": "v1.0.0",
            "registry": "ghcr.io",
            "repo_name": "test/test_api",
            "port": 8080,
            "replicas": 3
        }
        metadata_file.write_text(json.dumps(metadata))
        
        # Create minimal template directory
        template_dir = tmp_path / "templates"
        template_dir.mkdir()
        (template_dir / "deployment.yaml.tmpl").write_text("# deployment template")
        (template_dir / "service.yaml.tmpl").write_text("# service template")
        (template_dir / "ingress.yaml.tmpl").write_text("# ingress template")
        (template_dir / "pdb.yaml.tmpl").write_text("# pdb template")
        
        # Create composer and generate chart
        output_dir = tmp_path / "output"
        output_dir.mkdir()
        
        composer = HelmComposer(
            chart_name="test-chart",
            version="1.0.0",
            environment="production",
            namespace="prod",
            output_dir=str(output_dir),
            template_dir=str(template_dir)
        )
        
        composer.load_metadata([str(metadata_file)])
        composer.generate_chart()
        
        # Verify chart structure
        chart_dir = output_dir / "test-chart"
        assert chart_dir.exists()
        assert (chart_dir / "Chart.yaml").exists()
        assert (chart_dir / "values.yaml").exists()
        assert (chart_dir / "templates").exists()
        
        # Verify Chart.yaml
        with open(chart_dir / "Chart.yaml") as f:
            chart_data = yaml.safe_load(f)
        assert chart_data["name"] == "test-chart"
        assert chart_data["version"] == "1.0.0"
        
        # Verify values.yaml
        with open(chart_dir / "values.yaml") as f:
            values_data = yaml.safe_load(f)
        assert values_data["global"]["namespace"] == "prod"
        assert values_data["global"]["environment"] == "production"
        assert values_data["apps"]["test_api"]["type"] == "external-api"
        assert values_data["apps"]["test_api"]["port"] == 8080
        assert values_data["apps"]["test_api"]["replicas"] == 3
        assert values_data["ingress"]["enabled"] is True
    
    def test_worker_chart_generation(self, tmp_path):
        """Test generating a chart for a worker app."""
        # Create test metadata
        metadata_file = tmp_path / "worker_metadata.json"
        metadata = {
            "name": "background_worker",
            "app_type": "worker",
            "version": "v2.0.0",
            "registry": "ghcr.io",
            "repo_name": "test/worker"
        }
        metadata_file.write_text(json.dumps(metadata))
        
        # Create minimal template directory
        template_dir = tmp_path / "templates"
        template_dir.mkdir()
        (template_dir / "deployment.yaml.tmpl").write_text("# deployment template")
        (template_dir / "pdb.yaml.tmpl").write_text("# pdb template")
        
        # Create composer and generate chart
        output_dir = tmp_path / "output"
        output_dir.mkdir()
        
        composer = HelmComposer(
            chart_name="worker-chart",
            version="1.0.0",
            environment="staging",
            namespace="staging",
            output_dir=str(output_dir),
            template_dir=str(template_dir)
        )
        
        composer.load_metadata([str(metadata_file)])
        composer.generate_chart()
        
        # Verify values.yaml
        chart_dir = output_dir / "worker-chart"
        with open(chart_dir / "values.yaml") as f:
            values_data = yaml.safe_load(f)
        
        assert values_data["apps"]["background_worker"]["type"] == "worker"
        assert values_data["ingress"]["enabled"] is False  # Workers don't need ingress
        assert "healthCheck" not in values_data["apps"]["background_worker"]  # Workers don't have health checks


class TestAppTypes:
    """Test cases for AppType enum."""
    
    def test_external_api_requirements(self):
        """Test external-api app type requirements."""
        app_type = AppType.EXTERNAL_API
        assert app_type.requires_deployment() is True
        assert app_type.requires_service() is True
        assert app_type.requires_ingress() is True
        assert app_type.requires_job() is False
        assert app_type.requires_pdb() is True
        
        artifacts = app_type.template_artifacts()
        assert "deployment.yaml" in artifacts
        assert "service.yaml" in artifacts
        assert "ingress.yaml" in artifacts
        assert "pdb.yaml" in artifacts
        assert "job.yaml" not in artifacts
    
    def test_job_requirements(self):
        """Test job app type requirements."""
        app_type = AppType.JOB
        assert app_type.requires_deployment() is False
        assert app_type.requires_service() is False
        assert app_type.requires_ingress() is False
        assert app_type.requires_job() is True
        assert app_type.requires_pdb() is False
        
        artifacts = app_type.template_artifacts()
        assert "job.yaml" in artifacts
        assert "deployment.yaml" not in artifacts
        assert "service.yaml" not in artifacts
