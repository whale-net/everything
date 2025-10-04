"""
Unit tests for the images portion of the release helper.

This module provides comprehensive unit tests for the images.py module,
covering all image-related functions:
- format_registry_tags(): Registry tag formatting
- build_image(): Image building and loading
- push_image_with_tags(): Image pushing with multiple tags

The tests use mocking to avoid actual Bazel and Docker operations,
making them fast and reliable for CI/CD environments.
"""

import os
import pytest
from unittest.mock import Mock, patch, MagicMock

from tools.release_helper.images import format_registry_tags, build_image, push_image_with_tags





@pytest.fixture
def sample_image_targets():
    """Fixture providing sample image targets for testing."""
    return {
        "base": "//demo/hello_python:hello_python_image",
        "amd64": "//demo/hello_python:hello_python_image_amd64",
        "arm64": "//demo/hello_python:hello_python_image_arm64"
    }


@pytest.fixture
def mock_get_app_metadata(sample_metadata):
    """Mock get_app_metadata to return sample metadata."""
    with patch('tools.release_helper.images.get_app_metadata') as mock:
        mock.return_value = sample_metadata
        yield mock


@pytest.fixture
def mock_get_image_targets(sample_image_targets):
    """Mock get_image_targets to return sample image targets."""
    with patch('tools.release_helper.images.get_image_targets') as mock:
        mock.return_value = sample_image_targets
        yield mock


@pytest.fixture
def mock_run_bazel():
    """Mock run_bazel function."""
    with patch('tools.release_helper.images.run_bazel') as mock:
        yield mock


class TestFormatRegistryTags:
    """Test cases for format_registry_tags function."""

    def test_format_registry_tags_ghcr_default(self, clean_environ):
        """Test formatting registry tags for GHCR without repository owner."""
        result = format_registry_tags("demo", "hello_python", "v1.0.0")
        
        # Test essential structure rather than exact strings
        assert "ghcr.io" in result["latest"]
        assert "demo-hello_python" in result["latest"]
        assert "v1.0.0" in result["version"]

    def test_format_registry_tags_ghcr_with_owner(self, github_owner_env):
        """Test formatting registry tags for GHCR with repository owner."""
        result = format_registry_tags("demo", "hello_python", "v1.0.0")
        
        # Test that owner is included properly
        assert "testowner" in result["latest"]
        assert "ghcr.io" in result["latest"]

    def test_format_registry_tags_ghcr_with_commit_sha(self, github_owner_env):
        """Test formatting registry tags for GHCR with commit SHA (safety guard)."""
        result = format_registry_tags("demo", "hello_python", "v1.0.0", commit_sha="abc123")
        
        # Test that commit tag is added when provided
        assert "commit" in result
        assert "abc123" in result["commit"]

    def test_format_registry_tags_custom_registry(self):
        """Test formatting registry tags for custom registry (safety guard)."""
        result = format_registry_tags("demo", "hello_python", "v1.0.0", registry="docker.io")
        
        # Test that custom registry is used properly
        assert "docker.io" in result["latest"]
        assert "demo-hello_python" in result["latest"]


class TestBuildImage:
    """Test cases for build_image function."""

    def test_build_image_default_platform(self, mock_get_image_targets, mock_get_app_metadata, mock_run_bazel):
        """Test building image with default platform (uses base target)."""
        bazel_target = "//demo/hello_python:hello_python_metadata"
        
        result = build_image(bazel_target)
        
        # Verify the correct sequence of bazel commands
        expected_calls = [
            [["build", "//demo/hello_python:hello_python_image"]],
            [["run", "//demo/hello_python:hello_python_image_load"]]
        ]
        actual_calls = [call[0] for call in mock_run_bazel.call_args_list]
        assert actual_calls == expected_calls
        
        # Verify return value
        assert result == "demo-hello_python:latest"

    def test_build_image_amd64_platform(self, mock_get_image_targets, mock_get_app_metadata, mock_run_bazel):
        """Test building image for amd64 platform."""
        bazel_target = "//demo/hello_python:hello_python_metadata"
        
        result = build_image(bazel_target, platform="amd64")
        
        # Verify the correct sequence of bazel commands for amd64
        expected_calls = [
            [["build", "//demo/hello_python:hello_python_image_amd64"]],
            [["run", "//demo/hello_python:hello_python_image_amd64_load"]]
        ]
        actual_calls = [call[0] for call in mock_run_bazel.call_args_list]
        assert actual_calls == expected_calls
        
        assert result == "demo-hello_python:latest"

    def test_build_image_arm64_platform(self, mock_get_image_targets, mock_get_app_metadata, mock_run_bazel):
        """Test building image for arm64 platform."""
        bazel_target = "//demo/hello_python:hello_python_metadata"
        
        result = build_image(bazel_target, platform="arm64")
        
        # Verify the correct sequence of bazel commands for arm64
        expected_calls = [
            [["build", "//demo/hello_python:hello_python_image_arm64"]],
            [["run", "//demo/hello_python:hello_python_image_arm64_load"]]
        ]
        actual_calls = [call[0] for call in mock_run_bazel.call_args_list]
        assert actual_calls == expected_calls
        
        assert result == "demo-hello_python:latest"

    def test_build_image_get_metadata_called(self, mock_get_image_targets, mock_get_app_metadata, mock_run_bazel):
        """Test that build_image calls get_app_metadata to get domain and name."""
        bazel_target = "//demo/hello_python:hello_python_metadata"
        
        build_image(bazel_target)
        
        # Verify get_app_metadata was called with correct target
        mock_get_app_metadata.assert_called_once_with(bazel_target)

    def test_build_image_get_image_targets_called(self, mock_get_image_targets, mock_get_app_metadata, mock_run_bazel):
        """Test that build_image calls get_image_targets to get image targets."""
        bazel_target = "//demo/hello_python:hello_python_metadata"
        
        build_image(bazel_target)
        
        # Verify get_image_targets was called with correct target
        mock_get_image_targets.assert_called_once_with(bazel_target)

    def test_build_image_custom_domain_and_name(self, mock_get_image_targets, mock_run_bazel):
        """Test building image with custom domain and app name."""
        custom_metadata = {
            "name": "custom_app",
            "domain": "custom_domain",
            "registry": "ghcr.io",
            "version": "latest"
        }
        
        with patch('tools.release_helper.images.get_app_metadata', return_value=custom_metadata):
            result = build_image("//custom/app:metadata")
        
        assert result == "custom_domain-custom_app:latest"

    @patch('builtins.print')  # Mock print to avoid output during test
    def test_build_image_with_print_output(self, mock_print, mock_get_image_targets, mock_get_app_metadata, mock_run_bazel):
        """Test that build_image prints status information."""
        bazel_target = "//demo/hello_python:hello_python_metadata"
        
        build_image(bazel_target, platform="amd64")
        
        # Verify print was called with build status
        mock_print.assert_called_once()
        print_call = mock_print.call_args[0][0]
        assert "Building and loading" in print_call
        assert "optimized oci_load" in print_call


class TestPushImageWithTags:
    """Test cases for push_image_with_tags function."""

    def test_push_image_with_tags_default_platform(self, mock_get_image_targets, mock_get_app_metadata, mock_run_bazel):
        """Test pushing image with multiple tags (default platform)."""
        bazel_target = "//demo/hello_python:hello_python_metadata"
        tags = [
            "ghcr.io/owner/demo-hello_python:latest",
            "ghcr.io/owner/demo-hello_python:v1.0.0"
        ]
        
        push_image_with_tags(bazel_target, tags)
        
        # Verify push commands were called for each tag
        expected_calls = [
            [["run", "//demo/hello_python:hello_python_image", "--", 
              "--tag", "ghcr.io/owner/demo-hello_python:latest"]],
            [["run", "//demo/hello_python:hello_python_image", "--", 
              "--tag", "ghcr.io/owner/demo-hello_python:v1.0.0"]]
        ]
        actual_calls = [call[0] for call in mock_run_bazel.call_args_list]
        assert actual_calls == expected_calls

    def test_push_image_with_tags_amd64_platform(self, mock_get_image_targets, mock_get_app_metadata, mock_run_bazel):
        """Test pushing image with tags for amd64 platform."""
        bazel_target = "//demo/hello_python:hello_python_metadata"
        tags = ["ghcr.io/owner/demo-hello_python:v1.0.0"]
        
        push_image_with_tags(bazel_target, tags, platform="amd64")
        
        # Verify amd64-specific target was used
        expected_calls = [
            [["run", "//demo/hello_python:hello_python_image_amd64", "--", 
              "--tag", "ghcr.io/owner/demo-hello_python:v1.0.0"]]
        ]
        actual_calls = [call[0] for call in mock_run_bazel.call_args_list]
        assert actual_calls == expected_calls

    def test_push_image_with_tags_arm64_platform(self, mock_get_image_targets, mock_get_app_metadata, mock_run_bazel):
        """Test pushing image with tags for arm64 platform."""
        bazel_target = "//demo/hello_python:hello_python_metadata"
        tags = ["ghcr.io/owner/demo-hello_python:v1.0.0"]
        
        push_image_with_tags(bazel_target, tags, platform="arm64")
        
        # Verify arm64-specific target was used
        expected_calls = [
            [["run", "//demo/hello_python:hello_python_image_arm64", "--", 
              "--tag", "ghcr.io/owner/demo-hello_python:v1.0.0"]]
        ]
        actual_calls = [call[0] for call in mock_run_bazel.call_args_list]
        assert actual_calls == expected_calls

    def test_push_image_with_tags_empty_tags_list(self, mock_get_image_targets, mock_get_app_metadata, mock_run_bazel):
        """Test pushing image with empty tags list."""
        bazel_target = "//demo/hello_python:hello_python_metadata"
        tags = []
        
        push_image_with_tags(bazel_target, tags)
        
        # Verify no bazel commands were called
        mock_run_bazel.assert_not_called()

    def test_push_image_with_tags_single_tag(self, mock_get_image_targets, mock_get_app_metadata, mock_run_bazel):
        """Test pushing image with single tag."""
        bazel_target = "//demo/hello_python:hello_python_metadata"
        tags = ["ghcr.io/owner/demo-hello_python:latest"]
        
        push_image_with_tags(bazel_target, tags)
        
        # Verify single push command was called
        expected_calls = [
            [["run", "//demo/hello_python:hello_python_image", "--", 
              "--tag", "ghcr.io/owner/demo-hello_python:latest"]]
        ]
        actual_calls = [call[0] for call in mock_run_bazel.call_args_list]
        assert actual_calls == expected_calls

    def test_push_image_with_tags_multiple_tags(self, mock_get_image_targets, mock_get_app_metadata, mock_run_bazel):
        """Test pushing image with multiple tags."""
        bazel_target = "//demo/hello_python:hello_python_metadata"
        tags = [
            "ghcr.io/owner/demo-hello_python:latest",
            "ghcr.io/owner/demo-hello_python:v1.0.0",
            "ghcr.io/owner/demo-hello_python:abc123"
        ]
        
        push_image_with_tags(bazel_target, tags)
        
        # Verify all tags were pushed
        assert mock_run_bazel.call_count == 3
        
        # Check that each tag was included in the calls
        actual_calls = [call[0] for call in mock_run_bazel.call_args_list]
        for i, tag in enumerate(tags):
            expected_call = ["run", "//demo/hello_python:hello_python_image", "--", "--tag", tag]
            assert actual_calls[i] == expected_call

    def test_push_image_with_tags_exception_handling(self, mock_get_image_targets, mock_get_app_metadata, mock_run_bazel):
        """Test exception handling during image push."""
        bazel_target = "//demo/hello_python:hello_python_metadata"
        tags = ["ghcr.io/owner/demo-hello_python:v1.0.0"]
        
        # Mock run_bazel to raise an exception
        mock_run_bazel.side_effect = Exception("Push failed")
        
        # The function should re-raise the exception
        with pytest.raises(Exception, match="Push failed"):
            push_image_with_tags(bazel_target, tags)