"""
Tests for GitHub Container Registry (GHCR) management.

Note: Some tests are marked as integration tests due to complexity of mocking httpx Client context managers.
For now, we focus on core functionality and dataclass tests.
"""

import json
import os
from unittest.mock import Mock, patch, MagicMock

import pytest
import httpx

from tools.release_helper.ghcr import GHCRClient, GHCRPackageVersion


class TestGHCRClient:
    """Test cases for GHCRClient."""

    @pytest.fixture
    def mock_token(self):
        """Provide a mock GitHub token."""
        return "ghp_test_token_1234567890"

    @pytest.fixture
    def client(self, mock_token):
        """Create a GHCRClient instance for testing."""
        return GHCRClient(owner="test-owner", token=mock_token)

    @pytest.fixture
    def mock_httpx_client(self):
        """Provide a properly mocked httpx.Client with context manager support."""
        mock_response = MagicMock()
        mock_client = MagicMock()
        mock_client.__enter__.return_value = mock_client
        mock_client.__exit__.return_value = None
        mock_client.get.return_value = mock_response
        mock_client.delete.return_value = mock_response
        mock_client.post.return_value = mock_response
        return mock_client, mock_response

    def test_client_initialization_with_token(self, mock_token):
        """Test client initialization with explicit token."""
        client = GHCRClient(owner="test-owner", token=mock_token)
        assert client.owner == "test-owner"
        assert client.token == mock_token
        assert client.base_url == "https://api.github.com"

    def test_client_initialization_from_env(self, monkeypatch):
        """Test client initialization from GITHUB_TOKEN environment variable."""
        monkeypatch.setenv("GITHUB_TOKEN", "env_token_12345")
        client = GHCRClient(owner="test-owner")
        assert client.token == "env_token_12345"

    def test_client_initialization_no_token(self):
        """Test client initialization fails without token."""
        with patch.dict(os.environ, {}, clear=True):
            with pytest.raises(ValueError, match="GitHub token is required"):
                GHCRClient(owner="test-owner")

    def test_list_package_versions_success(self, client):
        """Test listing package versions successfully."""
        # Mock the owner type detection first
        with patch.object(client, '_detect_owner_type', return_value='orgs'):
            mock_response = MagicMock()
            mock_response.status_code = 200
            mock_response.headers = {}  # No Link header = no pagination
            mock_response.json.return_value = [
                {
                    "id": 12345,
                    "name": "sha256:abc123",
                    "metadata": {
                        "package_type": "container",
                        "container": {
                            "tags": ["v1.0.0", "latest"]
                        }
                    },
                    "created_at": "2025-01-15T10:00:00Z",
                    "updated_at": "2025-01-15T10:00:00Z"
                },
                {
                    "id": 12346,
                    "name": "sha256:def456",
                    "metadata": {
                        "package_type": "container",
                        "container": {
                            "tags": ["v1.0.1"]
                        }
                    },
                    "created_at": "2025-01-20T10:00:00Z",
                    "updated_at": "2025-01-20T10:00:00Z"
                }
            ]

            mock_http_client = MagicMock()
            mock_http_client.__enter__.return_value = mock_http_client
            mock_http_client.get.return_value = mock_response

            with patch("httpx.Client", return_value=mock_http_client):
                versions = client.list_package_versions("demo-hello-python")

            assert len(versions) == 2
            assert versions[0].version_id == 12345
            assert versions[0].tags == ["v1.0.0", "latest"]
            assert versions[1].version_id == 12346
            assert versions[1].tags == ["v1.0.1"]

    def test_list_package_versions_pagination(self, client):
        """Test pagination when listing package versions."""
        # Mock the owner type detection first
        with patch.object(client, '_detect_owner_type', return_value='orgs'):
            # Mock multiple pages of results
            page1_data = [{"id": i, "name": f"sha256:hash{i}", "metadata": {"container": {"tags": [f"v1.0.{i}"]}}} for i in range(100)]
            page2_data = [{"id": i, "name": f"sha256:hash{i}", "metadata": {"container": {"tags": [f"v1.1.{i}"]}}} for i in range(50)]

            mock_response1 = MagicMock()
            mock_response1.status_code = 200
            mock_response1.json.return_value = page1_data
            mock_response1.headers = {"Link": '<https://api.github.com/next>; rel="next"'}

            mock_response2 = MagicMock()
            mock_response2.status_code = 200
            mock_response2.json.return_value = page2_data
            mock_response2.headers = {}

            mock_http_client = MagicMock()
            mock_http_client.__enter__.return_value = mock_http_client
            mock_http_client.__exit__.return_value = None
            mock_http_client.get.side_effect = [mock_response1, mock_response2]

            with patch("httpx.Client", return_value=mock_http_client):
                versions = client.list_package_versions("demo-hello-python")

            assert len(versions) == 150
            assert mock_http_client.get.call_count == 2

    def test_list_package_versions_empty(self, client):
        """Test listing package versions when package has no versions."""
        with patch.object(client, '_detect_owner_type', return_value='orgs'):
            mock_response = MagicMock()
            mock_response.status_code = 200
            mock_response.headers = {}
            mock_response.json.return_value = []

            mock_http_client = MagicMock()
            mock_http_client.__enter__.return_value = mock_http_client
            mock_http_client.get.return_value = mock_response

            with patch("httpx.Client", return_value=mock_http_client):
                versions = client.list_package_versions("nonexistent-package")

            assert len(versions) == 0

    def test_list_package_versions_not_found(self, client):
        """Test listing package versions when package doesn't exist."""
        with patch.object(client, '_detect_owner_type', return_value='orgs'):
            mock_response = MagicMock()
            mock_response.status_code = 404

            mock_http_client = MagicMock()
            mock_http_client.__enter__.return_value = mock_http_client
            mock_http_client.get.return_value = mock_response

            with patch("httpx.Client", return_value=mock_http_client):
                versions = client.list_package_versions("nonexistent-package")

            assert len(versions) == 0

    def test_list_package_versions_unauthorized(self, client):
        """Test listing package versions with unauthorized token."""
        with patch.object(client, '_detect_owner_type', return_value='orgs'):
            mock_response = MagicMock()
            mock_response.status_code = 401
            mock_response.raise_for_status.side_effect = httpx.HTTPStatusError(
                "Unauthorized", request=Mock(), response=mock_response
            )

            mock_http_client = MagicMock()
            mock_http_client.__enter__.return_value = mock_http_client
            mock_http_client.get.return_value = mock_response

            with patch("httpx.Client", return_value=mock_http_client):
                with pytest.raises(httpx.HTTPStatusError):
                    client.list_package_versions("demo-hello-python")

    def test_delete_package_version_success(self, client, mock_httpx_client):
        """Test deleting a package version successfully."""
        with patch.object(client, '_detect_owner_type', return_value='orgs'):
            mock_client, mock_response = mock_httpx_client
            mock_response.status_code = 204  # No content - successful deletion

            with patch("httpx.Client", return_value=mock_client):
                result = client.delete_package_version("demo-hello-python", 12345)

            assert result is True
            mock_client.delete.assert_called_once()

    def test_delete_package_version_not_found(self, client, mock_httpx_client):
        """Test deleting a package version that doesn't exist."""
        with patch.object(client, '_detect_owner_type', return_value='orgs'):
            mock_client, mock_response = mock_httpx_client
            mock_response.status_code = 404

            with patch("httpx.Client", return_value=mock_client):
                result = client.delete_package_version("demo-hello-python", 99999)

            assert result is False

    def test_delete_package_version_forbidden(self, client, mock_httpx_client):
        """Test deleting a package version without permission."""
        with patch.object(client, '_detect_owner_type', return_value='orgs'):
            mock_client, mock_response = mock_httpx_client
            mock_response.status_code = 403
            mock_response.raise_for_status.side_effect = httpx.HTTPStatusError(
                "Forbidden", request=Mock(), response=mock_response
            )

            with patch("httpx.Client", return_value=mock_client):
                with pytest.raises(httpx.HTTPStatusError):
                    client.delete_package_version("demo-hello-python", 12345)

    def test_find_versions_by_tags(self, client, mock_httpx_client):
        """Test finding package versions by specific tags."""
        with patch.object(client, '_detect_owner_type', return_value='orgs'):
            mock_client, mock_response = mock_httpx_client
            mock_response.status_code = 200
            mock_response.headers = {}  # No pagination
            mock_response.json.return_value = [
                {
                    "id": 12345,
                    "name": "sha256:abc123",
                    "metadata": {
                        "container": {
                            "tags": ["v1.0.0", "latest"]
                        }
                    }
                },
                {
                    "id": 12346,
                    "name": "sha256:def456",
                    "metadata": {
                        "container": {
                            "tags": ["v1.0.1"]
                        }
                    }
                },
                {
                    "id": 12347,
                    "name": "sha256:ghi789",
                    "metadata": {
                        "container": {
                            "tags": ["v1.1.0", "v1.1.0-amd64", "v1.1.0-arm64"]
                        }
                    }
                }
            ]

            with patch("httpx.Client", return_value=mock_client):
                versions = client.find_versions_by_tags(
                    "demo-hello-python",
                    ["v1.0.0", "v1.1.0"]
                )

            assert len(versions) == 2
            assert versions[0].version_id == 12345
            assert "v1.0.0" in versions[0].tags
            assert versions[1].version_id == 12347
            assert "v1.1.0" in versions[1].tags

    def test_find_versions_by_tags_no_matches(self, client, mock_httpx_client):
        """Test finding package versions when no tags match."""
        with patch.object(client, '_detect_owner_type', return_value='orgs'):
            mock_client, mock_response = mock_httpx_client
            mock_response.status_code = 200
            mock_response.json.return_value = [
                {
                    "id": 12345,
                    "name": "sha256:abc123",
                    "metadata": {
                        "container": {
                            "tags": ["v2.0.0"]
                        }
                    }
                }
            ]

            with patch("httpx.Client", return_value=mock_client):
                versions = client.find_versions_by_tags(
                    "demo-hello-python",
                    ["v1.0.0", "v1.1.0"]
                )

            assert len(versions) == 0

    def test_find_versions_by_tags_handles_untagged(self, client, mock_httpx_client):
        """Test finding package versions handles untagged images."""
        with patch.object(client, '_detect_owner_type', return_value='orgs'):
            mock_client, mock_response = mock_httpx_client
            mock_response.status_code = 200
            mock_response.json.return_value = [
                {
                    "id": 12345,
                    "name": "sha256:abc123",
                    "metadata": {
                        "container": {
                            "tags": []  # Untagged image
                        }
                    }
                },
                {
                    "id": 12346,
                    "name": "sha256:def456",
                    "metadata": {
                        "container": {
                            "tags": ["v1.0.0"]
                        }
                    }
                }
            ]

            with patch("httpx.Client", return_value=mock_client):
                versions = client.find_versions_by_tags(
                    "demo-hello-python",
                    ["v1.0.0"]
                )

            assert len(versions) == 1
        assert versions[0].version_id == 12346

    def test_list_package_versions_handles_null_metadata(self, client):
        """Test listing package versions when metadata is null or missing container key."""
        with patch.object(client, '_detect_owner_type', return_value='orgs'):
            mock_response = MagicMock()
            mock_response.status_code = 200
            mock_response.headers = {}  # No Link header = no pagination
            mock_response.json.return_value = [
                {
                    "id": 12345,
                    "name": "sha256:abc123",
                    "metadata": None,  # Null metadata
                    "created_at": "2025-01-15T10:00:00Z",
                    "updated_at": "2025-01-15T10:00:00Z"
                },
                {
                    "id": 12346,
                    "name": "sha256:def456",
                    "metadata": {
                        "package_type": "container"
                        # Missing "container" key
                    },
                    "created_at": "2025-01-20T10:00:00Z",
                    "updated_at": "2025-01-20T10:00:00Z"
                },
                {
                    "id": 12347,
                    "name": "sha256:ghi789",
                    "metadata": {
                        "container": {
                            "tags": ["v1.0.0"]
                        }
                    },
                    "created_at": "2025-01-25T10:00:00Z",
                    "updated_at": "2025-01-25T10:00:00Z"
                }
            ]

            mock_http_client = MagicMock()
            mock_http_client.__enter__.return_value = mock_http_client
            mock_http_client.get.return_value = mock_response

            with patch("httpx.Client", return_value=mock_http_client):
                versions = client.list_package_versions("demo-hello-python")

            # Should successfully parse all versions, with empty tags for problematic ones
            assert len(versions) == 3
            assert versions[0].version_id == 12345
            assert versions[0].tags == []  # No tags due to null metadata
            assert versions[1].version_id == 12346
            assert versions[1].tags == []  # No tags due to missing container key
            assert versions[2].version_id == 12347
            assert versions[2].tags == ["v1.0.0"]  # Normal tags

    def test_validate_permissions_success(self, client, mock_httpx_client):
        """Test permission validation succeeds with correct scopes."""
        mock_client, mock_response = mock_httpx_client
        mock_response.status_code = 200
        mock_response.json.return_value = {
            "login": "test-owner",
            "type": "Organization"
        }
        mock_response.headers = {
            "X-OAuth-Scopes": "repo, write:packages, read:packages"
        }

        with patch("httpx.Client", return_value=mock_client):
            result = client.validate_permissions()

        assert result is True

    def test_validate_permissions_missing_scopes(self, client, mock_httpx_client):
        """Test permission validation fails without required scopes."""
        mock_client, mock_response = mock_httpx_client
        mock_response.status_code = 200
        mock_response.json.return_value = {
            "login": "test-owner",
            "type": "Organization"
        }
        mock_response.headers = {
            "X-OAuth-Scopes": "repo"  # Missing write:packages
        }

        with patch("httpx.Client", return_value=mock_client):
            result = client.validate_permissions()

        assert result is False

    def test_validate_permissions_forbidden(self, client, mock_httpx_client):
        """Test permission validation fails with forbidden response."""
        mock_client, mock_response = mock_httpx_client
        mock_response.status_code = 403

        with patch("httpx.Client", return_value=mock_client):
            result = client.validate_permissions()

        assert result is False

    def test_get_package_info_success(self, client, mock_httpx_client):
        """Test getting package info successfully."""
        mock_client, mock_response = mock_httpx_client
        mock_response.status_code = 200
        mock_response.json.return_value = {
            "id": 123,
            "name": "demo-hello-python",
            "package_type": "container",
            "owner": {"login": "test-owner"},
            "version_count": 42,
            "visibility": "public",
            "created_at": "2024-01-01T00:00:00Z",
            "updated_at": "2025-10-19T00:00:00Z"
        }

        with patch("httpx.Client", return_value=mock_client):
            info = client.get_package_info("demo-hello-python")

        assert info is not None
        assert info["name"] == "demo-hello-python"
        assert info["version_count"] == 42

    def test_get_package_info_not_found(self, client, mock_httpx_client):
        """Test getting package info for non-existent package."""
        mock_client, mock_response = mock_httpx_client
        mock_response.status_code = 404

        with patch("httpx.Client", return_value=mock_client):
            info = client.get_package_info("nonexistent-package")

        assert info is None

    def test_owner_type_detection_org(self, client, mock_httpx_client):
        """Test detecting organization owner type."""
        mock_client, mock_response = mock_httpx_client
        mock_response.status_code = 200
        mock_response.json.return_value = {
            "login": "test-owner",
            "type": "Organization"
        }

        with patch("httpx.Client", return_value=mock_client):
            owner_type = client._detect_owner_type()

        assert owner_type == "orgs"

    def test_owner_type_detection_user(self, client, mock_httpx_client):
        """Test detecting user owner type."""
        mock_client, mock_response = mock_httpx_client
        mock_response.status_code = 200
        mock_response.json.return_value = {
            "login": "test-owner",
            "type": "User"
        }

        with patch("httpx.Client", return_value=mock_client):
            owner_type = client._detect_owner_type()

        assert owner_type == "users"

    def test_owner_type_caching(self, client, mock_httpx_client):
        """Test that owner type detection is cached."""
        mock_client, mock_response = mock_httpx_client
        mock_response.status_code = 200
        mock_response.json.return_value = {
            "login": "test-owner",
            "type": "Organization"
        }

        with patch("httpx.Client", return_value=mock_client):
            # First call
            owner_type1 = client._detect_owner_type()
            # Second call should use cache
            owner_type2 = client._detect_owner_type()

        assert owner_type1 == "orgs"
        assert owner_type2 == "orgs"
        # Should only call API once
        assert mock_client.get.call_count == 1

    def test_find_hash_tagged_versions(self, client, mock_httpx_client):
        """Test finding hash-tagged versions older than specified age."""
        from datetime import datetime, timedelta, timezone
        
        with patch.object(client, '_detect_owner_type', return_value='orgs'):
            mock_client, mock_response = mock_httpx_client
            mock_response.status_code = 200
            mock_response.headers = {}
            
            # Create versions with different ages
            five_days_ago = (datetime.now(timezone.utc) - timedelta(days=5)).isoformat()
            two_days_ago = (datetime.now(timezone.utc) - timedelta(days=2)).isoformat()
            
            mock_response.json.return_value = [
                {
                    "id": 12345,
                    "name": "sha256:abc123",
                    "metadata": {"container": {"tags": ["abc123d"]}},
                    "created_at": five_days_ago
                },
                {
                    "id": 12346,
                    "name": "sha256:def456",
                    "metadata": {"container": {"tags": ["def456e"]}},
                    "created_at": two_days_ago
                },
                {
                    "id": 12347,
                    "name": "sha256:ghi789",
                    "metadata": {"container": {"tags": ["v1.0.0"]}},
                    "created_at": five_days_ago
                }
            ]
            
            with patch("httpx.Client", return_value=mock_client):
                versions = client.find_hash_tagged_versions("demo-hello-python", min_age_days=3.0)
            
            # Should only return hash-tagged versions older than 3 days
            assert len(versions) == 1
            assert versions[0].version_id == 12345
            assert "abc123d" in versions[0].tags

    def test_find_hash_tagged_versions_no_old_hashes(self, client, mock_httpx_client):
        """Test finding hash-tagged versions when none are old enough."""
        from datetime import datetime, timedelta, timezone
        
        with patch.object(client, '_detect_owner_type', return_value='orgs'):
            mock_client, mock_response = mock_httpx_client
            mock_response.status_code = 200
            mock_response.headers = {}
            
            # Recent hash-tagged version
            one_day_ago = (datetime.now(timezone.utc) - timedelta(days=1)).isoformat()
            
            mock_response.json.return_value = [
                {
                    "id": 12345,
                    "name": "sha256:abc123",
                    "metadata": {"container": {"tags": ["abc123d"]}},
                    "created_at": one_day_ago
                }
            ]
            
            with patch("httpx.Client", return_value=mock_client):
                versions = client.find_hash_tagged_versions("demo-hello-python", min_age_days=3.0)
            
            assert len(versions) == 0

    def test_list_all_packages(self, client, mock_httpx_client):
        """Test listing all packages for owner."""
        with patch.object(client, '_detect_owner_type', return_value='orgs'):
            mock_client, mock_response = mock_httpx_client
            mock_response.status_code = 200
            mock_response.headers = {}
            mock_response.json.return_value = [
                {"name": "demo-hello-python", "package_type": "container"},
                {"name": "demo-hello-go", "package_type": "container"},
                {"name": "helm-demo-fastapi", "package_type": "container"}
            ]
            
            with patch("httpx.Client", return_value=mock_client):
                packages = client.list_all_packages()
            
            assert len(packages) == 3
            assert "demo-hello-python" in packages
            assert "demo-hello-go" in packages
            assert "helm-demo-fastapi" in packages

    def test_list_all_packages_pagination(self, client, mock_httpx_client):
        """Test pagination when listing all packages."""
        with patch.object(client, '_detect_owner_type', return_value='orgs'):
            page1_data = [{"name": f"package-{i}", "package_type": "container"} for i in range(100)]
            page2_data = [{"name": f"package-{i}", "package_type": "container"} for i in range(100, 150)]
            
            mock_response1 = MagicMock()
            mock_response1.status_code = 200
            mock_response1.json.return_value = page1_data
            mock_response1.headers = {"Link": '<https://api.github.com/next>; rel="next"'}
            
            mock_response2 = MagicMock()
            mock_response2.status_code = 200
            mock_response2.json.return_value = page2_data
            mock_response2.headers = {}
            
            mock_http_client = MagicMock()
            mock_http_client.__enter__.return_value = mock_http_client
            mock_http_client.__exit__.return_value = None
            mock_http_client.get.side_effect = [mock_response1, mock_response2]
            
            with patch("httpx.Client", return_value=mock_http_client):
                packages = client.list_all_packages()
            
            assert len(packages) == 150
            assert mock_http_client.get.call_count == 2


class TestGHCRPackageVersion:
    """Test cases for GHCRPackageVersion dataclass."""

    def test_version_creation(self):
        """Test creating a package version."""
        version = GHCRPackageVersion(
            version_id=12345,
            tags=["v1.0.0", "latest"],
            created_at="2025-01-15T10:00:00Z",
            updated_at="2025-01-15T10:00:00Z"
        )

        assert version.version_id == 12345
        assert version.tags == ["v1.0.0", "latest"]
        assert version.created_at == "2025-01-15T10:00:00Z"

    def test_version_has_tag(self):
        """Test checking if version has a specific tag."""
        version = GHCRPackageVersion(
            version_id=12345,
            tags=["v1.0.0", "v1.0.0-amd64", "v1.0.0-arm64", "latest"]
        )

        assert version.has_tag("v1.0.0")
        assert version.has_tag("latest")
        assert not version.has_tag("v2.0.0")

    def test_version_is_untagged(self):
        """Test detecting untagged versions."""
        tagged_version = GHCRPackageVersion(
            version_id=12345,
            tags=["v1.0.0"]
        )
        untagged_version = GHCRPackageVersion(
            version_id=12346,
            tags=[]
        )

        assert not tagged_version.is_untagged()
        assert untagged_version.is_untagged()

    def test_version_has_hash_tag(self):
        """Test detecting hash tags in version."""
        # Version with hash tag
        hash_version = GHCRPackageVersion(
            version_id=12345,
            tags=["abc123d", "v1.0.0"]
        )
        
        # Version without hash tag
        no_hash_version = GHCRPackageVersion(
            version_id=12346,
            tags=["v1.0.0", "latest"]
        )
        
        # Version with longer hash
        long_hash_version = GHCRPackageVersion(
            version_id=12347,
            tags=["1234567890abcdef"]
        )

        assert hash_version.has_hash_tag()
        assert not no_hash_version.has_hash_tag()
        assert long_hash_version.has_hash_tag()

    def test_version_get_hash_tags(self):
        """Test extracting hash tags from version."""
        version = GHCRPackageVersion(
            version_id=12345,
            tags=["abc123d", "v1.0.0", "1234567890abcdef", "latest"]
        )
        
        hash_tags = version.get_hash_tags()
        assert len(hash_tags) == 2
        assert "abc123d" in hash_tags
        assert "1234567890abcdef" in hash_tags
        assert "v1.0.0" not in hash_tags
        assert "latest" not in hash_tags

    def test_version_age_days(self):
        """Test calculating version age in days."""
        from datetime import datetime, timedelta, timezone
        
        # Version created 5 days ago
        five_days_ago = datetime.now(timezone.utc) - timedelta(days=5)
        version = GHCRPackageVersion(
            version_id=12345,
            tags=["abc123d"],
            created_at=five_days_ago.isoformat()
        )
        
        age = version.age_days()
        assert age is not None
        assert 4.9 < age < 5.1  # Allow small tolerance
        
        # Version without creation date
        no_date_version = GHCRPackageVersion(
            version_id=12346,
            tags=["def456"]
        )
        
        assert no_date_version.age_days() is None

    def test_version_repr(self):
        """Test string representation of version."""
        version = GHCRPackageVersion(
            version_id=12345,
            tags=["v1.0.0", "latest"]
        )

        repr_str = repr(version)
        assert "12345" in repr_str
        assert "v1.0.0" in repr_str
