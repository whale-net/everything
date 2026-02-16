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

import tools.release_helper.metadata as metadata_module
from tools.release_helper.metadata import get_app_metadata, list_all_apps, get_image_targets


@pytest.fixture(autouse=True)
def clear_metadata_cache():
    """Clear the metadata cache before each test."""
    metadata_module._metadata_cache.clear()
    yield
    metadata_module._metadata_cache.clear()


@pytest.fixture
def mock_run_bazel():
    """Fixture to mock run_bazel function."""
    with patch('tools.release_helper.metadata.run_bazel') as mock:
        yield mock


@pytest.fixture
def mock_find_workspace_root():
    """Fixture to mock find_workspace_root function."""
    with patch('tools.release_helper.metadata.find_workspace_root') as mock:
        mock.return_value = Path("/workspace")
        yield mock


@pytest.fixture
def mock_path_exists():
    """Fixture to mock pathlib.Path.exists method."""
    with patch('pathlib.Path.exists') as mock:
        mock.return_value = True
        yield mock


@pytest.fixture
def mock_file_open():
    """Fixture to mock builtins.open for file operations."""
    def _mock_open_with_data(data):
        return mock_open(read_data=data)
    return _mock_open_with_data


@pytest.fixture
def mock_get_app_metadata():
    """Fixture to mock get_app_metadata function."""
    with patch('tools.release_helper.metadata.get_app_metadata') as mock:
        yield mock


@pytest.fixture
def mock_print():
    """Fixture to mock print function."""
    with patch('builtins.print') as mock:
        yield mock


@pytest.fixture
def sample_metadata():
    """Fixture providing sample metadata for testing."""
    return {
        "name": "hello_fastapi",
        "version": "latest", 
        "binary_target": ":hello_fastapi",
        "image_target": "hello_fastapi_image",
        "description": "FastAPI hello world application",
        "language": "python",
        "registry": "ghcr.io",
        "repo_name": "demo-hello_fastapi",
        "domain": "demo",
        "openapi_spec_target": "//demo/hello_fastapi:hello-fastapi_openapi_spec"
    }


class TestGetAppMetadata:
    """Test cases for get_app_metadata function."""

    def test_get_app_metadata_success(self, mock_run_bazel, mock_find_workspace_root, 
                                      sample_metadata):
        """Test successful metadata retrieval."""
        bazel_target = "//demo/hello_fastapi:hello_fastapi_metadata"
        
        # _read_metadata_file is called twice: once before build (returns None), once after
        with patch('builtins.open', mock_open(read_data=json.dumps(sample_metadata))):
            with patch('pathlib.Path.exists', side_effect=[False, True]):
                result = get_app_metadata(bazel_target)
            
                # Verify bazel build was called (file didn't exist on first check)
                mock_run_bazel.assert_called_once_with(["build", bazel_target])
                
                assert result == sample_metadata

    def test_get_app_metadata_cached(self, mock_run_bazel, mock_find_workspace_root,
                                     sample_metadata):
        """Test that repeated calls return cached result without rebuilding."""
        bazel_target = "//demo/hello_fastapi:hello_fastapi_metadata"
        
        with patch('builtins.open', mock_open(read_data=json.dumps(sample_metadata))):
            with patch('pathlib.Path.exists', side_effect=[False, True]):
                result1 = get_app_metadata(bazel_target)
        
        # Second call should use cache, no additional bazel build
        result2 = get_app_metadata(bazel_target)
        
        assert result1 == result2
        mock_run_bazel.assert_called_once()  # Only one build call

    def test_get_app_metadata_reads_from_disk_without_building(self, mock_run_bazel, 
                                                                mock_find_workspace_root,
                                                                sample_metadata):
        """Test that if metadata file already exists on disk, no build is triggered."""
        bazel_target = "//demo/hello_fastapi:hello_fastapi_metadata"
        
        with patch('builtins.open', mock_open(read_data=json.dumps(sample_metadata))):
            with patch('pathlib.Path.exists', return_value=True):
                result = get_app_metadata(bazel_target)
        
        # No bazel build should be called since file exists
        mock_run_bazel.assert_not_called()
        assert result == sample_metadata

    def test_get_app_metadata_invalid_target_format_no_slashes(self):
        """Test error handling for invalid target format without double slashes."""
        bazel_target = "demo/hello_fastapi:hello_fastapi_metadata"
        
        with pytest.raises(ValueError, match="Invalid bazel target format"):
            get_app_metadata(bazel_target)

    def test_get_app_metadata_invalid_target_format_no_colon(self):
        """Test error handling for invalid target format without colon."""
        bazel_target = "//demo/hello_fastapi/hello_fastapi_metadata"
        
        with pytest.raises(ValueError, match="Invalid bazel target format"):
            get_app_metadata(bazel_target)

    def test_get_app_metadata_invalid_target_format_multiple_colons(self):
        """Test error handling for invalid target format with multiple colons."""
        bazel_target = "//demo/hello_fastapi:hello:fastapi_metadata"
        
        with pytest.raises(ValueError, match="Invalid bazel target format"):
            get_app_metadata(bazel_target)

    def test_get_app_metadata_file_not_found(self, mock_run_bazel, mock_find_workspace_root):
        """Test error handling when metadata file doesn't exist even after build."""
        bazel_target = "//demo/hello_fastapi:hello_fastapi_metadata"
        
        with patch('pathlib.Path.exists', return_value=False):
            with pytest.raises(FileNotFoundError, match="Metadata file not found"):
                get_app_metadata(bazel_target)
        
        # Verify bazel build was called (first read returned None)
        mock_run_bazel.assert_called_once_with(["build", bazel_target])

    def test_get_app_metadata_json_parse_error(self, mock_run_bazel, mock_find_workspace_root):
        """Test error handling when JSON parsing fails."""
        bazel_target = "//demo/hello_fastapi:hello_fastapi_metadata"
        
        with patch('builtins.open', mock_open(read_data="invalid json")):
            with patch('pathlib.Path.exists', return_value=True):
                with pytest.raises(json.JSONDecodeError):
                    get_app_metadata(bazel_target)


class TestListAllApps:
    """Test cases for list_all_apps function."""

    def test_list_all_apps_success(self, mock_run_bazel, mock_get_app_metadata):
        """Test successful listing of all apps."""
        # Mock bazel query output
        bazel_query_output = """//demo/hello_fastapi:hello_fastapi_metadata
//demo/hello_python:hello_python_metadata
//services/api:api_metadata"""
        
        # Mock metadata for each app
        metadata_responses = [
            {"name": "hello_fastapi", "domain": "demo", "language": "python"},
            {"name": "hello_python", "domain": "demo", "language": "python"}, 
            {"name": "api", "domain": "services", "language": "go"}
        ]
        
        # Mock the bazel query and batch build results
        mock_run_bazel.return_value = Mock(stdout=bazel_query_output)
        
        # Mock get_app_metadata calls
        mock_get_app_metadata.side_effect = metadata_responses
        
        result = list_all_apps()
        
        # Verify bazel query was called, then batch build
        assert mock_run_bazel.call_count == 2
        mock_run_bazel.assert_any_call([
            "query", "kind(app_metadata, //...)", "--output=label"
        ])
        mock_run_bazel.assert_any_call([
            "build",
            "//demo/hello_fastapi:hello_fastapi_metadata",
            "//demo/hello_python:hello_python_metadata",
            "//services/api:api_metadata"
        ])
        
        # Verify get_app_metadata was called for each target
        expected_calls = [
            "//demo/hello_fastapi:hello_fastapi_metadata",
            "//demo/hello_python:hello_python_metadata", 
            "//services/api:api_metadata"
        ]
        assert mock_get_app_metadata.call_count == 3
        for i, call in enumerate(mock_get_app_metadata.call_args_list):
            assert call[0][0] == expected_calls[i]
        
        # Verify result structure and sorting - now includes full metadata
        expected_result = [
            {
                'bazel_target': "//services/api:api_metadata",
                'name': "api",
                'domain': "services",
                'language': "go"
            },
            {
                'bazel_target': "//demo/hello_fastapi:hello_fastapi_metadata", 
                'name': "hello_fastapi",
                'domain': "demo",
                'language': "python"
            },
            {
                'bazel_target': "//demo/hello_python:hello_python_metadata",
                'name': "hello_python", 
                'domain': "demo",
                'language': "python"
            }
        ]
        assert result == expected_result

    def test_list_all_apps_empty_output(self, mock_run_bazel):
        """Test behavior when no apps are found."""
        # Mock empty bazel query output
        mock_run_bazel.return_value = Mock(stdout="")
        
        result = list_all_apps()
        
        assert result == []

    def test_list_all_apps_metadata_error_skipped(self, mock_run_bazel, mock_get_app_metadata, mock_print):
        """Test that apps with metadata errors are skipped with warning."""
        bazel_query_output = """//demo/hello_fastapi:hello_fastapi_metadata
//demo/broken_app:broken_app_metadata"""
        
        # Mock metadata - one successful, one failing
        def metadata_side_effect(target):
            if "hello_fastapi" in target:
                return {"name": "hello_fastapi", "domain": "demo", "language": "python"}
            else:
                raise FileNotFoundError("Metadata file not found")
        
        mock_run_bazel.return_value = Mock(stdout=bazel_query_output)
        mock_get_app_metadata.side_effect = metadata_side_effect
        
        result = list_all_apps()
        
        # Should only return the successful app with full metadata
        expected_result = [{
            'bazel_target': "//demo/hello_fastapi:hello_fastapi_metadata",
            'name': "hello_fastapi", 
            'domain': "demo",
            'language': "python"
        }]
        assert result == expected_result
        
        # Should print warning for failed app
        mock_print.assert_called_once()
        warning_call = mock_print.call_args[0][0]
        assert "Warning: Could not get metadata for //demo/broken_app:broken_app_metadata" in warning_call

    def test_list_all_apps_filters_non_metadata_targets(self, mock_run_bazel, mock_get_app_metadata):
        """Test that only targets with '_metadata' are processed."""
        bazel_query_output = """//demo/hello_fastapi:hello_fastapi_metadata
//demo/hello_fastapi:hello_fastapi
//demo/other:some_target"""
        
        mock_run_bazel.return_value = Mock(stdout=bazel_query_output)
        mock_get_app_metadata.return_value = {"name": "hello_fastapi", "domain": "demo", "language": "python"}
        
        result = list_all_apps()
        
        # Should only call get_app_metadata for the metadata target
        mock_get_app_metadata.assert_called_once_with("//demo/hello_fastapi:hello_fastapi_metadata")

    def test_list_all_apps_handles_whitespace_and_empty_lines(self, mock_run_bazel, mock_get_app_metadata):
        """Test that whitespace and empty lines in bazel output are handled correctly."""
        bazel_query_output = """
//demo/hello_fastapi:hello_fastapi_metadata

//demo/hello_python:hello_python_metadata   

"""
        
        mock_run_bazel.return_value = Mock(stdout=bazel_query_output)
        mock_get_app_metadata.side_effect = [
            {"name": "hello_fastapi", "domain": "demo", "language": "python"},
            {"name": "hello_python", "domain": "demo", "language": "python"}
        ]
        
        result = list_all_apps()
        
        # Should process both valid targets, ignoring empty lines
        assert len(result) == 2
        assert mock_get_app_metadata.call_count == 2

    def test_list_all_apps_handles_lines_without_metadata_suffix(self, mock_run_bazel, mock_get_app_metadata):
        """Test handling of lines that don't contain '_metadata'."""
        bazel_query_output = """//demo/hello_fastapi:hello_fastapi_metadata
//demo/hello_fastapi:hello_fastapi_image
//demo/other:regular_target"""
        
        mock_run_bazel.return_value = Mock(stdout=bazel_query_output)
        mock_get_app_metadata.return_value = {"name": "hello_fastapi", "domain": "demo", "language": "python"}
        
        result = list_all_apps()
        
        # Should only process the metadata target
        mock_get_app_metadata.assert_called_once_with("//demo/hello_fastapi:hello_fastapi_metadata")


class TestGetImageTargets:
    """Test cases for get_image_targets function."""

    def test_get_image_targets_success(self, mock_get_app_metadata):
        """Test successful generation of image targets."""
        bazel_target = "//demo/hello_fastapi:hello_fastapi_metadata"
        mock_metadata = {
            "image_target": "hello_fastapi_image"
        }
        
        mock_get_app_metadata.return_value = mock_metadata
        
        result = get_image_targets(bazel_target)
        
        expected_result = {
            "base": "//demo/hello_fastapi:hello_fastapi_image",
            "push": "//demo/hello_fastapi:hello_fastapi_image_push",
        }
        
        assert result == expected_result
        mock_get_app_metadata.assert_called_once_with(bazel_target)

    def test_get_image_targets_different_package_path(self, mock_get_app_metadata):
        """Test image targets with different package path."""
        bazel_target = "//services/backend/api:api_metadata"
        mock_metadata = {
            "image_target": "api_container"
        }
        
        mock_get_app_metadata.return_value = mock_metadata
        
        result = get_image_targets(bazel_target)
        
        expected_result = {
            "base": "//services/backend/api:api_container",
            "push": "//services/backend/api:api_container_push",
        }
        
        assert result == expected_result

    def test_get_image_targets_metadata_error_propagated(self, mock_get_app_metadata):
        """Test that metadata errors are propagated."""
        bazel_target = "//demo/nonexistent:metadata"
        
        mock_get_app_metadata.side_effect = FileNotFoundError("Metadata file not found")
        
        with pytest.raises(FileNotFoundError):
            get_image_targets(bazel_target)

    def test_get_image_targets_invalid_target_format(self, mock_get_app_metadata):
        """Test error handling when get_image_targets receives invalid bazel target."""
        bazel_target = "invalid_target_format"
        
        # This should fail during the target parsing in get_image_targets
        # but get_app_metadata gets called first, so we need to mock that to see the parsing error
        mock_get_app_metadata.side_effect = ValueError("Invalid bazel target format")
        
        with pytest.raises(ValueError, match="Invalid bazel target format"):
            get_image_targets(bazel_target)

    def test_get_image_targets_missing_image_target_in_metadata(self, mock_get_app_metadata):
        """Test error handling when metadata doesn't contain image_target key."""
        bazel_target = "//demo/hello_fastapi:hello_fastapi_metadata"
        mock_metadata = {
            "name": "hello_fastapi",
            # Missing "image_target" key
        }
        
        mock_get_app_metadata.return_value = mock_metadata
        
        with pytest.raises(KeyError):
            get_image_targets(bazel_target)