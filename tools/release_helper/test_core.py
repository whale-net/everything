"""
Unit tests for the core portion of the release helper.

This module provides comprehensive unit tests for the core.py module,
covering all core utility functions:
- find_workspace_root(): Workspace directory detection
- run_bazel(): Bazel command execution wrapper

The tests use mocking to avoid actual file system operations and command execution,
making them fast and reliable for CI/CD environments.
"""

import os
import subprocess
import pytest
from pathlib import Path
from unittest.mock import Mock, patch, MagicMock

from tools.release_helper.core import find_workspace_root, run_bazel


class TestFindWorkspaceRoot:
    """Test cases for find_workspace_root function."""

    def test_find_workspace_root_with_build_workspace_directory(self, build_workspace_env):
        """Test finding workspace root using BUILD_WORKSPACE_DIRECTORY environment variable."""
        result = find_workspace_root()
        assert result == Path(build_workspace_env)

    def test_find_workspace_root_with_workspace_file(self, clean_environ):
        """Test finding workspace root by locating WORKSPACE file."""
        with patch('pathlib.Path.cwd') as mock_cwd:
            mock_cwd.return_value = Path("/some/project/subdir")
            
            # Mock Path.exists to return True for WORKSPACE at parent directory
            def mock_exists(self):
                return str(self).endswith("/some/project/WORKSPACE")
            
            with patch.object(Path, 'exists', mock_exists):
                result = find_workspace_root()
            
            assert result == Path("/some/project")

    def test_find_workspace_root_with_module_bazel_file(self, clean_environ):
        """Test finding workspace root by locating MODULE.bazel file."""
        with patch('pathlib.Path.cwd') as mock_cwd:
            mock_cwd.return_value = Path("/some/project/subdir")
            
            # Mock Path.exists to return True for MODULE.bazel at parent directory
            def mock_exists(self):
                return str(self).endswith("/some/project/MODULE.bazel")
            
            with patch.object(Path, 'exists', mock_exists):
                result = find_workspace_root()
            
            assert result == Path("/some/project")

    def test_find_workspace_root_no_markers_found(self, clean_environ):
        """Test finding workspace root when no markers are found (fallback to current directory)."""
        with patch('pathlib.Path.cwd') as mock_cwd:
            current_dir = Path("/some/random/dir")
            mock_cwd.return_value = current_dir
            
            # Mock Path.exists to always return False (no workspace markers)
            with patch.object(Path, 'exists', return_value=False):
                result = find_workspace_root()
            
            assert result == current_dir

    def test_find_workspace_root_current_directory_has_marker(self, clean_environ):
        """Test finding workspace root when current directory has workspace marker."""
        with patch('pathlib.Path.cwd') as mock_cwd:
            current_dir = Path("/workspace/root")
            mock_cwd.return_value = current_dir
            
            # Mock Path.exists to return True for current directory
            def mock_exists(self):
                return str(self) in ["/workspace/root/WORKSPACE", "/workspace/root/MODULE.bazel"]
            
            with patch.object(Path, 'exists', mock_exists):
                result = find_workspace_root()
            
            assert result == current_dir

    def test_find_workspace_root_deep_nested_structure(self, clean_environ):
        """Test finding workspace root from deeply nested directory structure."""
        with patch('pathlib.Path.cwd') as mock_cwd:
            mock_cwd.return_value = Path("/workspace/project/tools/release_helper/tests")
            
            # Mock Path.exists to return True only for workspace root
            def mock_exists(self):
                return str(self).endswith("/workspace/project/WORKSPACE")
            
            with patch.object(Path, 'exists', mock_exists):
                result = find_workspace_root()
            
            assert result == Path("/workspace/project")


class TestRunBazel:
    """Test cases for run_bazel function."""

    @patch('tools.release_helper.core.find_workspace_root')
    @patch('subprocess.run')
    def test_run_bazel_success(self, mock_subprocess_run, mock_find_workspace_root):
        """Test successful bazel command execution."""
        workspace_path = Path("/workspace/root")
        mock_find_workspace_root.return_value = workspace_path
        
        # Mock successful subprocess.run
        expected_result = Mock(returncode=0, stdout="success", stderr="")
        mock_subprocess_run.return_value = expected_result
        
        result = run_bazel(["build", "//demo:hello_python"])
        
        # Verify subprocess.run was called correctly
        mock_subprocess_run.assert_called_once_with(
            ["bazel", "build", "//demo:hello_python"],
            capture_output=True,
            text=True,
            check=True,
            cwd=workspace_path,
            env=os.environ.copy()
        )
        
        assert result == expected_result

    @patch('tools.release_helper.core.find_workspace_root')
    @patch('subprocess.run')
    def test_run_bazel_with_custom_env(self, mock_subprocess_run, mock_find_workspace_root):
        """Test bazel command execution with custom environment."""
        workspace_path = Path("/workspace/root")
        mock_find_workspace_root.return_value = workspace_path
        
        expected_result = Mock(returncode=0, stdout="success", stderr="")
        mock_subprocess_run.return_value = expected_result
        
        custom_env = {"CUSTOM_VAR": "custom_value"}
        result = run_bazel(["test", "//tools:test"], env=custom_env)
        
        # Verify subprocess.run was called with custom environment
        mock_subprocess_run.assert_called_once_with(
            ["bazel", "test", "//tools:test"],
            capture_output=True,
            text=True,
            check=True,
            cwd=workspace_path,
            env=custom_env
        )
        
        assert result == expected_result

    @patch('tools.release_helper.core.find_workspace_root')
    @patch('subprocess.run')
    def test_run_bazel_no_capture_output(self, mock_subprocess_run, mock_find_workspace_root):
        """Test bazel command execution without capturing output."""
        workspace_path = Path("/workspace/root")
        mock_find_workspace_root.return_value = workspace_path
        
        expected_result = Mock(returncode=0)
        mock_subprocess_run.return_value = expected_result
        
        result = run_bazel(["run", "//demo:hello_python"], capture_output=False)
        
        # Verify subprocess.run was called with capture_output=False
        mock_subprocess_run.assert_called_once_with(
            ["bazel", "run", "//demo:hello_python"],
            capture_output=False,
            text=True,
            check=True,
            cwd=workspace_path,
            env=os.environ.copy()
        )
        
        assert result == expected_result

    @patch('tools.release_helper.core.find_workspace_root')
    @patch('subprocess.run')
    def test_run_bazel_command_failure(self, mock_subprocess_run, mock_find_workspace_root, mock_print):
        """Test bazel command execution failure handling."""
        workspace_path = Path("/workspace/root")
        mock_find_workspace_root.return_value = workspace_path
        
        # Mock subprocess.CalledProcessError
        error = subprocess.CalledProcessError(
            returncode=1, 
            cmd=["bazel", "build", "//invalid:target"]
        )
        error.stdout = "build output"
        error.stderr = "build error"
        mock_subprocess_run.side_effect = error
        
        with pytest.raises(subprocess.CalledProcessError):
            run_bazel(["build", "//invalid:target"])
        
        # Verify error information was printed
        assert mock_print.call_count >= 1
        
        # Check that print was called with relevant error information
        print_calls = [call[0][0] for call in mock_print.call_args_list]
        assert any("Bazel command failed" in call for call in print_calls)
        assert any("Working directory" in call for call in print_calls)

    @patch('tools.release_helper.core.find_workspace_root')
    @patch('subprocess.run')
    def test_run_bazel_command_failure_no_stdout_stderr(self, mock_subprocess_run, mock_find_workspace_root, mock_print):
        """Test bazel command execution failure with no stdout/stderr."""
        workspace_path = Path("/workspace/root")
        mock_find_workspace_root.return_value = workspace_path
        
        # Mock subprocess.CalledProcessError without stdout/stderr
        error = subprocess.CalledProcessError(
            returncode=1, 
            cmd=["bazel", "build", "//invalid:target"]
        )
        error.stdout = None
        error.stderr = None
        mock_subprocess_run.side_effect = error
        
        with pytest.raises(subprocess.CalledProcessError):
            run_bazel(["build", "//invalid:target"])
        
        # Verify error information was printed (basic error info only)
        assert mock_print.call_count >= 2  # At least "command failed" and "working directory"

    @patch('tools.release_helper.core.find_workspace_root')  
    @patch('subprocess.run')
    def test_run_bazel_empty_args(self, mock_subprocess_run, mock_find_workspace_root):
        """Test bazel command execution with empty arguments."""
        workspace_path = Path("/workspace/root")
        mock_find_workspace_root.return_value = workspace_path
        
        expected_result = Mock(returncode=0, stdout="", stderr="")
        mock_subprocess_run.return_value = expected_result
        
        result = run_bazel([])
        
        # Verify subprocess.run was called with just "bazel" command
        mock_subprocess_run.assert_called_once_with(
            ["bazel"],
            capture_output=True,
            text=True,
            check=True,
            cwd=workspace_path,
            env=os.environ.copy()
        )
        
        assert result == expected_result

    @patch('tools.release_helper.core.find_workspace_root')
    @patch('subprocess.run')
    def test_run_bazel_complex_args(self, mock_subprocess_run, mock_find_workspace_root):
        """Test bazel command execution with complex arguments."""
        workspace_path = Path("/workspace/root")
        mock_find_workspace_root.return_value = workspace_path
        
        expected_result = Mock(returncode=0, stdout="success", stderr="")
        mock_subprocess_run.return_value = expected_result
        
        complex_args = [
            "test",
            "//tools/release_helper:test_validation", 
            "--test_output=all",
            "--test_arg=--verbose",
            "--config=ci"
        ]
        result = run_bazel(complex_args)
        
        # Verify subprocess.run was called with all arguments
        expected_cmd = ["bazel"] + complex_args
        mock_subprocess_run.assert_called_once_with(
            expected_cmd,
            capture_output=True,
            text=True,
            check=True,
            cwd=workspace_path,
            env=os.environ.copy()
        )
        
        assert result == expected_result