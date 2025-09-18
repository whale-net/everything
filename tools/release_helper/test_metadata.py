"""
Unit tests for the metadata portion of the release helper.

This module provides comprehensive unit tests for the metadata.py module,
covering all three main functions:
- get_app_metadata(): Retrieves metadata for a specific app
- list_all_apps(): Lists all apps with release metadata 
- get_image_targets(): Generates image target paths for an app

The tests use mocking to avoid actual Bazel command execution and file system
interactions, making them fast and reliable for CI/CD environments.
"""

import json
import pytest
from pathlib import Path
from unittest.mock import Mock, patch, mock_open, MagicMock
from subprocess import CompletedProcess

from tools.release_helper.metadata import get_app_metadata, list_all_apps, get_image_targets


class TestGetAppMetadata:
    """Test cases for get_app_metadata function."""

    def test_get_app_metadata_success(self):
        """Test successful metadata retrieval."""
        # Mock data
        bazel_target = "//demo/hello_fastapi:hello_fastapi_metadata"
        expected_metadata = {
            "name": "hello_fastapi",
            "version": "latest", 
            "binary_target": ":hello_fastapi",
            "image_target": "hello_fastapi_image",
            "description": "FastAPI hello world application",
            "language": "python",
            "registry": "ghcr.io",
            "repo_name": "demo-hello_fastapi",
            "domain": "demo"
        }
        
        with patch('tools.release_helper.metadata.run_bazel') as mock_run_bazel, \
             patch('tools.release_helper.metadata.find_workspace_root') as mock_find_root, \
             patch('builtins.open', mock_open(read_data=json.dumps(expected_metadata))) as mock_file, \
             patch('pathlib.Path.exists', return_value=True):
            
            mock_find_root.return_value = Path("/workspace")
            
            result = get_app_metadata(bazel_target)
            
            # Verify bazel build was called
            mock_run_bazel.assert_called_once_with(["build", bazel_target])
            
            # Verify correct file path was used
            expected_file_path = Path("/workspace/bazel-bin/demo/hello_fastapi/hello_fastapi_metadata_metadata.json")
            mock_file.assert_called_once_with(expected_file_path)
            
            assert result == expected_metadata

    def test_get_app_metadata_invalid_target_format_no_slashes(self):
        """Test error handling for invalid target format without double slashes."""
        bazel_target = "demo/hello_fastapi:hello_fastapi_metadata"
        
        with patch('tools.release_helper.metadata.run_bazel') as mock_run_bazel:
            with pytest.raises(ValueError, match="Invalid bazel target format"):
                get_app_metadata(bazel_target)
            
            # Verify run_bazel was called before validation
            mock_run_bazel.assert_called_once_with(["build", bazel_target])

    def test_get_app_metadata_invalid_target_format_no_colon(self):
        """Test error handling for invalid target format without colon."""
        bazel_target = "//demo/hello_fastapi/hello_fastapi_metadata"
        
        with patch('tools.release_helper.metadata.run_bazel') as mock_run_bazel:
            with pytest.raises(ValueError, match="Invalid bazel target format"):
                get_app_metadata(bazel_target)
            
            # Verify run_bazel was called before validation
            mock_run_bazel.assert_called_once_with(["build", bazel_target])

    def test_get_app_metadata_invalid_target_format_multiple_colons(self):
        """Test error handling for invalid target format with multiple colons."""
        bazel_target = "//demo/hello_fastapi:hello:fastapi_metadata"
        
        with patch('tools.release_helper.metadata.run_bazel') as mock_run_bazel:
            with pytest.raises(ValueError, match="Invalid bazel target format"):
                get_app_metadata(bazel_target)
            
            # Verify run_bazel was called before validation
            mock_run_bazel.assert_called_once_with(["build", bazel_target])

    def test_get_app_metadata_file_not_found(self):
        """Test error handling when metadata file doesn't exist."""
        bazel_target = "//demo/hello_fastapi:hello_fastapi_metadata"
        
        with patch('tools.release_helper.metadata.run_bazel') as mock_run_bazel, \
             patch('tools.release_helper.metadata.find_workspace_root') as mock_find_root, \
             patch('pathlib.Path.exists', return_value=False):
            
            mock_find_root.return_value = Path("/workspace")
            
            with pytest.raises(FileNotFoundError, match="Metadata file not found"):
                get_app_metadata(bazel_target)
            
            # Verify bazel build was still called
            mock_run_bazel.assert_called_once_with(["build", bazel_target])

    def test_get_app_metadata_json_parse_error(self):
        """Test error handling when JSON parsing fails."""
        bazel_target = "//demo/hello_fastapi:hello_fastapi_metadata"
        
        with patch('tools.release_helper.metadata.run_bazel') as mock_run_bazel, \
             patch('tools.release_helper.metadata.find_workspace_root') as mock_find_root, \
             patch('builtins.open', mock_open(read_data="invalid json")) as mock_file, \
             patch('pathlib.Path.exists', return_value=True):
            
            mock_find_root.return_value = Path("/workspace")
            
            with pytest.raises(json.JSONDecodeError):
                get_app_metadata(bazel_target)


class TestListAllApps:
    """Test cases for list_all_apps function."""

    def test_list_all_apps_success(self):
        """Test successful listing of all apps."""
        # Mock bazel query output
        bazel_query_output = """//demo/hello_fastapi:hello_fastapi_metadata
//demo/hello_python:hello_python_metadata
//services/api:api_metadata"""
        
        # Mock metadata for each app
        metadata_responses = [
            {"name": "hello_fastapi", "domain": "demo"},
            {"name": "hello_python", "domain": "demo"}, 
            {"name": "api", "domain": "services"}
        ]
        
        with patch('tools.release_helper.metadata.run_bazel') as mock_run_bazel, \
             patch('tools.release_helper.metadata.get_app_metadata') as mock_get_metadata:
            
            # Mock the bazel query result
            mock_run_bazel.return_value = Mock(stdout=bazel_query_output)
            
            # Mock get_app_metadata calls
            mock_get_metadata.side_effect = metadata_responses
            
            result = list_all_apps()
            
            # Verify bazel query was called correctly
            mock_run_bazel.assert_called_once_with([
                "query", "kind(app_metadata, //...)", "--output=label"
            ])
            
            # Verify get_app_metadata was called for each target
            expected_calls = [
                "//demo/hello_fastapi:hello_fastapi_metadata",
                "//demo/hello_python:hello_python_metadata", 
                "//services/api:api_metadata"
            ]
            assert mock_get_metadata.call_count == 3
            for i, call in enumerate(mock_get_metadata.call_args_list):
                assert call[0][0] == expected_calls[i]
            
            # Verify result structure and sorting
            expected_result = [
                {
                    'bazel_target': "//services/api:api_metadata",
                    'name': "api",
                    'domain': "services"
                },
                {
                    'bazel_target': "//demo/hello_fastapi:hello_fastapi_metadata", 
                    'name': "hello_fastapi",
                    'domain': "demo"
                },
                {
                    'bazel_target': "//demo/hello_python:hello_python_metadata",
                    'name': "hello_python", 
                    'domain': "demo"
                }
            ]
            assert result == expected_result

    def test_list_all_apps_empty_output(self):
        """Test behavior when no apps are found."""
        with patch('tools.release_helper.metadata.run_bazel') as mock_run_bazel:
            # Mock empty bazel query output
            mock_run_bazel.return_value = Mock(stdout="")
            
            result = list_all_apps()
            
            assert result == []

    def test_list_all_apps_metadata_error_skipped(self):
        """Test that apps with metadata errors are skipped with warning."""
        bazel_query_output = """//demo/hello_fastapi:hello_fastapi_metadata
//demo/broken_app:broken_app_metadata"""
        
        # Mock metadata - one successful, one failing
        def metadata_side_effect(target):
            if "hello_fastapi" in target:
                return {"name": "hello_fastapi", "domain": "demo"}
            else:
                raise FileNotFoundError("Metadata file not found")
        
        with patch('tools.release_helper.metadata.run_bazel') as mock_run_bazel, \
             patch('tools.release_helper.metadata.get_app_metadata') as mock_get_metadata, \
             patch('builtins.print') as mock_print:
            
            mock_run_bazel.return_value = Mock(stdout=bazel_query_output)
            mock_get_metadata.side_effect = metadata_side_effect
            
            result = list_all_apps()
            
            # Should only return the successful app
            expected_result = [{
                'bazel_target': "//demo/hello_fastapi:hello_fastapi_metadata",
                'name': "hello_fastapi", 
                'domain': "demo"
            }]
            assert result == expected_result
            
            # Should print warning for failed app
            mock_print.assert_called_once()
            warning_call = mock_print.call_args[0][0]
            assert "Warning: Could not get metadata for //demo/broken_app:broken_app_metadata" in warning_call

    def test_list_all_apps_filters_non_metadata_targets(self):
        """Test that only targets with '_metadata' are processed."""
        bazel_query_output = """//demo/hello_fastapi:hello_fastapi_metadata
//demo/hello_fastapi:hello_fastapi
//demo/other:some_target"""
        
        with patch('tools.release_helper.metadata.run_bazel') as mock_run_bazel, \
             patch('tools.release_helper.metadata.get_app_metadata') as mock_get_metadata:
            
            mock_run_bazel.return_value = Mock(stdout=bazel_query_output)
            mock_get_metadata.return_value = {"name": "hello_fastapi", "domain": "demo"}
            
            result = list_all_apps()
            
            # Should only call get_app_metadata for the metadata target
            mock_get_metadata.assert_called_once_with("//demo/hello_fastapi:hello_fastapi_metadata")

    def test_list_all_apps_handles_whitespace_and_empty_lines(self):
        """Test that whitespace and empty lines in bazel output are handled correctly."""
        bazel_query_output = """
//demo/hello_fastapi:hello_fastapi_metadata

//demo/hello_python:hello_python_metadata   

"""
        
        with patch('tools.release_helper.metadata.run_bazel') as mock_run_bazel, \
             patch('tools.release_helper.metadata.get_app_metadata') as mock_get_metadata:
            
            mock_run_bazel.return_value = Mock(stdout=bazel_query_output)
            mock_get_metadata.side_effect = [
                {"name": "hello_fastapi", "domain": "demo"},
                {"name": "hello_python", "domain": "demo"}
            ]
            
            result = list_all_apps()
            
            # Should process both valid targets, ignoring empty lines
            assert len(result) == 2
            assert mock_get_metadata.call_count == 2

    def test_list_all_apps_handles_lines_without_metadata_suffix(self):
        """Test handling of lines that don't contain '_metadata'."""
        bazel_query_output = """//demo/hello_fastapi:hello_fastapi_metadata
//demo/hello_fastapi:hello_fastapi_image
//demo/other:regular_target"""
        
        with patch('tools.release_helper.metadata.run_bazel') as mock_run_bazel, \
             patch('tools.release_helper.metadata.get_app_metadata') as mock_get_metadata:
            
            mock_run_bazel.return_value = Mock(stdout=bazel_query_output)
            mock_get_metadata.return_value = {"name": "hello_fastapi", "domain": "demo"}
            
            result = list_all_apps()
            
            # Should only process the metadata target
            mock_get_metadata.assert_called_once_with("//demo/hello_fastapi:hello_fastapi_metadata")


class TestGetImageTargets:
    """Test cases for get_image_targets function."""

    def test_get_image_targets_success(self):
        """Test successful generation of image targets."""
        bazel_target = "//demo/hello_fastapi:hello_fastapi_metadata"
        mock_metadata = {
            "image_target": "hello_fastapi_image"
        }
        
        with patch('tools.release_helper.metadata.get_app_metadata') as mock_get_metadata:
            mock_get_metadata.return_value = mock_metadata
            
            result = get_image_targets(bazel_target)
            
            expected_result = {
                "base": "//demo/hello_fastapi:hello_fastapi_image",
                "amd64": "//demo/hello_fastapi:hello_fastapi_image_amd64",
                "arm64": "//demo/hello_fastapi:hello_fastapi_image_arm64", 
                "push_base": "//demo/hello_fastapi:hello_fastapi_image_push",
                "push_amd64": "//demo/hello_fastapi:hello_fastapi_image_push_amd64",
                "push_arm64": "//demo/hello_fastapi:hello_fastapi_image_push_arm64"
            }
            
            assert result == expected_result
            mock_get_metadata.assert_called_once_with(bazel_target)

    def test_get_image_targets_different_package_path(self):
        """Test image targets with different package path."""
        bazel_target = "//services/backend/api:api_metadata"
        mock_metadata = {
            "image_target": "api_container"
        }
        
        with patch('tools.release_helper.metadata.get_app_metadata') as mock_get_metadata:
            mock_get_metadata.return_value = mock_metadata
            
            result = get_image_targets(bazel_target)
            
            expected_result = {
                "base": "//services/backend/api:api_container",
                "amd64": "//services/backend/api:api_container_amd64",
                "arm64": "//services/backend/api:api_container_arm64",
                "push_base": "//services/backend/api:api_container_push", 
                "push_amd64": "//services/backend/api:api_container_push_amd64",
                "push_arm64": "//services/backend/api:api_container_push_arm64"
            }
            
            assert result == expected_result

    def test_get_image_targets_metadata_error_propagated(self):
        """Test that metadata errors are propagated."""
        bazel_target = "//demo/nonexistent:metadata"
        
        with patch('tools.release_helper.metadata.get_app_metadata') as mock_get_metadata:
            mock_get_metadata.side_effect = FileNotFoundError("Metadata file not found")
            
            with pytest.raises(FileNotFoundError):
                get_image_targets(bazel_target)

    def test_get_image_targets_invalid_target_format(self):
        """Test error handling when get_image_targets receives invalid bazel target."""
        bazel_target = "invalid_target_format"
        
        # This should fail during the target parsing in get_image_targets
        # but get_app_metadata gets called first, so we need to mock that to see the parsing error
        with patch('tools.release_helper.metadata.get_app_metadata') as mock_get_metadata:
            mock_get_metadata.side_effect = ValueError("Invalid bazel target format")
            
            with pytest.raises(ValueError, match="Invalid bazel target format"):
                get_image_targets(bazel_target)

    def test_get_image_targets_missing_image_target_in_metadata(self):
        """Test error handling when metadata doesn't contain image_target key."""
        bazel_target = "//demo/hello_fastapi:hello_fastapi_metadata"
        mock_metadata = {
            "name": "hello_fastapi",
            # Missing "image_target" key
        }
        
        with patch('tools.release_helper.metadata.get_app_metadata') as mock_get_metadata:
            mock_get_metadata.return_value = mock_metadata
            
            with pytest.raises(KeyError):
                get_image_targets(bazel_target)