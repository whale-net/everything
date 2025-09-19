"""
Unit tests for git operations in the changes detection module.
"""

import subprocess
import sys
from unittest.mock import MagicMock, patch
import pytest

from tools.release_helper.changes import _get_changed_files


class TestGetChangedFiles:
    """Test the _get_changed_files function."""

    @patch('tools.release_helper.changes.subprocess.run')
    def test_get_changed_files_success(self, mock_run):
        """Test successfully getting changed files."""
        mock_result = MagicMock()
        mock_result.stdout = "file1.py\nfile2.go\ndir/file3.yaml\n"
        mock_run.return_value = mock_result
        
        result = _get_changed_files("main")
        
        assert result == ["file1.py", "file2.go", "dir/file3.yaml"]
        mock_run.assert_called_once_with(
            ["git", "diff", "--name-only", "main..HEAD"],
            capture_output=True,
            text=True,
            check=True
        )

    @patch('tools.release_helper.changes.subprocess.run')
    def test_get_changed_files_empty_result(self, mock_run):
        """Test getting changed files when no files changed."""
        mock_result = MagicMock()
        mock_result.stdout = ""
        mock_run.return_value = mock_result
        
        result = _get_changed_files("main")
        
        assert result == []

    @patch('tools.release_helper.changes.subprocess.run')
    def test_get_changed_files_whitespace_filtering(self, mock_run):
        """Test that empty lines and whitespace are filtered out."""
        mock_result = MagicMock()
        mock_result.stdout = "file1.py\n\n  \nfile2.go\n \t \n"
        mock_run.return_value = mock_result
        
        result = _get_changed_files("main")
        
        assert result == ["file1.py", "file2.go"]

    @patch('tools.release_helper.changes.subprocess.run')
    @patch('builtins.print')
    def test_get_changed_files_subprocess_error(self, mock_print, mock_run):
        """Test handling subprocess error when getting changed files."""
        error = subprocess.CalledProcessError(1, "git diff")
        mock_run.side_effect = error
        
        result = _get_changed_files("invalid-commit")
        
        assert result == []
        mock_print.assert_called_once_with(
            "Error getting changed files against invalid-commit: " + str(error),
            file=sys.stderr
        )

    @patch('tools.release_helper.changes.subprocess.run')
    def test_get_changed_files_different_base_commit(self, mock_run):
        """Test getting changed files with different base commit formats."""
        mock_result = MagicMock()
        mock_result.stdout = "changed.py\n"
        mock_run.return_value = mock_result
        
        # Test with SHA
        _get_changed_files("abc123def")
        mock_run.assert_called_with(
            ["git", "diff", "--name-only", "abc123def..HEAD"],
            capture_output=True,
            text=True,
            check=True
        )
        
        # Test with tag
        _get_changed_files("v1.0.0")
        mock_run.assert_called_with(
            ["git", "diff", "--name-only", "v1.0.0..HEAD"],
            capture_output=True,
            text=True,
            check=True
        )