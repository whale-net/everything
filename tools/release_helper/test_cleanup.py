"""
Tests for unified cleanup orchestration of Git tags and GHCR packages.
"""

import os
from unittest.mock import Mock, patch, MagicMock
from datetime import datetime, timedelta

import pytest

from tools.release_helper.cleanup import ReleaseCleanup, CleanupPlan, CleanupResult
from tools.release_helper.ghcr import GHCRPackageVersion


class TestReleaseCleanup:
    """Test cases for ReleaseCleanup orchestration."""

    @pytest.fixture
    def mock_token(self):
        """Provide a mock GitHub token."""
        return "ghp_test_token_1234567890"

    @pytest.fixture
    def cleanup(self, mock_token):
        """Create a ReleaseCleanup instance for testing."""
        return ReleaseCleanup(
            owner="test-owner",
            repo="test-repo",
            token=mock_token
        )

    @pytest.fixture
    def sample_tags(self):
        """Provide sample git tags for testing."""
        return [
            "demo-hello-python.v2.0.0",  # Keep: latest minor of major 2
            "demo-hello-python.v1.2.5",  # Keep: latest minor of major 1
            "demo-hello-python.v1.2.4",  # Delete: old patch
            "demo-hello-python.v1.1.3",  # Delete: old minor
            "demo-hello-python.v1.0.1",  # Delete: old minor
            "demo-hello-go.v1.0.0",      # Keep: recent
            "helm-demo-fastapi.v0.2.0",  # Keep: recent helm chart
            "helm-demo-fastapi.v0.1.0",  # Delete: old helm chart
        ]

    @pytest.fixture
    def sample_tag_dates(self):
        """Provide sample tag dates for testing."""
        now = datetime.now()
        return {
            "demo-hello-python.v2.0.0": now - timedelta(days=5),
            "demo-hello-python.v1.2.5": now - timedelta(days=10),
            "demo-hello-python.v1.2.4": now - timedelta(days=15),
            "demo-hello-python.v1.1.3": now - timedelta(days=20),
            "demo-hello-python.v1.0.1": now - timedelta(days=30),
            "demo-hello-go.v1.0.0": now - timedelta(days=5),
            "helm-demo-fastapi.v0.2.0": now - timedelta(days=7),
            "helm-demo-fastapi.v0.1.0": now - timedelta(days=60),
        }

    def test_cleanup_initialization(self, mock_token):
        """Test ReleaseCleanup initialization."""
        cleanup = ReleaseCleanup(
            owner="test-owner",
            repo="test-repo",
            token=mock_token
        )

        assert cleanup.owner == "test-owner"
        assert cleanup.repo == "test-repo"
        assert cleanup.ghcr_client is not None

    def test_plan_cleanup_default_retention(self, cleanup, sample_tags, sample_tag_dates):
        """Test planning cleanup with default retention policy."""
        with patch("tools.release_helper.cleanup.get_all_tags") as mock_get_tags, \
             patch("tools.release_helper.cleanup.get_tag_creation_date") as mock_get_date, \
             patch.object(cleanup.ghcr_client, "list_package_versions") as mock_list_versions:
            
            mock_get_tags.return_value = sample_tags
            mock_get_date.side_effect = lambda tag: sample_tag_dates.get(tag)
            
            # Mock GHCR package versions
            mock_list_versions.return_value = [
                GHCRPackageVersion(
                    version_id=12345,
                    tags=["v1.2.4"],
                    created_at="2025-01-01T00:00:00Z"
                ),
                GHCRPackageVersion(
                    version_id=12346,
                    tags=["v1.1.3"],
                    created_at="2025-01-01T00:00:00Z"
                )
            ]

            plan = cleanup.plan_cleanup(
                keep_minor_versions=2,
                min_age_days=14
            )

        assert isinstance(plan, CleanupPlan)
        assert len(plan.tags_to_delete) > 0
        assert len(plan.tags_to_keep) > 0
        
        # Should delete old tags
        assert "demo-hello-python.v1.2.4" in plan.tags_to_delete
        assert "demo-hello-python.v1.1.3" in plan.tags_to_delete
        
        # Should keep recent and latest minor per major
        assert "demo-hello-python.v2.0.0" in plan.tags_to_keep
        assert "demo-hello-python.v1.2.5" in plan.tags_to_keep

    def test_plan_cleanup_maps_tags_to_packages(self, cleanup, sample_tags, sample_tag_dates):
        """Test that planning correctly maps tags to GHCR packages."""
        with patch("tools.release_helper.cleanup.get_all_tags") as mock_get_tags, \
             patch("tools.release_helper.cleanup.get_tag_creation_date") as mock_get_date, \
             patch.object(cleanup.ghcr_client, "list_package_versions") as mock_list_versions:
            
            mock_get_tags.return_value = ["demo-hello-python.v1.0.0"]
            mock_get_date.return_value = datetime.now() - timedelta(days=30)
            
            mock_list_versions.return_value = [
                GHCRPackageVersion(
                    version_id=12345,
                    tags=["v1.0.0", "v1.0.0-amd64", "v1.0.0-arm64"]
                )
            ]

            plan = cleanup.plan_cleanup()

        # Should identify the package and all its versions
        assert "demo-hello-python" in plan.packages_to_delete
        assert 12345 in plan.packages_to_delete["demo-hello-python"]

    def test_plan_cleanup_handles_helm_charts(self, cleanup):
        """Test planning cleanup for helm chart tags."""
        helm_tags = [
            "helm-demo-fastapi.v0.3.0",  # Keep: recent
            "helm-demo-fastapi.v0.2.0",  # Keep: in retention window
            "helm-demo-fastapi.v0.1.0",  # Delete: old
        ]
        
        tag_dates = {
            "helm-demo-fastapi.v0.3.0": datetime.now() - timedelta(days=5),
            "helm-demo-fastapi.v0.2.0": datetime.now() - timedelta(days=12),
            "helm-demo-fastapi.v0.1.0": datetime.now() - timedelta(days=60),
        }

        with patch("tools.release_helper.cleanup.get_all_tags") as mock_get_tags, \
             patch("tools.release_helper.cleanup.get_tag_creation_date") as mock_get_date, \
             patch.object(cleanup.ghcr_client, "list_package_versions") as mock_list_versions:
            
            mock_get_tags.return_value = helm_tags
            mock_get_date.side_effect = lambda tag: tag_dates.get(tag)
            mock_list_versions.return_value = []

            plan = cleanup.plan_cleanup(keep_minor_versions=2, min_age_days=14)

        assert "helm-demo-fastapi.v0.1.0" in plan.tags_to_delete

    def test_plan_cleanup_respects_age_threshold(self, cleanup):
        """Test that planning respects minimum age threshold."""
        recent_tags = [
            "demo-hello-python.v1.0.0",  # Only 5 days old - should NOT delete
        ]
        
        tag_dates = {
            "demo-hello-python.v1.0.0": datetime.now() - timedelta(days=5),
        }

        with patch("tools.release_helper.cleanup.get_all_tags") as mock_get_tags, \
             patch("tools.release_helper.cleanup.get_tag_creation_date") as mock_get_date, \
             patch.object(cleanup.ghcr_client, "list_package_versions"):
            
            mock_get_tags.return_value = recent_tags
            mock_get_date.side_effect = lambda tag: tag_dates.get(tag)

            plan = cleanup.plan_cleanup(min_age_days=14)

        # Recent tag should be kept despite not being in retention window
        assert len(plan.tags_to_delete) == 0
        assert "demo-hello-python.v1.0.0" in plan.tags_to_keep

    def test_execute_cleanup_dry_run(self, cleanup):
        """Test executing cleanup in dry-run mode."""
        plan = CleanupPlan(
            tags_to_delete=["demo-hello-python.v1.0.0"],
            tags_to_keep=["demo-hello-python.v2.0.0"],
            packages_to_delete={"demo-hello-python": [12345]},
            retention_policy={
                "keep_minor_versions": 2,
                "min_age_days": 14
            }
        )

        with patch("tools.release_helper.cleanup.delete_remote_tag") as mock_delete_tag, \
             patch.object(cleanup.ghcr_client, "delete_package_version") as mock_delete_pkg:
            
            result = cleanup.execute_cleanup(plan, dry_run=True)

        # In dry run mode, no actual deletions should occur
        mock_delete_tag.assert_not_called()
        mock_delete_pkg.assert_not_called()
        
        assert isinstance(result, CleanupResult)
        assert result.dry_run is True

    def test_execute_cleanup_real_mode(self, cleanup):
        """Test executing cleanup in real mode."""
        plan = CleanupPlan(
            tags_to_delete=["demo-hello-python.v1.0.0"],
            tags_to_keep=["demo-hello-python.v2.0.0"],
            packages_to_delete={"demo-hello-python": [12345]},
            retention_policy={
                "keep_minor_versions": 2,
                "min_age_days": 14
            }
        )

        with patch("tools.release_helper.cleanup.delete_remote_tag") as mock_delete_tag, \
             patch.object(cleanup.ghcr_client, "delete_package_version") as mock_delete_pkg:
            
            mock_delete_tag.return_value = True
            mock_delete_pkg.return_value = True

            result = cleanup.execute_cleanup(plan, dry_run=False)

        # In real mode, deletions should occur
        mock_delete_tag.assert_called_once_with("demo-hello-python.v1.0.0", "test-owner", "test-repo")
        mock_delete_pkg.assert_called_once_with("demo-hello-python", 12345)
        
        assert result.dry_run is False
        assert len(result.tags_deleted) == 1
        assert "demo-hello-python.v1.0.0" in result.tags_deleted

    def test_execute_cleanup_handles_tag_deletion_errors(self, cleanup):
        """Test that cleanup handles tag deletion errors gracefully."""
        plan = CleanupPlan(
            tags_to_delete=["demo-hello-python.v1.0.0", "demo-hello-python.v1.1.0"],
            tags_to_keep=[],
            packages_to_delete={},
            retention_policy={}
        )

        with patch("tools.release_helper.cleanup.delete_remote_tag") as mock_delete_tag:
            # First deletion succeeds, second fails
            mock_delete_tag.side_effect = [True, False]

            result = cleanup.execute_cleanup(plan, dry_run=False)

        assert len(result.tags_deleted) == 1
        assert len(result.errors) == 1
        assert "demo-hello-python.v1.0.0" in result.tags_deleted
        assert any("v1.1.0" in error for error in result.errors)

    def test_execute_cleanup_handles_package_deletion_errors(self, cleanup):
        """Test that cleanup handles package deletion errors gracefully."""
        plan = CleanupPlan(
            tags_to_delete=[],
            tags_to_keep=[],
            packages_to_delete={"demo-hello-python": [12345, 12346]},
            retention_policy={}
        )

        with patch.object(cleanup.ghcr_client, "delete_package_version") as mock_delete_pkg:
            # First deletion succeeds, second fails
            mock_delete_pkg.side_effect = [True, False]

            result = cleanup.execute_cleanup(plan, dry_run=False)

        assert len(result.packages_deleted["demo-hello-python"]) == 1
        assert 12345 in result.packages_deleted["demo-hello-python"]
        assert len(result.errors) == 1

    def test_execute_cleanup_deletes_tags_before_packages(self, cleanup):
        """Test that tags are deleted before packages (safer order)."""
        plan = CleanupPlan(
            tags_to_delete=["demo-hello-python.v1.0.0"],
            tags_to_keep=[],
            packages_to_delete={"demo-hello-python": [12345]},
            retention_policy={}
        )

        call_order = []

        def track_tag_delete(*args):
            call_order.append("tag")
            return True

        def track_pkg_delete(*args):
            call_order.append("package")
            return True

        with patch("tools.release_helper.cleanup.delete_remote_tag", side_effect=track_tag_delete), \
             patch.object(cleanup.ghcr_client, "delete_package_version", side_effect=track_pkg_delete):
            
            cleanup.execute_cleanup(plan, dry_run=False)

        # Tags should be deleted before packages
        assert call_order == ["tag", "package"]

    def test_parse_tag_to_package_name_app_tags(self, cleanup):
        """Test parsing app tags to extract package names."""
        assert cleanup._parse_tag_to_package_name("demo-hello-python.v1.0.0") == "demo-hello-python"
        assert cleanup._parse_tag_to_package_name("api-gateway.v2.1.0") == "api-gateway"

    def test_parse_tag_to_package_name_helm_tags(self, cleanup):
        """Test parsing helm chart tags to extract package names."""
        # Helm charts keep the helm- prefix in package names
        assert cleanup._parse_tag_to_package_name("helm-demo-fastapi.v1.0.0") == "helm-demo-fastapi"
        assert cleanup._parse_tag_to_package_name("helm-manman-services.v0.2.0") == "helm-manman-services"

    def test_parse_tag_to_package_name_invalid_format(self, cleanup):
        """Test parsing invalid tag formats returns None."""
        assert cleanup._parse_tag_to_package_name("invalid-tag") is None
        assert cleanup._parse_tag_to_package_name("no-version") is None

    def test_extract_version_from_tag(self, cleanup):
        """Test extracting version from tag names."""
        assert cleanup._extract_version_from_tag("demo-hello-python.v1.0.0") == "v1.0.0"
        assert cleanup._extract_version_from_tag("helm-demo-fastapi.v0.2.1") == "v0.2.1"

    def test_extract_version_from_tag_invalid(self, cleanup):
        """Test extracting version from invalid tags."""
        assert cleanup._extract_version_from_tag("invalid") is None
        assert cleanup._extract_version_from_tag("demo-hello-python") is None


class TestCleanupPlan:
    """Test cases for CleanupPlan dataclass."""

    def test_plan_creation(self):
        """Test creating a cleanup plan."""
        plan = CleanupPlan(
            tags_to_delete=["tag1", "tag2"],
            tags_to_keep=["tag3"],
            packages_to_delete={"app1": [123, 456]},
            retention_policy={"keep_minor_versions": 2}
        )

        assert len(plan.tags_to_delete) == 2
        assert len(plan.tags_to_keep) == 1
        assert "app1" in plan.packages_to_delete
        assert plan.retention_policy["keep_minor_versions"] == 2

    def test_plan_total_deletions(self):
        """Test calculating total deletions in a plan."""
        plan = CleanupPlan(
            tags_to_delete=["tag1", "tag2"],
            tags_to_keep=[],
            packages_to_delete={"app1": [123, 456], "app2": [789]},
            retention_policy={}
        )

        assert plan.total_tag_deletions() == 2
        assert plan.total_package_deletions() == 3

    def test_plan_is_empty(self):
        """Test detecting empty cleanup plan."""
        empty_plan = CleanupPlan(
            tags_to_delete=[],
            tags_to_keep=[],
            packages_to_delete={},
            retention_policy={}
        )
        
        non_empty_plan = CleanupPlan(
            tags_to_delete=["tag1"],
            tags_to_keep=[],
            packages_to_delete={},
            retention_policy={}
        )

        assert empty_plan.is_empty()
        assert not non_empty_plan.is_empty()


class TestCleanupResult:
    """Test cases for CleanupResult dataclass."""

    def test_result_creation(self):
        """Test creating a cleanup result."""
        result = CleanupResult(
            tags_deleted=["tag1", "tag2"],
            packages_deleted={"app1": [123]},
            errors=["Error 1"],
            dry_run=False
        )

        assert len(result.tags_deleted) == 2
        assert "app1" in result.packages_deleted
        assert len(result.errors) == 1
        assert result.dry_run is False

    def test_result_success_status(self):
        """Test checking if result was successful."""
        success_result = CleanupResult(
            tags_deleted=["tag1"],
            packages_deleted={},
            errors=[],
            dry_run=False
        )
        
        failed_result = CleanupResult(
            tags_deleted=["tag1"],
            packages_deleted={},
            errors=["Error occurred"],
            dry_run=False
        )

        assert success_result.is_successful()
        assert not failed_result.is_successful()

    def test_result_summary(self):
        """Test generating result summary."""
        result = CleanupResult(
            tags_deleted=["tag1", "tag2"],
            releases_deleted=["tag1"],
            packages_deleted={"app1": [123, 456]},
            errors=["Error 1"],
            dry_run=False
        )

        summary = result.summary()
        assert "2" in summary  # 2 tags deleted
        assert "1" in summary  # 1 release deleted
        assert "2" in summary  # 2 packages deleted
        assert "1" in summary  # 1 error


class TestReleaseCleanupWithReleases:
    """Test cases for ReleaseCleanup with GitHub Releases integration."""

    @pytest.fixture
    def mock_token(self):
        """Provide a mock GitHub token."""
        return "ghp_test_token_1234567890"

    @pytest.fixture
    def cleanup(self, mock_token):
        """Create a ReleaseCleanup instance for testing."""
        return ReleaseCleanup(
            owner="test-owner",
            repo="test-repo",
            token=mock_token
        )

    @pytest.fixture
    def sample_tags(self):
        """Provide sample git tags for testing."""
        return [
            "demo-hello-python.v2.0.0",
            "demo-hello-python.v1.2.5",
            "demo-hello-python.v1.2.4",
            "demo-hello-python.v1.1.3",
        ]

    @pytest.fixture
    def sample_tag_dates(self):
        """Provide sample tag dates for testing."""
        now = datetime.now()
        return {
            "demo-hello-python.v2.0.0": now - timedelta(days=5),
            "demo-hello-python.v1.2.5": now - timedelta(days=10),
            "demo-hello-python.v1.2.4": now - timedelta(days=20),
            "demo-hello-python.v1.1.3": now - timedelta(days=30),
        }

    def test_plan_cleanup_with_releases(self, cleanup, sample_tags, sample_tag_dates):
        """Test planning cleanup identifies GitHub releases."""
        with patch("tools.release_helper.cleanup.get_all_tags", return_value=sample_tags):
            with patch("tools.release_helper.cleanup.get_tag_creation_date") as mock_get_date:
                mock_get_date.side_effect = lambda tag: sample_tag_dates.get(tag, datetime.now())
                
                # Mock release client
                mock_release_client = Mock()
                mock_release_client.find_releases_by_tags.return_value = {
                    "demo-hello-python.v1.2.4": {"id": 111, "tag_name": "demo-hello-python.v1.2.4"},
                    "demo-hello-python.v1.1.3": {"id": 222, "tag_name": "demo-hello-python.v1.1.3"},
                }
                cleanup.release_client = mock_release_client
                
                # Mock GHCR client to avoid real API calls
                cleanup.ghcr_client.list_package_versions = Mock(return_value=[])
                
                plan = cleanup.plan_cleanup(keep_minor_versions=2, min_age_days=7)
                
                # Should identify releases to delete
                assert len(plan.releases_to_delete) == 2
                assert "demo-hello-python.v1.2.4" in plan.releases_to_delete
                assert "demo-hello-python.v1.1.3" in plan.releases_to_delete
                assert plan.releases_to_delete["demo-hello-python.v1.2.4"] == 111
                assert plan.releases_to_delete["demo-hello-python.v1.1.3"] == 222
    
    def test_execute_cleanup_deletes_releases(self, cleanup):
        """Test executing cleanup deletes releases atomically."""
        plan = CleanupPlan(
            tags_to_delete=["tag1", "tag2"],
            tags_to_keep=["tag3"],
            packages_to_delete={},
            releases_to_delete={"tag1": 123, "tag2": 456}
        )
        
        # Mock the release client
        mock_release_client = Mock()
        mock_release_client.delete_release.return_value = True
        cleanup.release_client = mock_release_client
        
        # Execute dry run
        result = cleanup.execute_cleanup(plan, dry_run=True)
        
        # Should mark releases for deletion in dry run
        assert len(result.releases_deleted) == 2
        assert "tag1" in result.releases_deleted
        assert "tag2" in result.releases_deleted
        mock_release_client.delete_release.assert_not_called()
        
        # Execute real cleanup
        result = cleanup.execute_cleanup(plan, dry_run=False)
        
        # Should actually delete releases
        assert len(result.releases_deleted) == 2
        assert mock_release_client.delete_release.call_count == 2
        mock_release_client.delete_release.assert_any_call(123)

    def test_plan_cleanup_handles_none_release_data(self, cleanup, sample_tags, sample_tag_dates):
        """Test planning cleanup handles None and invalid release data gracefully."""
        with patch("tools.release_helper.cleanup.get_all_tags") as mock_get_tags, \
             patch("tools.release_helper.cleanup.get_tag_creation_date") as mock_get_date, \
             patch.object(cleanup.ghcr_client, "list_package_versions") as mock_list_versions, \
             patch.object(cleanup.release_client, "find_releases_by_tags") as mock_find_releases:
            
            mock_get_tags.return_value = sample_tags
            mock_get_date.side_effect = lambda tag: sample_tag_dates.get(tag)
            mock_list_versions.return_value = []
            
            # Mock releases with None values and missing IDs
            mock_find_releases.return_value = {
                "demo-hello-python.v1.2.4": None,  # None release
                "demo-hello-python.v1.1.3": "invalid_string",  # Invalid type
                "demo-hello-python.v1.0.1": {"tag_name": "v1.0.1"},  # Missing ID
                "helm-demo-fastapi.v0.1.0": {"id": 12345, "tag_name": "v0.1.0"},  # Valid
            }
            
            plan = cleanup.plan_cleanup(
                keep_minor_versions=2,
                min_age_days=14
            )
            
            # Should only include the valid release with ID
            assert len(plan.releases_to_delete) == 1
            assert "helm-demo-fastapi.v0.1.0" in plan.releases_to_delete
            assert plan.releases_to_delete["helm-demo-fastapi.v0.1.0"] == 12345
            
            # Invalid releases should be filtered out silently
            assert "demo-hello-python.v1.2.4" not in plan.releases_to_delete
            assert "demo-hello-python.v1.1.3" not in plan.releases_to_delete
            assert "demo-hello-python.v1.0.1" not in plan.releases_to_delete
        mock_release_client.delete_release.assert_any_call(456)