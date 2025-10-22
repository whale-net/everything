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
import httpx

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
    
    def test_domain_app_format_passed_to_create_app_release(self):
        """Test that create_app_release receives app_name in domain-app format."""
        app_versions = {"hello_fastapi": "v0.0.8"}
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
            
            # Call the function
            results = create_releases_for_apps_with_notes(
                app_list=app_list,
                owner="test-owner",
                repo="test-repo",
                app_versions=app_versions
            )
            
            # Verify that create_app_release was called with domain-app format
            mock_create_release.assert_called_once()
            call_kwargs = mock_create_release.call_args[1]
            
            # The app_name parameter should be in domain-app format
            assert call_kwargs["app_name"] == "demo-hello_fastapi", \
                f"Expected app_name to be 'demo-hello_fastapi' but got '{call_kwargs['app_name']}'"
    
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
    
    def test_full_domain_app_format_in_app_list_and_versions(self):
        """Test that the function works when app_list and app_versions both use full domain-app format.
        
        This is the scenario that occurs when the workflow passes apps in full domain-app format
        and the MATRIX environment variable is parsed to use full names as keys.
        """
        # Simulate what the workflow and CLI do:
        # - app_list contains full domain-app names (from workflow: echo "$MATRIX" | jq -r '.include[] | "\(.domain)-\(.app)"')
        # - app_versions keys are also full domain-app names (from CLI parsing MATRIX)
        app_versions = {"friendly-computing-machine-migration": "v0.0.9"}
        app_list = ["friendly-computing-machine-migration"]
        
        # Mock the required functions
        with patch('tools.release_helper.github_release.find_app_bazel_target') as mock_find_target, \
             patch('tools.release_helper.github_release.get_app_metadata') as mock_get_metadata, \
             patch('tools.release_helper.github_release.generate_release_notes') as mock_gen_notes, \
             patch('tools.release_helper.github_release.create_app_release') as mock_create_release:
            
            # Setup mocks - find_app_bazel_target should accept full domain-app format
            mock_find_target.return_value = "//friendly_computing_machine:migration_metadata"
            mock_get_metadata.return_value = {"domain": "friendly-computing-machine", "name": "migration"}
            mock_gen_notes.return_value = "Release notes content"
            mock_create_release.return_value = {"id": 789, "tag_name": "friendly-computing-machine-migration.v0.0.9"}
            
            # Call the function
            results = create_releases_for_apps_with_notes(
                app_list=app_list,
                owner="test-owner",
                repo="test-repo",
                app_versions=app_versions
            )
            
            # Verify that the version was found and release was created
            assert "friendly-computing-machine-migration" in results
            assert results["friendly-computing-machine-migration"] is not None
            
            # Verify that create_app_release was called with domain-app format
            mock_create_release.assert_called_once()
            call_kwargs = mock_create_release.call_args[1]
            
            # The app_name parameter should be in domain-app format
            assert call_kwargs["app_name"] == "friendly-computing-machine-migration", \
                f"Expected app_name to be 'friendly-computing-machine-migration' but got '{call_kwargs['app_name']}'"
            
            # Verify that the tag_name uses the canonical format from metadata
            assert call_kwargs["tag_name"] == "friendly-computing-machine-migration.v0.0.9", \
                f"Expected tag_name to be 'friendly-computing-machine-migration.v0.0.9' but got '{call_kwargs['tag_name']}'"


class TestCreateReleasesForApps:
    """Test cases for create_releases_for_apps function."""
    
    def test_domain_app_format_passed_to_create_app_release(self):
        """Test that create_app_release receives app_name in domain-app format for create_releases_for_apps."""
        from tools.release_helper.github_release import create_releases_for_apps
        
        app_list = ["hello_python"]
        version = "v1.0.0"
        
        # Mock the required functions
        with patch('tools.release_helper.github_release.find_app_bazel_target') as mock_find_target, \
             patch('tools.release_helper.github_release.get_app_metadata') as mock_get_metadata, \
             patch('tools.release_helper.github_release.generate_release_notes') as mock_gen_notes, \
             patch('tools.release_helper.github_release.create_app_release') as mock_create_release:
            
            # Setup mocks
            mock_find_target.return_value = "//demo/hello_python:hello_python_metadata"
            mock_get_metadata.return_value = {"domain": "demo", "name": "hello_python"}
            mock_gen_notes.return_value = "Release notes content"
            mock_create_release.return_value = {"id": 456, "tag_name": "demo-hello_python.v1.0.0"}
            
            # Call the function
            results = create_releases_for_apps(
                app_list=app_list,
                version=version,
                owner="test-owner",
                repo="test-repo"
            )
            
            # Verify that create_app_release was called with domain-app format
            mock_create_release.assert_called_once()
            call_kwargs = mock_create_release.call_args[1]
            
            # The app_name parameter should be in domain-app format
            assert call_kwargs["app_name"] == "demo-hello_python", \
                f"Expected app_name to be 'demo-hello_python' but got '{call_kwargs['app_name']}'"


class TestGitHubReleaseClientTagVerification:
    """Test cases for GitHubReleaseClient tag verification."""
    
    def test_verify_tag_exists_success(self):
        """Test successful tag verification."""
        mock_response = Mock()
        mock_response.status_code = 200
        mock_response.json.return_value = {
            "ref": "refs/tags/demo-hello_python.v1.0.0",
            "object": {
                "sha": "abc123def456",
                "type": "commit"
            }
        }
        
        with patch('httpx.Client') as mock_client:
            mock_client.return_value.__enter__.return_value.get.return_value = mock_response
            
            client = GitHubReleaseClient("test-owner", "test-repo", "dummy-token")
            result = client.verify_tag_exists("demo-hello_python.v1.0.0")
            
            assert result is True
    
    def test_verify_tag_exists_with_commit_sha(self):
        """Test tag verification with commit SHA match."""
        # First response for the tag ref
        mock_ref_response = Mock()
        mock_ref_response.status_code = 200
        mock_ref_response.json.return_value = {
            "ref": "refs/tags/demo-hello_python.v1.0.0",
            "object": {
                "sha": "tag_object_sha",
                "type": "tag"  # Annotated tag
            }
        }
        
        # Second response for the tag object
        mock_tag_response = Mock()
        mock_tag_response.status_code = 200
        mock_tag_response.json.return_value = {
            "object": {
                "sha": "abc123def456789",
                "type": "commit"
            }
        }
        
        with patch('httpx.Client') as mock_client:
            mock_get = mock_client.return_value.__enter__.return_value.get
            mock_get.side_effect = [mock_ref_response, mock_tag_response]
            
            client = GitHubReleaseClient("test-owner", "test-repo", "dummy-token")
            result = client.verify_tag_exists("demo-hello_python.v1.0.0", "abc123def456789")
            
            assert result is True
    
    def test_verify_tag_exists_commit_mismatch(self):
        """Test tag verification with commit SHA mismatch."""
        mock_response = Mock()
        mock_response.status_code = 200
        mock_response.json.return_value = {
            "ref": "refs/tags/demo-hello_python.v1.0.0",
            "object": {
                "sha": "xyz789",
                "type": "commit"
            }
        }
        
        with patch('httpx.Client') as mock_client:
            mock_client.return_value.__enter__.return_value.get.return_value = mock_response
            
            client = GitHubReleaseClient("test-owner", "test-repo", "dummy-token")
            result = client.verify_tag_exists("demo-hello_python.v1.0.0", "abc123")
            
            assert result is False
    
    def test_verify_tag_exists_not_found_with_retry(self):
        """Test tag verification when tag doesn't exist after retries."""
        mock_response = Mock()
        mock_response.status_code = 404
        
        with patch('httpx.Client') as mock_client, \
             patch('time.sleep'):  # Mock sleep to speed up test
            mock_client.return_value.__enter__.return_value.get.return_value = mock_response
            
            client = GitHubReleaseClient("test-owner", "test-repo", "dummy-token")
            result = client.verify_tag_exists("demo-hello_python.v1.0.0", max_retries=2)
            
            assert result is False
    
    def test_verify_tag_exists_retry_then_success(self):
        """Test tag verification that succeeds after retries."""
        # First call returns 404, second call succeeds
        mock_404 = Mock()
        mock_404.status_code = 404
        
        mock_200 = Mock()
        mock_200.status_code = 200
        mock_200.json.return_value = {
            "ref": "refs/tags/demo-hello_python.v1.0.0",
            "object": {
                "sha": "abc123def456",
                "type": "commit"
            }
        }
        
        with patch('httpx.Client') as mock_client, \
             patch('time.sleep'):  # Mock sleep to speed up test
            mock_get = mock_client.return_value.__enter__.return_value.get
            mock_get.side_effect = [mock_404, mock_200]
            
            client = GitHubReleaseClient("test-owner", "test-repo", "dummy-token")
            result = client.verify_tag_exists("demo-hello_python.v1.0.0", max_retries=3)
            
            assert result is True


class TestOpenAPISpecValidation:
    """Test cases for OpenAPI spec validation during release creation."""
    
    def test_release_succeeds_with_warning_when_expected_openapi_spec_missing(self):
        """Test that release succeeds with warning when an app expects OpenAPI spec but it's missing."""
        app_list = ["hello-fastapi"]
        
        # Create a temp directory for specs (but don't put the spec file there)
        import tempfile
        with tempfile.TemporaryDirectory() as tmpdir:
            # Mock the required functions
            with patch('tools.release_helper.github_release.find_app_bazel_target') as mock_find_target, \
                 patch('tools.release_helper.github_release.get_app_metadata') as mock_get_metadata, \
                 patch('tools.release_helper.github_release.generate_release_notes') as mock_gen_notes, \
                 patch('tools.release_helper.github_release.create_app_release') as mock_create_release:
                
                # Setup mocks
                mock_find_target.return_value = "//demo/hello-fastapi:hello-fastapi_metadata"
                # App has openapi_spec_target, indicating it should have an OpenAPI spec
                mock_get_metadata.return_value = {
                    "domain": "demo",
                    "name": "hello-fastapi",
                    "openapi_spec_target": "//demo/hello-fastapi:hello-fastapi_openapi_spec"
                }
                mock_gen_notes.return_value = "Release notes content"
                mock_create_release.return_value = {"id": 123, "tag_name": "demo-hello-fastapi.v1.0.0"}
                
                # Call the function with openapi_specs_dir pointing to empty directory
                results = create_releases_for_apps_with_notes(
                    app_list=app_list,
                    version="v1.0.0",
                    owner="test-owner",
                    repo="test-repo",
                    openapi_specs_dir=tmpdir
                )
                
                # Verify the release succeeded even though spec was missing
                assert "hello-fastapi" in results
                assert results["hello-fastapi"] is not None
                assert results["hello-fastapi"]["id"] == 123
                
                # Verify that create_app_release was called with warning in release notes
                assert mock_create_release.call_count == 1
                call_kwargs = mock_create_release.call_args[1]
                release_notes = call_kwargs['release_notes']
                
                # Check that warning was added to release notes
                assert "⚠️ **Warning: OpenAPI Specification Missing**" in release_notes
                assert "//demo/hello-fastapi:hello-fastapi_openapi_spec" in release_notes
                assert "Release notes content" in release_notes  # Original notes should still be there
    
    def test_release_succeeds_when_expected_openapi_spec_present(self):
        """Test that release succeeds when expected OpenAPI spec is present."""
        app_list = ["hello-fastapi"]
        
        import tempfile
        from pathlib import Path
        with tempfile.TemporaryDirectory() as tmpdir:
            # Create the expected OpenAPI spec file
            spec_file = Path(tmpdir) / "demo-hello-fastapi-openapi.json"
            spec_file.write_text('{"openapi": "3.0.0"}')
            
            # Mock the required functions
            with patch('tools.release_helper.github_release.find_app_bazel_target') as mock_find_target, \
                 patch('tools.release_helper.github_release.get_app_metadata') as mock_get_metadata, \
                 patch('tools.release_helper.github_release.generate_release_notes') as mock_gen_notes, \
                 patch('tools.release_helper.github_release.create_app_release') as mock_create_release, \
                 patch('tools.release_helper.github_release.GitHubReleaseClient') as mock_client_class:
                
                # Setup mocks
                mock_find_target.return_value = "//demo/hello-fastapi:hello-fastapi_metadata"
                mock_get_metadata.return_value = {
                    "domain": "demo",
                    "name": "hello-fastapi",
                    "openapi_spec_target": "//demo/hello-fastapi:hello-fastapi_openapi_spec"
                }
                mock_gen_notes.return_value = "Release notes content"
                mock_create_release.return_value = {"id": 123, "tag_name": "demo-hello-fastapi.v1.0.0"}
                
                # Mock the client for asset upload
                mock_client = Mock()
                mock_client.upload_release_asset = Mock()
                mock_client_class.return_value = mock_client
                
                # Call the function
                results = create_releases_for_apps_with_notes(
                    app_list=app_list,
                    version="v1.0.0",
                    owner="test-owner",
                    repo="test-repo",
                    token="dummy-token",
                    openapi_specs_dir=tmpdir
                )
                
                # Verify the release succeeded
                assert "hello-fastapi" in results
                assert results["hello-fastapi"] is not None
                assert results["hello-fastapi"]["id"] == 123
                
                # Verify asset upload was attempted
                mock_client.upload_release_asset.assert_called_once()
    
    def test_release_succeeds_when_no_openapi_spec_expected_or_present(self):
        """Test that release succeeds when app doesn't expect OpenAPI spec."""
        app_list = ["hello-python"]
        
        import tempfile
        with tempfile.TemporaryDirectory() as tmpdir:
            # Mock the required functions
            with patch('tools.release_helper.github_release.find_app_bazel_target') as mock_find_target, \
                 patch('tools.release_helper.github_release.get_app_metadata') as mock_get_metadata, \
                 patch('tools.release_helper.github_release.generate_release_notes') as mock_gen_notes, \
                 patch('tools.release_helper.github_release.create_app_release') as mock_create_release:
                
                # Setup mocks - note: no openapi_spec_target in metadata
                mock_find_target.return_value = "//demo/hello-python:hello-python_metadata"
                mock_get_metadata.return_value = {
                    "domain": "demo",
                    "name": "hello-python"
                    # No openapi_spec_target
                }
                mock_gen_notes.return_value = "Release notes content"
                mock_create_release.return_value = {"id": 456, "tag_name": "demo-hello-python.v1.0.0"}
                
                # Call the function
                results = create_releases_for_apps_with_notes(
                    app_list=app_list,
                    version="v1.0.0",
                    owner="test-owner",
                    repo="test-repo",
                    openapi_specs_dir=tmpdir
                )
                
                # Verify the release succeeded
                assert "hello-python" in results
                assert results["hello-python"] is not None
                assert results["hello-python"]["id"] == 456


class TestGitHubReleaseClientDeletion:
    """Test cases for GitHubReleaseClient deletion operations."""

    @pytest.fixture
    def mock_token(self):
        """Provide a mock GitHub token."""
        return "ghp_test_token_1234567890"

    @pytest.fixture
    def client(self, mock_token):
        """Create a GitHubReleaseClient instance for testing."""
        return GitHubReleaseClient(owner="test-owner", repo="test-repo", token=mock_token)

    @pytest.fixture
    def mock_httpx_client(self):
        """Create a mock httpx.Client with proper context manager support."""
        mock_client = MagicMock()
        mock_client.__enter__ = MagicMock(return_value=mock_client)
        mock_client.__exit__ = MagicMock(return_value=None)
        return mock_client

    def test_delete_release_success(self, client, mock_httpx_client):
        """Test successfully deleting a release by ID."""
        mock_response = Mock()
        mock_response.status_code = 204
        mock_httpx_client.delete.return_value = mock_response

        with patch("httpx.Client", return_value=mock_httpx_client):
            result = client.delete_release(12345)

        assert result is True
        mock_httpx_client.delete.assert_called_once()
        call_args = mock_httpx_client.delete.call_args
        # Verify URL contains release ID
        assert "/releases/12345" in str(call_args)

    def test_delete_release_not_found(self, client, mock_httpx_client):
        """Test deleting a release that doesn't exist returns False."""
        mock_response = Mock()
        mock_response.status_code = 404
        mock_httpx_client.delete.return_value = mock_response

        with patch("httpx.Client", return_value=mock_httpx_client):
            result = client.delete_release(12345)

        assert result is False

    def test_delete_release_permission_denied(self, client, mock_httpx_client):
        """Test deleting a release without permission raises error."""
        mock_response = Mock()
        mock_response.status_code = 403
        mock_response.raise_for_status.side_effect = httpx.HTTPStatusError(
            "Forbidden", request=Mock(), response=mock_response
        )
        mock_httpx_client.delete.return_value = mock_response

        with patch("httpx.Client", return_value=mock_httpx_client):
            with pytest.raises(httpx.HTTPStatusError):
                client.delete_release(12345)

    def test_delete_release_by_tag_success(self, client, mock_httpx_client):
        """Test deleting a release by tag name."""
        # Mock get_release_by_tag
        mock_get_response = Mock()
        mock_get_response.status_code = 200
        mock_get_response.json.return_value = {
            "id": 12345,
            "tag_name": "v1.0.0",
            "name": "Release v1.0.0"
        }
        
        # Mock delete
        mock_delete_response = Mock()
        mock_delete_response.status_code = 204
        
        mock_httpx_client.get.return_value = mock_get_response
        mock_httpx_client.delete.return_value = mock_delete_response

        with patch("httpx.Client", return_value=mock_httpx_client):
            result = client.delete_release_by_tag("v1.0.0")

        assert result is True
        # Should have called get and delete
        assert mock_httpx_client.get.call_count == 1
        assert mock_httpx_client.delete.call_count == 1

    def test_delete_release_by_tag_not_found(self, client, mock_httpx_client):
        """Test deleting a release by tag when release doesn't exist."""
        mock_response = Mock()
        mock_response.status_code = 404
        mock_httpx_client.get.return_value = mock_response

        with patch("httpx.Client", return_value=mock_httpx_client):
            result = client.delete_release_by_tag("v1.0.0")

        assert result is False
        # Should only call get, not delete
        mock_httpx_client.get.assert_called_once()
        mock_httpx_client.delete.assert_not_called()

    def test_find_releases_by_tags(self, client, mock_httpx_client):
        """Test finding releases by tag names."""
        mock_response = Mock()
        mock_response.status_code = 200
        mock_response.json.return_value = [
            {"id": 1, "tag_name": "v1.0.0", "name": "Release 1.0.0"},
            {"id": 2, "tag_name": "v1.1.0", "name": "Release 1.1.0"},
            {"id": 3, "tag_name": "v2.0.0", "name": "Release 2.0.0"},
        ]
        # Prevent pagination
        mock_response.headers = {}
        mock_httpx_client.get.return_value = mock_response

        with patch("httpx.Client", return_value=mock_httpx_client):
            releases = client.find_releases_by_tags(["v1.0.0", "v2.0.0"])

        assert len(releases) == 2
        assert "v1.0.0" in releases
        assert "v2.0.0" in releases
        assert "v1.1.0" not in releases
        assert releases["v1.0.0"]["id"] == 1

    def test_find_releases_by_tags_handles_none_values(self, client, mock_httpx_client):
        """Test finding releases handles None values and invalid releases gracefully."""
        mock_response = Mock()
        mock_response.status_code = 200
        mock_response.json.return_value = [
            None,  # None value in the list
            {"id": 1, "tag_name": "v1.0.0", "name": "Release 1.0.0"},
            "invalid_string",  # Invalid non-dict value
            {"id": 2, "tag_name": "v1.1.0", "name": "Release 1.1.0"},
            None,  # Another None
            {"id": 3, "tag_name": "v2.0.0", "name": "Release 2.0.0"},
        ]
        # Prevent pagination
        mock_response.headers = {}
        mock_httpx_client.get.return_value = mock_response

        with patch("httpx.Client", return_value=mock_httpx_client):
            releases = client.find_releases_by_tags(["v1.0.0", "v2.0.0"])

        # Should successfully filter out None and invalid values
        assert len(releases) == 2
        assert "v1.0.0" in releases
        assert "v2.0.0" in releases
        assert releases["v1.0.0"]["id"] == 1
        assert releases["v2.0.0"]["id"] == 3
        assert releases["v2.0.0"]["id"] == 3