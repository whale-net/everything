"""
Unit tests for git operations in the release helper.
"""

import subprocess
from unittest.mock import MagicMock, patch
import pytest

from tools.release_helper.git import (
    format_git_tag,
    create_git_tag,
    push_git_tag,
    get_previous_tag,
    get_all_tags,
    clear_tags_cache,
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


class TestGetAllTags:
    """Test the get_all_tags function and caching."""

    @patch('tools.release_helper.git.subprocess.run')
    def test_get_all_tags_success(self, mock_run):
        """Test successfully getting all tags."""
        # Clear cache before test
        clear_tags_cache()
        
        mock_result = MagicMock()
        mock_result.stdout = "v2.0.0\nv1.5.0\nv1.0.0\n"
        mock_run.return_value = mock_result
        
        result = get_all_tags()
        
        assert result == ["v2.0.0", "v1.5.0", "v1.0.0"]
        mock_run.assert_called_once_with(
            ["git", "tag", "--sort=-version:refname"],
            capture_output=True,
            text=True,
            check=True
        )

    @patch('tools.release_helper.git.subprocess.run')
    def test_get_all_tags_caching(self, mock_run):
        """Test that get_all_tags caches results."""
        # Clear cache before test
        clear_tags_cache()
        
        mock_result = MagicMock()
        mock_result.stdout = "v2.0.0\nv1.5.0\n"
        mock_run.return_value = mock_result
        
        # First call
        result1 = get_all_tags()
        assert result1 == ["v2.0.0", "v1.5.0"]
        
        # Second call should use cache
        result2 = get_all_tags()
        assert result2 == ["v2.0.0", "v1.5.0"]
        
        # subprocess.run should only be called once due to caching
        assert mock_run.call_count == 1

    @patch('tools.release_helper.git.subprocess.run')
    def test_get_all_tags_no_tags(self, mock_run):
        """Test getting all tags when no tags exist."""
        clear_tags_cache()
        
        mock_run.side_effect = subprocess.CalledProcessError(1, "git tag")
        
        result = get_all_tags()
        
        assert result == []

    @patch('tools.release_helper.git.subprocess.run')
    def test_clear_tags_cache_works(self, mock_run):
        """Test that clearing the cache causes get_all_tags to re-fetch."""
        clear_tags_cache()
        
        mock_result = MagicMock()
        mock_result.stdout = "v1.0.0\n"
        mock_run.return_value = mock_result
        
        # First call
        result1 = get_all_tags()
        assert result1 == ["v1.0.0"]
        assert mock_run.call_count == 1
        
        # Clear cache
        clear_tags_cache()
        
        # Update mock to return different tags
        mock_result.stdout = "v2.0.0\nv1.0.0\n"
        
        # Call again should re-fetch
        result2 = get_all_tags()
        assert result2 == ["v2.0.0", "v1.0.0"]
        assert mock_run.call_count == 2

    @patch('tools.release_helper.git.subprocess.run')
    def test_create_git_tag_clears_cache(self, mock_run):
        """Test that create_git_tag clears the tags cache."""
        # Clear cache and populate it
        clear_tags_cache()
        
        mock_result = MagicMock()
        mock_result.stdout = "v1.0.0\n"
        mock_run.return_value = mock_result
        
        # Populate cache
        get_all_tags()
        tag_call_count = mock_run.call_count
        
        # Create a new tag (this should clear cache)
        create_git_tag("v2.0.0")
        
        # Verify cache was cleared by checking that next call to get_all_tags re-fetches
        mock_result.stdout = "v2.0.0\nv1.0.0\n"
        result = get_all_tags()
        
        # Should have made additional git tag call due to cache clear
        assert mock_run.call_count > tag_call_count + 1

    @patch('tools.release_helper.git.subprocess.run')
    def test_push_git_tag_clears_cache(self, mock_run):
        """Test that push_git_tag clears the tags cache."""
        # Clear cache and populate it
        clear_tags_cache()
        
        mock_result = MagicMock()
        mock_result.stdout = "v1.0.0\n"
        mock_run.return_value = mock_result
        
        # Populate cache
        get_all_tags()
        tag_call_count = mock_run.call_count
        
        # Push a tag (this should clear cache)
        push_git_tag("v1.0.0")
        
        # Verify cache was cleared by checking that next call to get_all_tags re-fetches
        result = get_all_tags()
        
        # Should have made additional git tag call due to cache clear
        assert mock_run.call_count > tag_call_count + 1