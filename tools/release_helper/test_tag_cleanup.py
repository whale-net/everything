"""
Unit tests for tag cleanup functionality.
"""

from datetime import datetime, timedelta
from unittest.mock import MagicMock, patch
import pytest

from tools.release_helper.git import (
    get_tag_creation_date,
    delete_local_tag,
    identify_tags_to_prune,
)


class TestGetTagCreationDate:
    """Test the get_tag_creation_date function."""

    @patch('tools.release_helper.git.subprocess.run')
    def test_get_tag_creation_date_success(self, mock_run):
        """Test successfully getting tag creation date."""
        mock_run.return_value = MagicMock(
            stdout="2024-01-15T10:30:00+00:00\n",
            returncode=0
        )
        
        result = get_tag_creation_date("demo-hello_python.v1.0.0")
        
        assert result is not None
        assert result.year == 2024
        assert result.month == 1
        assert result.day == 15
        mock_run.assert_called_once()

    @patch('tools.release_helper.git.subprocess.run')
    def test_get_tag_creation_date_tag_not_found(self, mock_run):
        """Test handling non-existent tag."""
        import subprocess
        mock_run.side_effect = subprocess.CalledProcessError(1, "git log")
        
        result = get_tag_creation_date("nonexistent-tag")
        
        assert result is None


class TestDeleteLocalTag:
    """Test the delete_local_tag function."""

    @patch('tools.release_helper.git.subprocess.run')
    @patch('builtins.print')
    def test_delete_local_tag_success(self, mock_print, mock_run):
        """Test successfully deleting a local tag."""
        mock_run.return_value = MagicMock(returncode=0)
        
        result = delete_local_tag("demo-hello_python.v1.0.0")
        
        assert result is True
        mock_run.assert_called_once_with(
            ["git", "tag", "-d", "demo-hello_python.v1.0.0"],
            check=True,
            capture_output=True
        )

    @patch('tools.release_helper.git.subprocess.run')
    @patch('builtins.print')
    def test_delete_local_tag_failure(self, mock_print, mock_run):
        """Test handling failure to delete tag."""
        import subprocess
        mock_run.side_effect = subprocess.CalledProcessError(1, "git tag -d")
        
        result = delete_local_tag("demo-hello_python.v1.0.0")
        
        assert result is False


class TestIdentifyTagsToPrune:
    """Test the identify_tags_to_prune function."""

    @patch('tools.release_helper.git.get_tag_creation_date')
    def test_identify_tags_basic(self, mock_get_date):
        """Test basic tag pruning logic."""
        now = datetime.now()
        old_date = now - timedelta(days=30)
        recent_date = now - timedelta(days=7)
        
        # Set up mock dates
        def mock_date_fn(tag):
            if "v1.0" in tag or "v1.1" in tag:
                return old_date
            else:
                return recent_date
        
        mock_get_date.side_effect = mock_date_fn
        
        tags = [
            "demo-app.v2.0.0",  # Recent, in last 2 minors (keep)
            "demo-app.v1.2.0",  # Recent, in last 2 minors (keep)
            "demo-app.v1.1.5",  # Old, latest patch of v1.1 (prune - not in last 2 minors)
            "demo-app.v1.1.4",  # Old, v1.1 older patch (prune)
            "demo-app.v1.1.3",  # Old, v1.1 older patch (prune)
            "demo-app.v1.0.2",  # Old, latest patch of v1.0 (prune - not in last 2 minors)
            "demo-app.v1.0.1",  # Old, v1.0 older patch (prune)
        ]
        
        result = identify_tags_to_prune(tags, min_age_days=14, keep_latest_minor_versions=2)
        
        # Should prune all v1.1 and v1.0 tags (not in last 2 minors)
        assert "demo-app.v1.1.5" in result
        assert "demo-app.v1.1.4" in result
        assert "demo-app.v1.1.3" in result
        assert "demo-app.v1.0.2" in result
        assert "demo-app.v1.0.1" in result
        
        # Should keep only the last 2 minors
        assert "demo-app.v2.0.0" not in result  # Recent, last 2 minors
        assert "demo-app.v1.2.0" not in result  # Recent, last 2 minors (also latest minor of major 1)

    @patch('tools.release_helper.git.get_tag_creation_date')
    def test_identify_tags_respects_age(self, mock_get_date):
        """Test that recent tags are not pruned."""
        now = datetime.now()
        recent_date = now - timedelta(days=7)
        
        mock_get_date.return_value = recent_date
        
        tags = [
            "demo-app.v1.1.0",
            "demo-app.v1.0.5",
            "demo-app.v1.0.4",
        ]
        
        result = identify_tags_to_prune(tags, min_age_days=14, keep_latest_minor_versions=2)
        
        # Nothing should be pruned because all tags are recent
        assert len(result) == 0

    @patch('tools.release_helper.git.get_tag_creation_date')
    def test_identify_tags_multiple_prefixes(self, mock_get_date):
        """Test handling multiple app prefixes."""
        now = datetime.now()
        old_date = now - timedelta(days=30)
        
        mock_get_date.return_value = old_date
        
        tags = [
            "demo-app1.v1.1.0",
            "demo-app1.v1.0.5",
            "demo-app1.v1.0.4",
            "demo-app2.v2.0.0",
            "demo-app2.v1.5.0",
            "demo-app2.v1.4.0",
        ]
        
        result = identify_tags_to_prune(tags, min_age_days=14, keep_latest_minor_versions=2)
        
        # Should keep last 2 minor versions for each app (latest patch only)
        # app1: keep v1.1.0 (in last 2, also latest minor of major 1) and v1.0.5 (in last 2), prune v1.0.4
        # app2: keep v2.0.0 (in last 2, also latest minor of major 2) and v1.5.0 (in last 2), prune v1.4.0
        assert "demo-app1.v1.0.4" in result
        assert "demo-app2.v1.4.0" in result
        
        assert "demo-app1.v1.1.0" not in result
        assert "demo-app1.v1.0.5" not in result
        assert "demo-app2.v2.0.0" not in result
        assert "demo-app2.v1.5.0" not in result

    @patch('tools.release_helper.git.get_tag_creation_date')
    def test_identify_tags_empty_list(self, mock_get_date):
        """Test handling empty tag list."""
        result = identify_tags_to_prune([], min_age_days=14, keep_latest_minor_versions=2)
        assert len(result) == 0

    @patch('tools.release_helper.git.get_tag_creation_date')
    def test_identify_tags_invalid_format(self, mock_get_date):
        """Test handling tags with invalid format."""
        now = datetime.now()
        old_date = now - timedelta(days=30)
        mock_get_date.return_value = old_date
        
        tags = [
            "invalid-tag",
            "also.invalid",
            "demo-app.v1.0.0",  # Valid
        ]
        
        result = identify_tags_to_prune(tags, min_age_days=14, keep_latest_minor_versions=2)
        
        # Should handle invalid tags gracefully
        assert "demo-app.v1.0.0" not in result  # Only tag, so it's kept

    @patch('tools.release_helper.git.get_tag_creation_date')
    def test_identify_tags_helm_charts(self, mock_get_date):
        """Test handling helm chart tags."""
        now = datetime.now()
        old_date = now - timedelta(days=30)
        
        mock_get_date.return_value = old_date
        
        tags = [
            "helm-demo-app.v1.2.0",
            "helm-demo-app.v1.1.5",
            "helm-demo-app.v1.1.4",
            "helm-demo-app.v1.0.0",
        ]
        
        result = identify_tags_to_prune(tags, min_age_days=14, keep_latest_minor_versions=2)
        
        # Should keep v1.2.0 (in last 2 minors, also latest minor of major 1) and v1.1.5 (in last 2 minors)
        # Should prune v1.1.4 (older patch of v1.1) and v1.0.0 (not in last 2 minors)
        assert "helm-demo-app.v1.1.4" in result
        assert "helm-demo-app.v1.0.0" in result
        assert "helm-demo-app.v1.2.0" not in result
        assert "helm-demo-app.v1.1.5" not in result

    @patch('tools.release_helper.git.get_tag_creation_date')
    def test_identify_tags_keeps_latest_minor_per_major(self, mock_get_date):
        """Test that latest minor version in each major is always kept."""
        now = datetime.now()
        old_date = now - timedelta(days=30)
        
        mock_get_date.return_value = old_date
        
        tags = [
            "demo-app.v3.0.0",  # Latest minor of major 3 (keep - in last 2)
            "demo-app.v2.5.0",  # Latest minor of major 2 (keep - in last 2)
            "demo-app.v2.4.0",  # Older minor of major 2 (prune)
            "demo-app.v1.2.5",  # Latest minor of major 1 (keep - latest in major 1)
            "demo-app.v1.2.4",  # Older patch of v1.2 (prune)
            "demo-app.v1.1.0",  # Older minor of major 1 (prune)
        ]
        
        result = identify_tags_to_prune(tags, min_age_days=14, keep_latest_minor_versions=2)
        
        # v3.0.0 and v2.5.0 are in the last 2 minors - keep
        # v1.2.5 is the latest minor in major 1 - keep (even though not in last 2)
        # Everything else should be pruned
        assert "demo-app.v2.4.0" in result
        assert "demo-app.v1.2.4" in result
        assert "demo-app.v1.1.0" in result
        
        assert "demo-app.v3.0.0" not in result
        assert "demo-app.v2.5.0" not in result
        assert "demo-app.v1.2.5" not in result  # Latest minor in major 1
