"""
Unit tests for the github_release portion of the release helper.

This module provides comprehensive unit tests for the github_release.py module,
focusing on the permission validation logic that was updated to handle GitHub Actions tokens.
"""

import os
import pytest
from unittest.mock import Mock, patch, MagicMock
import sys

# Add the parent directory to path for imports
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

try:
    from tools.release_helper.github_release import GitHubReleaseClient
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


if __name__ == "__main__":
    # Run tests if executed directly
    pytest.main([__file__, "-v"])