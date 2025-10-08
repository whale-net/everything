"""
Unit tests for the install_venv tool.

Tests the virtual environment installation tool's functionality including:
- Finding workspace root
- Creating virtual environments with UV
- Installing dependencies from uv.lock
"""

import pytest
import sys
import tempfile
from pathlib import Path
from unittest.mock import Mock, patch, MagicMock, call
import subprocess

# Import the module we're testing
sys.path.insert(0, str(Path(__file__).parent.parent))
from install_venv import find_workspace_root, install_venv, check_uv_available


class TestFindWorkspaceRoot:
    """Tests for find_workspace_root function."""
    
    def test_finds_workspace_with_pyproject(self, tmp_path):
        """Test that find_workspace_root locates pyproject.toml."""
        # Create a pyproject.toml in the temp directory
        pyproject = tmp_path / "pyproject.toml"
        pyproject.write_text("[project]\nname = 'test'\n")
        
        # Create a subdirectory and test from there
        subdir = tmp_path / "subdir"
        subdir.mkdir()
        
        with patch('pathlib.Path.cwd', return_value=subdir):
            result = find_workspace_root()
            assert result == tmp_path
    
    def test_raises_when_no_pyproject(self, tmp_path):
        """Test that find_workspace_root raises error when no pyproject.toml found."""
        # Don't create pyproject.toml
        with patch('pathlib.Path.cwd', return_value=tmp_path):
            with pytest.raises(RuntimeError, match="Could not find workspace root"):
                find_workspace_root()


class TestCheckUvAvailable:
    """Tests for check_uv_available function."""
    
    @patch('shutil.which')
    def test_uv_available(self, mock_which):
        """Test UV is detected when available."""
        mock_which.return_value = "/usr/local/bin/uv"
        assert check_uv_available() is True
        mock_which.assert_called_once_with("uv")
    
    @patch('shutil.which')
    def test_uv_not_available(self, mock_which):
        """Test UV is not detected when unavailable."""
        mock_which.return_value = None
        assert check_uv_available() is False


class TestInstallVenv:
    """Tests for install_venv function."""
    
    @patch('install_venv.check_uv_available')
    @patch('subprocess.run')
    def test_creates_venv_successfully_with_uv(self, mock_run, mock_uv_check, tmp_path):
        """Test successful venv creation using UV sync."""
        mock_uv_check.return_value = True
        mock_run.return_value = Mock(returncode=0)
        
        workspace_root = tmp_path / "workspace"
        workspace_root.mkdir()
        
        # Create uv.lock
        lock_file = workspace_root / "uv.lock"
        lock_file.write_text("# lock file content")
        
        venv_dir = tmp_path / "test_venv"
        
        result = install_venv(venv_dir, workspace_root)
        
        # Should return True on success
        assert result is True
        # Verify subprocess.run was called with uv sync
        assert any(
            "uv" in str(call_args[0][0])
            for call_args in mock_run.call_args_list
        )
    
    @patch('install_venv.check_uv_available')
    @patch('subprocess.run')
    def test_fails_when_no_lock_file(self, mock_run, mock_uv_check, tmp_path):
        """Test failure when uv.lock doesn't exist."""
        mock_uv_check.return_value = True
        
        workspace_root = tmp_path / "workspace"
        workspace_root.mkdir()
        
        # Don't create uv.lock
        venv_dir = tmp_path / "test_venv"
        
        result = install_venv(venv_dir, workspace_root)
        
        # Should return False when lock file is missing
        assert result is False
    
    @patch('install_venv.check_uv_available')
    @patch('install_venv.install_uv')
    @patch('subprocess.run')
    def test_installs_uv_when_not_available(self, mock_run, mock_install_uv, mock_uv_check, tmp_path):
        """Test that UV is installed when not available."""
        # First call returns False (UV not available), second returns True (after install)
        mock_uv_check.side_effect = [False, True]
        mock_install_uv.return_value = True
        mock_run.return_value = Mock(returncode=0)
        
        workspace_root = tmp_path / "workspace"
        workspace_root.mkdir()
        
        # Create uv.lock
        lock_file = workspace_root / "uv.lock"
        lock_file.write_text("# lock file content")
        
        venv_dir = tmp_path / "test_venv"
        
        result = install_venv(venv_dir, workspace_root)
        
        # Should install UV and succeed
        mock_install_uv.assert_called_once()
    
    @patch('install_venv.check_uv_available')
    @patch('subprocess.run')
    def test_handles_uv_sync_error_with_fallback(self, mock_run, mock_uv_check, tmp_path):
        """Test fallback to manual venv creation when uv sync fails."""
        mock_uv_check.return_value = True
        
        # First call (uv sync) fails, subsequent calls succeed
        mock_run.side_effect = [
            subprocess.CalledProcessError(1, "uv sync"),  # uv sync fails
            Mock(returncode=0),  # venv creation succeeds
            Mock(returncode=0),  # uv pip sync succeeds
        ]
        
        workspace_root = tmp_path / "workspace"
        workspace_root.mkdir()
        
        # Create uv.lock
        lock_file = workspace_root / "uv.lock"
        lock_file.write_text("# lock file content")
        
        venv_dir = tmp_path / "test_venv"
        
        result = install_venv(venv_dir, workspace_root)
        
        # Should fall back and succeed
        assert result is True
        # Should have called subprocess.run multiple times (sync, venv, pip sync)
        assert mock_run.call_count == 3


class TestMainFunction:
    """Tests for the main CLI function."""
    
    @patch('install_venv.install_venv')
    @patch('install_venv.find_workspace_root')
    @patch('builtins.input')
    def test_main_with_existing_venv(self, mock_input, mock_find_root, mock_install, tmp_path):
        """Test main function when venv already exists."""
        from install_venv import main
        
        # Setup mocks
        workspace = tmp_path / "workspace"
        workspace.mkdir()
        mock_find_root.return_value = workspace
        
        venv_dir = workspace / ".venv"
        venv_dir.mkdir()
        
        # User chooses not to recreate
        mock_input.return_value = "n"
        
        with patch('sys.argv', ['install_venv.py']):
            result = main()
        
        # Should exit without calling install_venv
        assert result == 0
        mock_install.assert_not_called()
    
    @patch('install_venv.install_venv')
    @patch('install_venv.find_workspace_root')
    def test_main_with_new_venv(self, mock_find_root, mock_install, tmp_path):
        """Test main function with new venv."""
        from install_venv import main
        
        # Setup mocks
        workspace = tmp_path / "workspace"
        workspace.mkdir()
        mock_find_root.return_value = workspace
        mock_install.return_value = True
        
        with patch('sys.argv', ['install_venv.py', '--venv-dir', 'test_venv']):
            result = main()
        
        # Should call install_venv
        assert result == 0
        mock_install.assert_called_once()
