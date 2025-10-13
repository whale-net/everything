"""
Unit tests for the release portion of the release helper.

This module provides comprehensive unit tests for the release.py module,
covering all release-related functions:
- find_app_bazel_target(): Finding bazel targets for apps
- plan_release(): Release planning logic for different event types
- tag_and_push_image(): Complete image release workflow

The tests use mocking to avoid actual Bazel operations and network calls,
making them fast and reliable for CI/CD environments.
"""

import subprocess
import pytest
from unittest.mock import Mock, patch, MagicMock
import sys

from tools.release_helper.release import find_app_bazel_target, plan_release, tag_and_push_image





@pytest.fixture
def mock_list_all_apps(sample_apps):
    """Mock list_all_apps to return sample data."""
    with patch('tools.release_helper.release.list_all_apps') as mock:
        mock.return_value = sample_apps
        yield mock


@pytest.fixture
def mock_get_app_metadata(sample_metadata):
    """Mock get_app_metadata to return sample metadata."""
    with patch('tools.release_helper.release.get_app_metadata') as mock:
        mock.return_value = sample_metadata
        yield mock


@pytest.fixture
def mock_validate_apps(sample_apps):
    """Mock validate_apps to return sample data."""
    with patch('tools.release_helper.release.validate_apps') as mock:
        mock.return_value = sample_apps[:2]  # Return first two apps by default
        yield mock


@pytest.fixture
def mock_detect_changed_apps():
    """Mock detect_changed_apps to return sample data."""
    with patch('tools.release_helper.release.detect_changed_apps') as mock:
        mock.return_value = [
            {
                "name": "hello_python",
                "domain": "demo",
                "bazel_target": "//demo/hello_python:hello_python_metadata"
            }
        ]
        yield mock


class TestFindAppBazelTarget:
    """Test cases for find_app_bazel_target function."""

    def test_find_app_bazel_target_success(self, sample_apps):
        """Test successfully finding a bazel target for an existing app."""
        with patch('tools.release_helper.validation.list_all_apps', return_value=sample_apps):
            result = find_app_bazel_target("hello_python")
            
            assert result == "//demo/hello_python:hello_python_metadata"

    def test_find_app_bazel_target_not_found(self, sample_apps):
        """Test error when app is not found."""
        with patch('tools.release_helper.validation.list_all_apps', return_value=sample_apps):
            with pytest.raises(ValueError, match="Invalid apps: nonexistent"):
                find_app_bazel_target("nonexistent")

    def test_find_app_bazel_target_empty_apps_list(self):
        """Test error when no apps are available."""
        with patch('tools.release_helper.validation.list_all_apps', return_value=[]):
            with pytest.raises(ValueError, match="Invalid apps: hello_python"):
                find_app_bazel_target("hello_python")

    def test_find_app_bazel_target_multiple_apps(self, sample_apps):
        """Test finding target among multiple apps."""
        with patch('tools.release_helper.validation.list_all_apps', return_value=sample_apps):
            result = find_app_bazel_target("status_service")
            
            assert result == "//api/status_service:status_service_metadata"

    def test_find_app_bazel_target_ambiguous_name(self, sample_apps_with_collision):
        """Test that ambiguous app names raise an error."""
        with patch('tools.release_helper.validation.list_all_apps', return_value=sample_apps_with_collision):
            # hello_python exists in both demo and api domains
            with pytest.raises(ValueError, match="ambiguous"):
                find_app_bazel_target("hello_python")

    def test_find_app_bazel_target_full_format(self, sample_apps_with_collision):
        """Test finding app by full domain-app format."""
        with patch('tools.release_helper.validation.list_all_apps', return_value=sample_apps_with_collision):
            # Use full format to disambiguate
            result = find_app_bazel_target("demo-hello_python")
            assert result == "//demo/hello_python:hello_python_metadata"
            
            result = find_app_bazel_target("api-hello_python")
            assert result == "//api/hello_python:hello_python_metadata"

    def test_find_app_bazel_target_path_format(self, sample_apps_with_collision):
        """Test finding app by path format (domain/name)."""
        with patch('tools.release_helper.validation.list_all_apps', return_value=sample_apps_with_collision):
            # Use path format to disambiguate
            result = find_app_bazel_target("demo/hello_python")
            assert result == "//demo/hello_python:hello_python_metadata"
            
            result = find_app_bazel_target("api/hello_python")
            assert result == "//api/hello_python:hello_python_metadata"


class TestPlanRelease:
    """Test cases for plan_release function."""

    @patch('tools.release_helper.release.validate_semantic_version')
    def test_plan_release_workflow_dispatch_all_apps(self, mock_validate_semantic, mock_list_all_apps, sample_apps):
        """Test planning release for workflow_dispatch with all apps (excludes demo domain by default)."""
        mock_validate_semantic.return_value = True
        
        result = plan_release(
            event_type="workflow_dispatch",
            requested_apps="all",
            version="v1.0.0"
        )
        
        assert result["event_type"] == "workflow_dispatch"
        assert result["version"] == "v1.0.0"
        # When using "all", demo domain apps are excluded by default
        # So we should only get non-demo apps (status_service in this case)
        assert len(result["matrix"]["include"]) == 1
        assert result["apps"] == ["status_service"]

    @patch('tools.release_helper.release.validate_semantic_version')
    def test_plan_release_workflow_dispatch_specific_apps(self, mock_validate_semantic, mock_validate_apps, sample_apps):
        """Test planning release for workflow_dispatch with specific apps."""
        mock_validate_semantic.return_value = True
        
        result = plan_release(
            event_type="workflow_dispatch",
            requested_apps="hello_python,hello_go",
            version="v1.0.0"
        )
        
        assert result["event_type"] == "workflow_dispatch"
        assert len(result["matrix"]["include"]) == 2
        mock_validate_apps.assert_called_once_with(["hello_python", "hello_go"])

    def test_plan_release_workflow_dispatch_no_apps_specified(self):
        """Test error when workflow_dispatch has no apps specified."""
        with pytest.raises(ValueError, match="Manual releases require apps to be specified"):
            plan_release(event_type="workflow_dispatch")

    @patch('tools.release_helper.release.validate_semantic_version')
    def test_plan_release_workflow_dispatch_specific_version_mode(self, mock_validate_semantic, mock_list_all_apps, sample_apps):
        """Test workflow_dispatch with specific version mode."""
        mock_validate_semantic.return_value = True
        
        result = plan_release(
            event_type="workflow_dispatch",
            requested_apps="all",
            version="v1.0.0",
            version_mode="specific"
        )
        
        assert result["version"] == "v1.0.0"
        assert all(app["version"] == "v1.0.0" for app in result["matrix"]["include"])

    def test_plan_release_workflow_dispatch_specific_mode_no_version(self):
        """Test error when specific mode has no version."""
        with pytest.raises(ValueError, match="Specific version mode requires version to be specified"):
            plan_release(
                event_type="workflow_dispatch",
                requested_apps="all",
                version_mode="specific"
            )

    @patch('tools.release_helper.release.auto_increment_version')
    def test_plan_release_workflow_dispatch_increment_minor(self, mock_auto_increment, mock_validate_apps, mock_get_app_metadata, sample_apps, mock_print):
        """Test workflow_dispatch with increment_minor mode."""
        mock_auto_increment.return_value = "v1.1.0"
        
        result = plan_release(
            event_type="workflow_dispatch",
            requested_apps="hello_python",
            version_mode="increment_minor"
        )
        
        assert result["version"] is None  # No global version in increment mode
        assert "v1.1.0" in result["versions"].values()
        mock_auto_increment.assert_called_with("demo", "hello_python", "minor")

    @patch('tools.release_helper.release.auto_increment_version')
    def test_plan_release_workflow_dispatch_increment_patch(self, mock_auto_increment, mock_validate_apps, mock_get_app_metadata, sample_apps, mock_print):
        """Test workflow_dispatch with increment_patch mode."""
        mock_auto_increment.return_value = "v1.0.1"
        
        result = plan_release(
            event_type="workflow_dispatch",
            requested_apps="hello_python",
            version_mode="increment_patch"
        )
        
        assert result["version"] is None  # No global version in increment mode
        assert "v1.0.1" in result["versions"].values()
        mock_auto_increment.assert_called_with("demo", "hello_python", "patch")

    def test_plan_release_increment_mode_with_version(self):
        """Test error when increment mode has version specified."""
        with pytest.raises(ValueError, match="Version should not be specified when using increment_minor mode"):
            plan_release(
                event_type="workflow_dispatch",
                requested_apps="all",
                version="v1.0.0",
                version_mode="increment_minor"
            )

    def test_plan_release_workflow_dispatch_legacy_no_version(self):
        """Test error when legacy workflow_dispatch has no version."""
        with pytest.raises(ValueError, match="Manual releases require version to be specified"):
            plan_release(
                event_type="workflow_dispatch",
                requested_apps="all"
            )

    @patch('tools.release_helper.release.validate_semantic_version')
    @patch('tools.release_helper.release.get_previous_tag')
    def test_plan_release_tag_push(self, mock_get_previous_tag, mock_validate_semantic, mock_detect_changed_apps, mock_print):
        """Test planning release for tag_push event."""
        mock_validate_semantic.return_value = True
        mock_get_previous_tag.return_value = "demo-hello_python.v0.9.0"
        
        result = plan_release(
            event_type="tag_push",
            version="v1.0.0"
        )
        
        assert result["event_type"] == "tag_push"
        assert result["version"] == "v1.0.0"
        mock_detect_changed_apps.assert_called_once_with("demo-hello_python.v0.9.0")

    def test_plan_release_tag_push_no_version(self):
        """Test error when tag_push has no version."""
        with pytest.raises(ValueError, match="Tag push releases require version to be specified"):
            plan_release(event_type="tag_push")

    @patch('tools.release_helper.release.validate_semantic_version')
    def test_plan_release_tag_push_with_base_commit(self, mock_validate_semantic, mock_detect_changed_apps):
        """Test tag_push with explicit base commit."""
        mock_validate_semantic.return_value = True
        
        result = plan_release(
            event_type="tag_push",
            version="v1.0.0",
            base_commit="abc123"
        )
        
        mock_detect_changed_apps.assert_called_once_with("abc123")

    def test_plan_release_pull_request_with_base_commit(self, mock_detect_changed_apps, mock_print):
        """Test planning release for pull_request event."""
        result = plan_release(
            event_type="pull_request",
            base_commit="main"
        )
        
        assert result["event_type"] == "pull_request"
        mock_detect_changed_apps.assert_called_once_with("main")

    def test_plan_release_push_fallback_mode(self, mock_list_all_apps, sample_apps, mock_print):
        """Test planning release for push event in fallback mode."""
        result = plan_release(event_type="push")
        
        assert result["event_type"] == "push"
        assert len(result["matrix"]["include"]) == len(sample_apps)

    def test_plan_release_fallback_event(self, mock_list_all_apps, sample_apps, mock_print):
        """Test planning release for fallback event type."""
        result = plan_release(event_type="fallback")
        
        assert result["event_type"] == "fallback"
        assert len(result["matrix"]["include"]) == len(sample_apps)

    def test_plan_release_unknown_event_type(self):
        """Test error for unknown event type."""
        with pytest.raises(ValueError, match="Unknown event type: unknown"):
            plan_release(event_type="unknown")

    def test_plan_release_invalid_version_format(self):
        """Test error for invalid version format."""
        with patch('tools.release_helper.release.validate_semantic_version', return_value=False):
            with pytest.raises(ValueError, match="does not follow semantic versioning format"):
                plan_release(
                    event_type="workflow_dispatch",
                    requested_apps="all",
                    version="1.0.0"  # Missing 'v' prefix
                )

    def test_plan_release_latest_version_allowed(self, mock_list_all_apps, sample_apps):
        """Test that 'latest' version is always allowed."""
        result = plan_release(
            event_type="workflow_dispatch", 
            requested_apps="all",
            version="latest"
        )
        
        assert result["version"] == "latest"

    def test_plan_release_empty_matrix_no_apps(self, mock_detect_changed_apps):
        """Test planning release when no apps are detected."""
        mock_detect_changed_apps.return_value = []
        
        result = plan_release(
            event_type="pull_request",
            base_commit="main"
        )
        
        assert result["matrix"] == {"include": []}
        assert result["apps"] == []


class TestTagAndPushImage:
    """Test cases for tag_and_push_image function."""

    @patch('tools.release_helper.release.validate_release_version')
    @patch('tools.release_helper.release.build_image')
    @patch('tools.release_helper.release.format_registry_tags')
    @patch('tools.release_helper.release.push_image_with_tags')
    @patch('builtins.print')
    def test_tag_and_push_image_success(self, mock_print, mock_push_image, mock_format_tags, 
                                      mock_build_image, mock_validate_version, 
                                      mock_get_app_metadata, sample_apps):
        """Test successful image tagging and pushing."""
        # Setup mocks
        with patch('tools.release_helper.release.find_app_bazel_target', return_value="//demo/hello_python:hello_python_metadata"):
            mock_format_tags.return_value = {
                "latest": "ghcr.io/demo-hello_python:latest",
                "version": "ghcr.io/demo-hello_python:v1.0.0"
            }
            
            tag_and_push_image("hello_python", "v1.0.0")
            
            # Verify function calls
            mock_validate_version.assert_called_once_with("//demo/hello_python:hello_python_metadata", "v1.0.0", False)
            mock_build_image.assert_called_once_with("//demo/hello_python:hello_python_metadata")
            mock_push_image.assert_called_once()

    @patch('tools.release_helper.release.validate_release_version')
    @patch('tools.release_helper.release.build_image')
    @patch('tools.release_helper.release.format_registry_tags')
    @patch('builtins.print')
    def test_tag_and_push_image_dry_run(self, mock_print, mock_format_tags, mock_build_image, 
                                       mock_validate_version, mock_get_app_metadata):
        """Test dry run mode."""
        with patch('tools.release_helper.release.find_app_bazel_target', return_value="//demo/hello_python:hello_python_metadata"):
            mock_format_tags.return_value = {
                "latest": "ghcr.io/demo-hello_python:latest",
                "version": "ghcr.io/demo-hello_python:v1.0.0"
            }
            
            tag_and_push_image("hello_python", "v1.0.0", dry_run=True)
            
            # Verify build was called but push was not
            mock_build_image.assert_called_once()
            
            # Verify dry run messages were printed
            print_calls = [call[0][0] for call in mock_print.call_args_list]
            assert any("DRY RUN: Would push" in call for call in print_calls)

    @patch('tools.release_helper.release.validate_release_version')
    @patch('tools.release_helper.release.build_image')
    @patch('tools.release_helper.release.format_registry_tags')
    @patch('tools.release_helper.release.format_git_tag')
    @patch('builtins.print')
    def test_tag_and_push_image_dry_run_with_git_tag(self, mock_print, mock_format_git_tag, 
                                                    mock_format_tags, mock_build_image, 
                                                    mock_validate_version, mock_get_app_metadata):
        """Test dry run mode with git tag creation."""
        with patch('tools.release_helper.release.find_app_bazel_target', return_value="//demo/hello_python:hello_python_metadata"):
            mock_format_tags.return_value = {"latest": "ghcr.io/demo-hello_python:latest"}
            mock_format_git_tag.return_value = "demo-hello_python.v1.0.0"
            
            tag_and_push_image("hello_python", "v1.0.0", dry_run=True, create_git_tag_flag=True)
            
            # Verify git tag dry run message was printed
            print_calls = [call[0][0] for call in mock_print.call_args_list]
            assert any("DRY RUN: Would create Git tag" in call for call in print_calls)

    @patch('tools.release_helper.release.validate_release_version')
    @patch('tools.release_helper.release.build_image') 
    @patch('tools.release_helper.release.format_registry_tags')
    @patch('tools.release_helper.release.push_image_with_tags')
    @patch('tools.release_helper.release.format_git_tag')
    @patch('tools.release_helper.release.create_git_tag')
    @patch('tools.release_helper.release.push_git_tag')
    @patch('builtins.print')
    def test_tag_and_push_image_with_git_tag(self, mock_print, mock_push_git_tag, mock_create_git_tag,
                                            mock_format_git_tag, mock_push_image, mock_format_tags,
                                            mock_build_image, mock_validate_version, mock_get_app_metadata):
        """Test image push with git tag creation."""
        with patch('tools.release_helper.release.find_app_bazel_target', return_value="//demo/hello_python:hello_python_metadata"):
            mock_format_tags.return_value = {"latest": "ghcr.io/demo-hello_python:latest"}
            mock_format_git_tag.return_value = "demo-hello_python.v1.0.0"
            
            tag_and_push_image("hello_python", "v1.0.0", commit_sha="abc123", create_git_tag_flag=True)
            
            # Verify git operations were called
            mock_create_git_tag.assert_called_once_with("demo-hello_python.v1.0.0", "abc123", "Release hello_python v1.0.0")
            mock_push_git_tag.assert_called_once_with("demo-hello_python.v1.0.0")

    @patch('tools.release_helper.release.validate_release_version')
    @patch('tools.release_helper.release.build_image')
    @patch('tools.release_helper.release.format_registry_tags')
    @patch('tools.release_helper.release.push_image_with_tags')
    @patch('builtins.print')
    def test_tag_and_push_image_push_failure(self, mock_print, mock_push_image, mock_format_tags,
                                            mock_build_image, mock_validate_version, mock_get_app_metadata):
        """Test handling of push failure."""
        with patch('tools.release_helper.release.find_app_bazel_target', return_value="//demo/hello_python:hello_python_metadata"):
            mock_format_tags.return_value = {"latest": "ghcr.io/demo-hello_python:latest"}
            mock_push_image.side_effect = Exception("Push failed")
            
            with pytest.raises(Exception, match="Push failed"):
                tag_and_push_image("hello_python", "v1.0.0")

    @patch('tools.release_helper.release.validate_release_version')
    @patch('tools.release_helper.release.build_image')
    @patch('tools.release_helper.release.format_registry_tags')
    @patch('tools.release_helper.release.push_image_with_tags')
    @patch('tools.release_helper.release.format_git_tag')
    @patch('tools.release_helper.release.create_git_tag')
    @patch('builtins.print')
    def test_tag_and_push_image_git_tag_failure(self, mock_print, mock_create_git_tag, mock_format_git_tag,
                                               mock_push_image, mock_format_tags, mock_build_image,
                                               mock_validate_version, mock_get_app_metadata):
        """Test handling of git tag creation failure."""
        with patch('tools.release_helper.release.find_app_bazel_target', return_value="//demo/hello_python:hello_python_metadata"):
            mock_format_tags.return_value = {"latest": "ghcr.io/demo-hello_python:latest"}
            mock_format_git_tag.return_value = "demo-hello_python.v1.0.0"
            mock_create_git_tag.side_effect = subprocess.CalledProcessError(1, "git tag")
            
            # Should not raise exception, just print warning
            tag_and_push_image("hello_python", "v1.0.0", create_git_tag_flag=True)
            
            # Verify warning was printed to stderr
            assert mock_print.call_count >= 1

    @patch('tools.release_helper.release.validate_release_version')
    @patch('tools.release_helper.release.build_image')
    @patch('tools.release_helper.release.format_registry_tags')
    @patch('tools.release_helper.release.push_image_with_tags')
    @patch('builtins.print')
    def test_tag_and_push_image_with_commit_sha(self, mock_print, mock_push_image, mock_format_tags,
                                               mock_build_image, mock_validate_version, mock_get_app_metadata):
        """Test image push with commit SHA."""
        with patch('tools.release_helper.release.find_app_bazel_target', return_value="//demo/hello_python:hello_python_metadata"):
            mock_format_tags.return_value = {
                "latest": "ghcr.io/demo-hello_python:latest",
                "version": "ghcr.io/demo-hello_python:v1.0.0",
                "commit": "ghcr.io/demo-hello_python:abc123"
            }
            
            tag_and_push_image("hello_python", "v1.0.0", commit_sha="abc123")
            
            # Verify format_registry_tags was called with commit SHA
            mock_format_tags.assert_called_once_with("demo", "hello_python", "v1.0.0", "ghcr.io", "abc123")

    @patch('tools.release_helper.release.validate_release_version')
    @patch('tools.release_helper.release.build_image')
    @patch('tools.release_helper.release.format_registry_tags')
    @patch('tools.release_helper.release.push_image_with_tags')
    @patch('builtins.print')
    def test_tag_and_push_image_allow_overwrite(self, mock_print, mock_push_image, mock_format_tags,
                                               mock_build_image, mock_validate_version, mock_get_app_metadata):
        """Test image push with allow_overwrite flag."""
        with patch('tools.release_helper.release.find_app_bazel_target', return_value="//demo/hello_python:hello_python_metadata"):
            mock_format_tags.return_value = {"latest": "ghcr.io/demo-hello_python:latest"}
            
            tag_and_push_image("hello_python", "v1.0.0", allow_overwrite=True)
            
            # Verify validate_release_version was called with allow_overwrite=True
            mock_validate_version.assert_called_once_with("//demo/hello_python:hello_python_metadata", "v1.0.0", True)