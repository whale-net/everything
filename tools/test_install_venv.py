"""
Unit tests for the install_venv tool.

Tests the virtual environment installation tool's functionality including:
- Finding workspace root
- Creating virtual environments
- Installing dependencies from pyproject.toml
"""

import pytest
import sys
import tempfile
from pathlib import Path
from unittest.mock import Mock, patch, MagicMock, call
import subprocess

# Import the module we're testing
sys.path.insert(0, str(Path(__file__).parent.parent))
from install_venv import find_workspace_root, install_venv


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


class TestInstallVenv:
    """Tests for install_venv function."""
    
    @patch('subprocess.run')
    def test_creates_venv_successfully(self, mock_run, tmp_path):
        """Test successful venv creation."""
        mock_run.return_value = Mock(returncode=0)
        
        workspace_root = tmp_path / "workspace"
        workspace_root.mkdir()
        
        # Create minimal pyproject.toml
        pyproject = workspace_root / "pyproject.toml"
        pyproject.write_text("""
[project]
name = "test"
dependencies = []
""")
        
        venv_dir = tmp_path / "test_venv"
        
        # We can't actually test the full install due to it requiring tomllib
        # and actual venv creation, so we mock subprocess.run
        with patch('builtins.open', create=True) as mock_file:
            mock_file.return_value.__enter__.return_value.read.return_value = b"""
[project]
name = "test"
dependencies = []
"""
            result = install_venv(venv_dir, workspace_root)
        
        # Verify subprocess.run was called to create venv
        assert any(
            call_args[0][0][2] == "venv" 
            for call_args in mock_run.call_args_list 
            if len(call_args[0][0]) > 2
        )
    
    @patch('subprocess.run')
    def test_handles_venv_creation_error(self, mock_run, tmp_path):
        """Test error handling when venv creation fails."""
        # Make the first subprocess.run call fail
        mock_run.side_effect = subprocess.CalledProcessError(1, "venv")
        
        workspace_root = tmp_path / "workspace"
        workspace_root.mkdir()
        
        pyproject = workspace_root / "pyproject.toml"
        pyproject.write_text("[project]\nname = 'test'\n")
        
        venv_dir = tmp_path / "test_venv"
        
        result = install_venv(venv_dir, workspace_root)
        
        # Should return False on error
        assert result is False
    
    @patch('subprocess.run')
    def test_handles_pip_timeout(self, mock_run, tmp_path):
        """Test handling of pip timeout during upgrade."""
        # First call succeeds (venv creation), second times out (pip upgrade)
        mock_run.side_effect = [
            Mock(returncode=0),  # venv creation
            subprocess.TimeoutExpired("pip", 60),  # pip upgrade timeout
        ]
        
        workspace_root = tmp_path / "workspace"
        workspace_root.mkdir()
        
        pyproject = workspace_root / "pyproject.toml"
        pyproject.write_text("""
[project]
name = "test"
dependencies = []
""")
        
        venv_dir = tmp_path / "test_venv"
        
        # Mock tomllib/tomli loading
        with patch('builtins.open', create=True) as mock_file:
            # Configure mock for binary read mode
            mock_file.return_value.__enter__.return_value.read.return_value = b"""
[project]
name = "test"
dependencies = []
"""
            with patch('tomllib.load') as mock_toml:
                mock_toml.return_value = {
                    "project": {
                        "name": "test",
                        "dependencies": []
                    }
                }
                
                result = install_venv(venv_dir, workspace_root)
        
        # Should still return True since timeout on pip upgrade is non-fatal
        # (it continues with existing pip version)
        assert result is True


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
