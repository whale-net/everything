"""
Unit tests for git operations in the release helper.
"""

import subprocess
from unittest.mock import MagicMock, patch
import pytest

from tools.release_helper.git import (
    format_git_tag,
    format_helm_chart_tag,
    get_helm_chart_tags,
    parse_version_from_helm_chart_tag,
    create_git_tag,
    push_git_tag,
    get_previous_tag,
)


class TestFormatGitTag:
    """Test the format_git_tag function."""

    def test_format_git_tag_basic(self):
        """Test basic git tag formatting."""
        result = format_git_tag("api", "user-service", "1.0.0")
        assert result == "api-user-service-1.0.0"

    def test_format_git_tag_with_hyphens(self):
        """Test git tag formatting with hyphens in components."""
        result = format_git_tag("data-processing", "ml-service", "2.1.0-beta")
        assert result == "data-processing-ml-service-2.1.0-beta"

    def test_format_git_tag_empty_strings(self):
        """Test git tag formatting with empty strings."""
        result = format_git_tag("", "", "")
        assert result == "--"


class TestCreateGitTag:
    """Test the create_git_tag function."""

    @patch('tools.release_helper.git.subprocess.run')
    @patch('builtins.print')
    def test_create_git_tag_simple(self, mock_print, mock_run):
        """Test creating a simple git tag."""
        mock_run.return_value = None
        
        create_git_tag("v1.0.0")
        
        mock_print.assert_called_once_with("Creating Git tag: v1.0.0")
        mock_run.assert_called_once_with(["git", "tag", "v1.0.0"], check=True)

    @patch('tools.release_helper.git.subprocess.run')
    @patch('builtins.print')
    def test_create_git_tag_with_message(self, mock_print, mock_run):
        """Test creating a git tag with a message."""
        mock_run.return_value = None
        
        create_git_tag("v1.0.0", message="Release version 1.0.0")
        
        mock_print.assert_called_once_with("Creating Git tag: v1.0.0")
        mock_run.assert_called_once_with(
            ["git", "tag", "-a", "v1.0.0", "-m", "Release version 1.0.0"],
            check=True
        )

    @patch('tools.release_helper.git.subprocess.run')
    @patch('builtins.print')
    def test_create_git_tag_with_commit_sha(self, mock_print, mock_run):
        """Test creating a git tag on a specific commit."""
        mock_run.return_value = None
        
        create_git_tag("v1.0.0", commit_sha="abc123")
        
        mock_print.assert_called_once_with("Creating Git tag: v1.0.0")
        mock_run.assert_called_once_with(["git", "tag", "v1.0.0", "abc123"], check=True)

    @patch('tools.release_helper.git.subprocess.run')
    @patch('builtins.print')
    def test_create_git_tag_with_message_and_commit(self, mock_print, mock_run):
        """Test creating a git tag with both message and commit SHA."""
        mock_run.return_value = None
        
        create_git_tag("v1.0.0", commit_sha="abc123", message="Release version 1.0.0")
        
        mock_print.assert_called_once_with("Creating Git tag: v1.0.0")
        mock_run.assert_called_once_with(
            ["git", "tag", "-a", "v1.0.0", "-m", "Release version 1.0.0", "abc123"],
            check=True
        )

    @patch('tools.release_helper.git.subprocess.run')
    @patch('builtins.print')
    def test_create_git_tag_subprocess_error(self, mock_print, mock_run):
        """Test handling subprocess error when creating git tag."""
        mock_run.side_effect = subprocess.CalledProcessError(1, "git tag")
        
        with pytest.raises(subprocess.CalledProcessError):
            create_git_tag("v1.0.0")


class TestPushGitTag:
    """Test the push_git_tag function."""

    @patch('tools.release_helper.git.subprocess.run')
    @patch('builtins.print')
    def test_push_git_tag_success(self, mock_print, mock_run):
        """Test successfully pushing a git tag."""
        mock_run.return_value = None
        
        push_git_tag("v1.0.0")
        
        mock_print.assert_called_once_with("Pushing Git tag: v1.0.0")
        mock_run.assert_called_once_with(["git", "push", "origin", "v1.0.0"], check=True)

    @patch('tools.release_helper.git.subprocess.run')
    @patch('builtins.print')
    def test_push_git_tag_subprocess_error(self, mock_print, mock_run):
        """Test handling subprocess error when pushing git tag."""
        mock_run.side_effect = subprocess.CalledProcessError(1, "git push")
        
        with pytest.raises(subprocess.CalledProcessError):
            push_git_tag("v1.0.0")


class TestGetPreviousTag:
    """Test the get_previous_tag function."""

    @patch('tools.release_helper.git.subprocess.run')
    def test_get_previous_tag_success(self, mock_run):
        """Test successfully getting the previous tag."""
        mock_result = MagicMock()
        mock_result.stdout = "v1.0.0\n"
        mock_run.return_value = mock_result
        
        result = get_previous_tag()
        
        assert result == "v1.0.0"
        mock_run.assert_called_once_with(
            ["git", "describe", "--tags", "--abbrev=0", "HEAD^"],
            capture_output=True,
            text=True,
            check=True
        )

    @patch('tools.release_helper.git.subprocess.run')
    def test_get_previous_tag_no_tags(self, mock_run):
        """Test getting previous tag when no tags exist."""
        mock_run.side_effect = subprocess.CalledProcessError(1, "git describe")
        
        result = get_previous_tag()
        
        assert result is None

    @patch('tools.release_helper.git.subprocess.run')
    def test_get_previous_tag_strips_whitespace(self, mock_run):
        """Test that get_previous_tag strips whitespace from output."""
        mock_result = MagicMock()
        mock_result.stdout = "  v2.1.3  \n\t"
        mock_run.return_value = mock_result
        
        result = get_previous_tag()
        
        assert result == "v2.1.3"


class TestFormatHelmChartTag:
    """Test the format_helm_chart_tag function."""

    def test_format_helm_chart_tag_with_helm_prefix(self):
        """Test formatting helm chart tag with helm- prefix."""
        result = format_helm_chart_tag("helm-demo-hello-fastapi", "v1.0.0")
        assert result == "helm-demo-hello-fastapi.v1.0.0"

    def test_format_helm_chart_tag_without_helm_prefix(self):
        """Test formatting helm chart tag without helm- prefix."""
        result = format_helm_chart_tag("demo-hello-fastapi", "v1.0.0")
        assert result == "demo-hello-fastapi.v1.0.0"

    def test_format_helm_chart_tag_with_multiple_hyphens(self):
        """Test formatting helm chart tag with multiple hyphens."""
        result = format_helm_chart_tag("helm-manman-host-services", "v2.1.3")
        assert result == "helm-manman-host-services.v2.1.3"


class TestGetHelmChartTags:
    """Test the get_helm_chart_tags function."""

    @patch('tools.release_helper.git.get_all_tags')
    def test_get_helm_chart_tags_with_helm_prefix(self, mock_get_all_tags):
        """Test getting helm chart tags with helm- prefix in chart name."""
        mock_get_all_tags.return_value = [
            "helm-demo-hello-fastapi.v1.2.0",
            "helm-demo-hello-fastapi.v1.1.0",
            "helm-demo-hello-fastapi.v1.0.0",
            "api-user-service.v2.0.0",
        ]
        
        result = get_helm_chart_tags("helm-demo-hello-fastapi")
        
        assert result == [
            "helm-demo-hello-fastapi.v1.2.0",
            "helm-demo-hello-fastapi.v1.1.0",
            "helm-demo-hello-fastapi.v1.0.0",
        ]

    @patch('tools.release_helper.git.get_all_tags')
    def test_get_helm_chart_tags_no_matches(self, mock_get_all_tags):
        """Test getting helm chart tags when no matching tags exist."""
        mock_get_all_tags.return_value = [
            "api-user-service.v2.0.0",
            "demo-app.v1.0.0",
        ]
        
        result = get_helm_chart_tags("helm-demo-hello-fastapi")
        
        assert result == []


class TestParseVersionFromHelmChartTag:
    """Test the parse_version_from_helm_chart_tag function."""

    def test_parse_version_from_helm_chart_tag_success(self):
        """Test successfully parsing version from helm chart tag."""
        result = parse_version_from_helm_chart_tag(
            "helm-demo-hello-fastapi.v1.2.3",
            "helm-demo-hello-fastapi"
        )
        assert result == "v1.2.3"

    def test_parse_version_from_helm_chart_tag_with_prerelease(self):
        """Test parsing version with prerelease suffix."""
        result = parse_version_from_helm_chart_tag(
            "helm-demo-hello-fastapi.v1.2.3-beta.1",
            "helm-demo-hello-fastapi"
        )
        assert result == "v1.2.3-beta.1"

    def test_parse_version_from_helm_chart_tag_wrong_chart(self):
        """Test parsing version from wrong chart tag."""
        result = parse_version_from_helm_chart_tag(
            "helm-demo-hello-fastapi.v1.2.3",
            "helm-demo-other-app"
        )
        assert result is None

    def test_parse_version_from_helm_chart_tag_invalid_version(self):
        """Test parsing invalid version format."""
        result = parse_version_from_helm_chart_tag(
            "helm-demo-hello-fastapi.invalid",
            "helm-demo-hello-fastapi"
        )
        assert result is None