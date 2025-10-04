"""
Unit tests for the github_release portion of the release helper.

This module provides comprehensive unit tests for the github_release.py module,
focusing on the permission validation logic that was updated to handle GitHub Actions tokens.
"""

import os
import pytest
from unittest.mock import Mock, patch, MagicMock
import sys
import json

# Add the parent directory to path for imports
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

try:
    from tools.release_helper.github_release import GitHubReleaseClient, create_releases_for_apps_with_notes
except ImportError:
    # httpx not available in this environment, skip tests
    pytest.skip("httpx not available, skipping github_release tests", allow_module_level=True)


class TestGitHubReleaseClientPermissions:
    """Test cases for GitHubReleaseClient permission validation."""
    
    def test_github_actions_environment_bypass(self):
        """Test that GitHub Actions environment bypasses permission validation."""
        with patch.dict(os.environ, {'GITHUB_ACTIONS': 'true'}):
            client = GitHubReleaseClient("test-owner", "test-repo", "dummy-token")
            
            # Should return True without making any API calls
            result = client.validate_permissions()
            assert result is True
    
    def test_traditional_push_permission(self):
        """Test validation with traditional push permission."""
        mock_response = Mock()
        mock_response.status_code = 200
        mock_response.json.return_value = {
            "permissions": {"push": True, "pull": True, "admin": False}
        }
        
        with patch('httpx.Client') as mock_client:
            mock_client.return_value.__enter__.return_value.get.return_value = mock_response
            
            # Ensure we're not in GitHub Actions environment
            with patch.dict(os.environ, {}, clear=True):
                client = GitHubReleaseClient("test-owner", "test-repo", "dummy-token")
                result = client.validate_permissions()
                assert result is True
    
    def test_admin_permission(self):
        """Test validation with admin permission."""
        mock_response = Mock()
        mock_response.status_code = 200
        mock_response.json.return_value = {
            "permissions": {"push": False, "pull": True, "admin": True}
        }
        
        with patch('httpx.Client') as mock_client:
            mock_client.return_value.__enter__.return_value.get.return_value = mock_response
            
            with patch.dict(os.environ, {}, clear=True):
                client = GitHubReleaseClient("test-owner", "test-repo", "dummy-token")
                result = client.validate_permissions()
                assert result is True
    
    def test_contents_write_permission(self):
        """Test validation with GitHub Actions style contents write permission."""
        mock_response = Mock()
        mock_response.status_code = 200
        mock_response.json.return_value = {
            "permissions": {"contents": "write", "pull": True}
        }
        
        with patch('httpx.Client') as mock_client:
            mock_client.return_value.__enter__.return_value.get.return_value = mock_response
            
            with patch.dict(os.environ, {}, clear=True):
                client = GitHubReleaseClient("test-owner", "test-repo", "dummy-token")
                result = client.validate_permissions()
                assert result is True
    
    def test_maintain_permission(self):
        """Test validation with maintain permission."""
        mock_response = Mock()
        mock_response.status_code = 200
        mock_response.json.return_value = {
            "permissions": {"maintain": True, "pull": True}
        }
        
        with patch('httpx.Client') as mock_client:
            mock_client.return_value.__enter__.return_value.get.return_value = mock_response
            
            with patch.dict(os.environ, {}, clear=True):
                client = GitHubReleaseClient("test-owner", "test-repo", "dummy-token")
                result = client.validate_permissions()
                assert result is True
    
    def test_generic_write_permission(self):
        """Test validation with generic write permission."""
        mock_response = Mock()
        mock_response.status_code = 200
        mock_response.json.return_value = {
            "permissions": {"write": True, "read": True}
        }
        
        with patch('httpx.Client') as mock_client:
            mock_client.return_value.__enter__.return_value.get.return_value = mock_response
            
            with patch.dict(os.environ, {}, clear=True):
                client = GitHubReleaseClient("test-owner", "test-repo", "dummy-token")
                result = client.validate_permissions()
                assert result is True
    
    def test_insufficient_permissions_with_fallback_success(self):
        """Test that fallback to releases endpoint works when permissions are unclear."""
        mock_repo_response = Mock()
        mock_repo_response.status_code = 200
        mock_repo_response.json.return_value = {
            "permissions": {"pull": True}  # Read-only
        }
        
        mock_releases_response = Mock()
        mock_releases_response.status_code = 200  # Can access releases
        
        with patch('httpx.Client') as mock_client:
            mock_client_instance = mock_client.return_value.__enter__.return_value
            mock_client_instance.get.side_effect = [mock_repo_response, mock_releases_response]
            
            with patch.dict(os.environ, {}, clear=True):
                client = GitHubReleaseClient("test-owner", "test-repo", "dummy-token")
                result = client.validate_permissions()
                assert result is True
    
    def test_insufficient_permissions_with_fallback_failure(self):
        """Test that validation fails when both permissions and fallback fail."""
        mock_repo_response = Mock()
        mock_repo_response.status_code = 200
        mock_repo_response.json.return_value = {
            "permissions": {"pull": True}  # Read-only
        }
        
        mock_releases_response = Mock()
        mock_releases_response.status_code = 403  # Forbidden
        
        with patch('httpx.Client') as mock_client:
            mock_client_instance = mock_client.return_value.__enter__.return_value
            mock_client_instance.get.side_effect = [mock_repo_response, mock_releases_response]
            
            with patch.dict(os.environ, {}, clear=True):
                client = GitHubReleaseClient("test-owner", "test-repo", "dummy-token")
                result = client.validate_permissions()
                assert result is False
    
    def test_repo_access_forbidden(self):
        """Test handling of 403 Forbidden when accessing repository."""
        mock_response = Mock()
        mock_response.status_code = 403
        
        with patch('httpx.Client') as mock_client:
            mock_client.return_value.__enter__.return_value.get.return_value = mock_response
            
            with patch.dict(os.environ, {}, clear=True):
                client = GitHubReleaseClient("test-owner", "test-repo", "dummy-token")
                result = client.validate_permissions()
                assert result is False
    
    def test_repo_not_found(self):
        """Test handling of 404 Not Found when accessing repository."""
        mock_response = Mock()
        mock_response.status_code = 404
        
        with patch('httpx.Client') as mock_client:
            mock_client.return_value.__enter__.return_value.get.return_value = mock_response
            
            with patch.dict(os.environ, {}, clear=True):
                client = GitHubReleaseClient("test-owner", "test-repo", "dummy-token")
                result = client.validate_permissions()
                assert result is False


class TestCreateReleasesWithIndividualVersions:
    """Test cases for create_releases_for_apps_with_individual_versions function."""
    
    def test_individual_versions_tag_creation(self):
        """Test that individual versions create correct tag names."""
        app_versions = {"hello_fastapi": "v0.0.8", "hello_python": "v1.2.3"}
        app_list = ["hello_fastapi"]
        
        # Mock the required functions
        with patch('tools.release_helper.github_release.find_app_bazel_target') as mock_find_target, \
             patch('tools.release_helper.github_release.get_app_metadata') as mock_get_metadata, \
             patch('tools.release_helper.github_release.generate_release_notes') as mock_gen_notes, \
             patch('tools.release_helper.github_release.create_app_release') as mock_create_release:
            
            # Setup mocks
            mock_find_target.return_value = "//demo/hello_fastapi:hello_fastapi_metadata"
            mock_get_metadata.return_value = {"domain": "demo", "name": "hello_fastapi"}
            mock_gen_notes.return_value = "Release notes content"
            mock_create_release.return_value = {"id": 123, "tag_name": "demo-hello_fastapi.v0.0.8"}
            
            # Call the enhanced function with app_versions
            results = create_releases_for_apps_with_notes(
                app_list=app_list,
                owner="test-owner",
                repo="test-repo",
                app_versions=app_versions
            )
            
            # Verify the results
            assert "hello_fastapi" in results
            assert results["hello_fastapi"] is not None
            
            # Verify the correct tag name was used
            mock_create_release.assert_called_once()
            call_args = mock_create_release.call_args
            assert call_args[1]["tag_name"] == "demo-hello_fastapi.v0.0.8"
    
    def test_missing_version_in_app_versions(self):
        """Test handling when an app is not in app_versions dict."""
        app_versions = {"hello_fastapi": "v0.0.8"}
        app_list = ["hello_fastapi", "missing_app"]
        
        with patch('tools.release_helper.github_release.find_app_bazel_target') as mock_find_target, \
             patch('tools.release_helper.github_release.get_app_metadata') as mock_get_metadata, \
             patch('tools.release_helper.github_release.generate_release_notes') as mock_gen_notes, \
             patch('tools.release_helper.github_release.create_app_release') as mock_create_release:
            
            # Setup mocks for successful app
            mock_find_target.return_value = "//demo/hello_fastapi:hello_fastapi_metadata"
            mock_get_metadata.return_value = {"domain": "demo", "name": "hello_fastapi"}
            mock_gen_notes.return_value = "Release notes content"
            mock_create_release.return_value = {"id": 123}
            
            results = create_releases_for_apps_with_notes(
                app_list=app_list,
                owner="test-owner",
                repo="test-repo",
                app_versions=app_versions
            )
            
            # Verify results
            assert "hello_fastapi" in results
            assert results["hello_fastapi"] is not None
            assert "missing_app" in results
            assert results["missing_app"] is None  # Should be None for missing version