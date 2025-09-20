"""
Unit tests for the summary portion of the release helper.

This module provides comprehensive unit tests for the summary.py module,
covering all summary generation functions:
- generate_release_summary(): Generate formatted release summaries for GitHub Actions

The tests use mocking to avoid external dependencies,
making them fast and reliable for CI/CD environments.
"""

import json
import pytest
from unittest.mock import Mock, patch

from tools.release_helper.summary import generate_release_summary


class TestGenerateReleaseSummary:
    """Test cases for generate_release_summary function."""

    def test_generate_release_summary_no_apps(self):
        """Test generating summary when no apps are detected for release."""
        matrix_json = json.dumps({"include": []})
        
        result = generate_release_summary(
            matrix_json=matrix_json,
            version="v1.0.0",
            event_type="pull_request"
        )
        
        assert "## 🚀 Release Summary" in result
        assert "🔍 **Result:** No apps detected for release" in result
        assert "v1.0.0" not in result  # Version shouldn't appear when no apps

    def test_generate_release_summary_single_app(self):
        """Test generating summary for single app release."""
        matrix_json = json.dumps({
            "include": [
                {
                    "app": "hello_python",
                    "bazel_target": "//demo/hello_python:hello_python_metadata",
                    "version": "v1.0.0"
                }
            ]
        })
        
        result = generate_release_summary(
            matrix_json=matrix_json,
            version="v1.0.0",
            event_type="workflow_dispatch"
        )
        
        assert "## 🚀 Release Summary" in result
        assert "✅ **Result:** Release completed" in result
        assert "📦 **Apps:** hello_python" in result
        assert "🏷️  **Version:** v1.0.0" in result

    def test_generate_release_summary_multiple_apps_same_version(self):
        """Test generating summary for multiple apps with same version."""
        matrix_json = json.dumps({
            "include": [
                {
                    "app": "hello_python",
                    "bazel_target": "//demo/hello_python:hello_python_metadata",
                    "version": "v1.0.0"
                },
                {
                    "app": "hello_go",
                    "bazel_target": "//demo/hello_go:hello_go_metadata", 
                    "version": "v1.0.0"
                }
            ]
        })
        
        result = generate_release_summary(
            matrix_json=matrix_json,
            version="v1.0.0",
            event_type="tag_push"
        )
        
        assert "📦 **Apps:** hello_python, hello_go" in result
        assert "🏷️  **Version:** v1.0.0" in result

    def test_generate_release_summary_multiple_apps_different_versions(self):
        """Test generating summary for multiple apps with different versions."""
        matrix_json = json.dumps({
            "include": [
                {
                    "app": "hello_python",
                    "bazel_target": "//demo/hello_python:hello_python_metadata",
                    "version": "v1.0.0"
                },
                {
                    "app": "hello_go",
                    "bazel_target": "//demo/hello_go:hello_go_metadata",
                    "version": "v1.1.0"
                },
                {
                    "app": "status_service",
                    "bazel_target": "//api/status_service:status_service_metadata",
                    "version": "v2.0.0"
                }
            ]
        })
        
        result = generate_release_summary(
            matrix_json=matrix_json,
            version="v1.0.0",  # Main version different from individual versions
            event_type="workflow_dispatch"
        )
        
        assert "📦 **Apps:** hello_python, hello_go, status_service" in result
        assert "🏷️  **Versions:**" in result
        assert "hello_python: v1.0.0" in result
        assert "hello_go: v1.1.0" in result
        assert "status_service: v2.0.0" in result

    def test_generate_release_summary_increment_mode_same_version(self):
        """Test generating summary for increment mode with same version for all apps."""
        matrix_json = json.dumps({
            "include": [
                {
                    "app": "hello_python",
                    "bazel_target": "//demo/hello_python:hello_python_metadata",
                    "version": "v1.1.0"
                },
                {
                    "app": "hello_go",
                    "bazel_target": "//demo/hello_go:hello_go_metadata",
                    "version": "v1.1.0"
                }
            ]
        })
        
        result = generate_release_summary(
            matrix_json=matrix_json,
            version=None,  # No main version in increment mode
            event_type="workflow_dispatch"
        )
        
        assert "📦 **Apps:** hello_python, hello_go" in result
        assert "🏷️  **Version:** v1.1.0" in result  # All apps have same version

    def test_generate_release_summary_invalid_json(self):
        """Test generating summary with invalid JSON matrix."""
        matrix_json = "invalid json"
        
        result = generate_release_summary(
            matrix_json=matrix_json,
            version="v1.0.0",
            event_type="pull_request"
        )
        
        assert "🔍 **Result:** No apps detected for release" in result

    def test_generate_release_summary_empty_json(self):
        """Test generating summary with empty JSON matrix."""
        matrix_json = ""
        
        result = generate_release_summary(
            matrix_json=matrix_json,
            version="v1.0.0",
            event_type="push"
        )
        
        assert "🔍 **Result:** No apps detected for release" in result

    def test_generate_release_summary_dry_run_flag(self):
        """Test generating summary with dry run flag."""
        matrix_json = json.dumps({
            "include": [
                {
                    "app": "hello_python",
                    "bazel_target": "//demo/hello_python:hello_python_metadata",
                    "version": "v1.0.0"
                }
            ]
        })
        
        result = generate_release_summary(
            matrix_json=matrix_json,
            version="v1.0.0",
            event_type="workflow_dispatch",
            dry_run=True
        )
        
        assert "## 🚀 Release Summary" in result
        assert "✅ **Result:** Release completed" in result
        assert "📦 **Apps:** hello_python" in result

    def test_generate_release_summary_with_repository_owner(self):
        """Test generating summary with repository owner."""
        matrix_json = json.dumps({
            "include": [
                {
                    "app": "hello_python",
                    "bazel_target": "//demo/hello_python:hello_python_metadata",
                    "version": "v1.0.0"
                }
            ]
        })
        
        result = generate_release_summary(
            matrix_json=matrix_json,
            version="v1.0.0",
            event_type="tag_push",
            repository_owner="whale-net"
        )
        
        assert "## 🚀 Release Summary" in result
        assert "✅ **Result:** Release completed" in result

    def test_generate_release_summary_latest_version(self):
        """Test generating summary with 'latest' version."""
        matrix_json = json.dumps({
            "include": [
                {
                    "app": "hello_python",
                    "bazel_target": "//demo/hello_python:hello_python_metadata",
                    "version": "latest"
                }
            ]
        })
        
        result = generate_release_summary(
            matrix_json=matrix_json,
            version="latest",
            event_type="push"
        )
        
        assert "🏷️  **Version:** latest" in result

    def test_generate_release_summary_mixed_versions_with_fallback(self):
        """Test generating summary with mixed versions and fallback to main version."""
        matrix_json = json.dumps({
            "include": [
                {
                    "app": "hello_python",
                    "bazel_target": "//demo/hello_python:hello_python_metadata",
                    "version": "v1.0.0"
                },
                {
                    "app": "hello_go",
                    "bazel_target": "//demo/hello_go:hello_go_metadata"
                    # No version specified - should fallback to main version
                }
            ]
        })
        
        result = generate_release_summary(
            matrix_json=matrix_json,
            version="v1.2.0",  # Main/fallback version
            event_type="workflow_dispatch"
        )
        
        assert "🏷️  **Versions:**" in result
        assert "hello_python: v1.0.0" in result
        assert "hello_go: v1.2.0" in result  # Should use fallback version

    def test_generate_release_summary_long_app_list(self):
        """Test generating summary with many apps."""
        apps = []
        for i in range(10):
            apps.append({
                "app": f"app_{i}",
                "bazel_target": f"//domain/app_{i}:app_{i}_metadata",
                "version": "v1.0.0"
            })
        
        matrix_json = json.dumps({"include": apps})
        
        result = generate_release_summary(
            matrix_json=matrix_json,
            version="v1.0.0", 
            event_type="workflow_dispatch"
        )
        
        assert "📦 **Apps:** app_0, app_1, app_2, app_3, app_4, app_5, app_6, app_7, app_8, app_9" in result
        assert "🏷️  **Version:** v1.0.0" in result

    def test_generate_release_summary_special_characters_in_app_names(self):
        """Test generating summary with special characters in app names."""
        matrix_json = json.dumps({
            "include": [
                {
                    "app": "hello_python_v2",
                    "bazel_target": "//demo/hello_python_v2:hello_python_v2_metadata",
                    "version": "v1.0.0"
                },
                {
                    "app": "status-service",
                    "bazel_target": "//api/status-service:status_service_metadata",
                    "version": "v1.0.0"
                }
            ]
        })
        
        result = generate_release_summary(
            matrix_json=matrix_json,
            version="v1.0.0",
            event_type="tag_push"
        )
        
        assert "📦 **Apps:** hello_python_v2, status-service" in result
        assert "🏷️  **Version:** v1.0.0" in result

    def test_generate_release_summary_format_consistency(self):
        """Test that summary format is consistent and well-formatted."""
        matrix_json = json.dumps({
            "include": [
                {
                    "app": "hello_python",
                    "bazel_target": "//demo/hello_python:hello_python_metadata",
                    "version": "v1.0.0"
                }
            ]
        })
        
        result = generate_release_summary(
            matrix_json=matrix_json,
            version="v1.0.0",
            event_type="workflow_dispatch"
        )
        
        lines = result.split('\n')
        
        # Check header format
        assert lines[0] == "## 🚀 Release Summary"
        assert lines[1] == ""  # Empty line after header
        
        # Check emoji usage consistency
        assert any(line.startswith("✅ **Result:**") for line in lines)
        assert any(line.startswith("📦 **Apps:**") for line in lines)
        assert any(line.startswith("🏷️  **Version:**") for line in lines)


if __name__ == "__main__":
    # Run tests if executed directly
    pytest.main([__file__, "-v"])