"""
Unit tests for the validation portion of the release helper.

This module provides comprehensive unit tests for the validation.py module,
covering all validation functions:
- validate_semantic_version(): Version format validation
- check_version_exists_in_registry(): Registry existence checks
- validate_release_version(): Complete release validation
- validate_apps(): App name validation and resolution
- validate_domain(): Domain validation
- Various helper functions

The tests use mocking to avoid actual Docker commands and network calls,
making them fast and reliable for CI/CD environments.
"""

import os
import pytest
import subprocess
from unittest.mock import Mock, patch, MagicMock

from tools.release_helper.validation import (
    validate_semantic_version,
    check_version_exists_in_registry,
    validate_release_version,
    validate_apps,
    validate_domain,
    get_available_domains,
    is_domain_name,
    _get_app_full_name
)





@pytest.fixture
def sample_apps_with_collision(sample_apps):
    """Extend sample_apps with intentional name collision for validation tests."""
    collision_app = {
        "name": "hello_python",  # Intentional name collision
        "domain": "api",
        "bazel_target": "//api/hello_python:hello_python_metadata"
    }
    return sample_apps + [collision_app]


@pytest.fixture
def mock_list_all_apps(sample_apps):
    """Mock list_all_apps to return sample data."""
    with patch('tools.release_helper.validation.list_all_apps') as mock:
        mock.return_value = sample_apps
        yield mock


@pytest.fixture
def mock_get_app_metadata(sample_metadata):
    """Mock get_app_metadata to return sample metadata."""
    with patch('tools.release_helper.validation.get_app_metadata') as mock:
        mock.return_value = sample_metadata
        yield mock





class TestValidateSemanticVersion:
    """Test cases for validate_semantic_version function."""

    def test_valid_semantic_versions(self):
        """Test valid semantic version formats."""
        valid_versions = [
            "v1.0.0",
            "v0.1.0", 
            "v10.20.30",
            "v1.0.0-alpha",
            "v1.0.0-beta1",
            "v1.0.0-rc1",
            "v1.0.0-rc2",
            "v2.1.0-beta.1",
        ]
        
        for version in valid_versions:
            assert validate_semantic_version(version), f"Expected {version} to be valid"

    def test_invalid_semantic_versions(self):
        """Test invalid semantic version formats (safety guards)."""
        invalid_versions = [
            "1.0.0",  # Missing 'v' prefix
            "v1.0",   # Missing patch version
            "v1",     # Missing minor and patch
            "",       # Empty string
            "latest", # Not semantic version
        ]
        
        for version in invalid_versions:
            assert not validate_semantic_version(version), f"Expected {version} to be invalid"


class TestIsPrereleaseVersion:
    """Test cases for is_prerelease_version function."""

    def test_prerelease_versions(self):
        """Test that prerelease versions are correctly identified."""
        from tools.release_helper.validation import is_prerelease_version
        
        prerelease_versions = [
            "v1.0.0-alpha",
            "v1.0.0-beta",
            "v1.0.0-beta1",
            "v1.0.0-rc1",
            "v1.0.0-rc2",
            "v2.1.3-alpha.1",
            "v1.2.3-beta.2",
        ]
        
        for version in prerelease_versions:
            assert is_prerelease_version(version), f"Expected {version} to be identified as prerelease"

    def test_stable_versions(self):
        """Test that stable versions are not identified as prereleases."""
        from tools.release_helper.validation import is_prerelease_version
        
        stable_versions = [
            "v1.0.0",
            "v0.1.0",
            "v10.20.30",
            "v2.1.3",
        ]
        
        for version in stable_versions:
            assert not is_prerelease_version(version), f"Expected {version} to NOT be identified as prerelease"

    def test_invalid_versions(self):
        """Test that invalid versions return False."""
        from tools.release_helper.validation import is_prerelease_version
        
        invalid_versions = [
            "1.0.0",
            "v1.0",
            "latest",
            "",
        ]
        
        for version in invalid_versions:
            assert not is_prerelease_version(version), f"Expected {version} to return False (invalid)"


class TestCheckVersionExistsInRegistry:
    """Test cases for check_version_exists_in_registry function."""

    def test_version_exists_success(self, mock_get_app_metadata, mock_subprocess_run):
        """Test when version exists in registry."""
        mock_subprocess_run.return_value = Mock(returncode=0, stderr="")
        
        result = check_version_exists_in_registry("//demo/hello_python:hello_python_metadata", "v1.0.0")
        
        assert result is True
        mock_subprocess_run.assert_called_once_with(
            ["docker", "manifest", "inspect", "ghcr.io/demo-hello_python:v1.0.0"],
            capture_output=True,
            text=True,
            check=False
        )

    def test_version_not_found_manifest_unknown(self, mock_get_app_metadata, mock_subprocess_run):
        """Test when version doesn't exist (manifest unknown)."""
        mock_subprocess_run.return_value = Mock(
            returncode=1, 
            stderr="manifest unknown: manifest tagged by v1.0.0 is not found"
        )
        
        result = check_version_exists_in_registry("//demo/hello_python:hello_python_metadata", "v1.0.0")
        
        assert result is False

    def test_version_not_found_name_invalid(self, mock_get_app_metadata, mock_subprocess_run):
        """Test when version doesn't exist (name invalid).""" 
        mock_subprocess_run.return_value = Mock(
            returncode=1,
            stderr="name invalid: invalid repository name"
        )
        
        result = check_version_exists_in_registry("//demo/hello_python:hello_python_metadata", "v1.0.0")
        
        assert result is False

    def test_version_unauthorized(self, mock_get_app_metadata, mock_subprocess_run):
        """Test when access is unauthorized (treat as not found)."""
        mock_subprocess_run.return_value = Mock(
            returncode=1,
            stderr="unauthorized: access denied"
        )
        
        result = check_version_exists_in_registry("//demo/hello_python:hello_python_metadata", "v1.0.0")
        
        assert result is False

    def test_version_check_other_error(self, mock_get_app_metadata, mock_subprocess_run, mock_print):
        """Test when other error occurs (assume exists for safety)."""
        mock_subprocess_run.return_value = Mock(
            returncode=1,
            stderr="some other error occurred"
        )
        
        result = check_version_exists_in_registry("//demo/hello_python:hello_python_metadata", "v1.0.0")
        
        assert result is False  # Conservative approach

    def test_docker_not_available(self, mock_get_app_metadata, mock_subprocess_run, mock_print):
        """Test when Docker is not available."""
        mock_subprocess_run.side_effect = FileNotFoundError("docker command not found")
        
        result = check_version_exists_in_registry("//demo/hello_python:hello_python_metadata", "v1.0.0")
        
        assert result is False

    def test_github_repository_owner_env_var(self, mock_get_app_metadata, mock_subprocess_run, github_owner_env):
        """Test registry path with GITHUB_REPOSITORY_OWNER environment variable."""
        mock_subprocess_run.return_value = Mock(returncode=0, stderr="")
        
        result = check_version_exists_in_registry("//demo/hello_python:hello_python_metadata", "v1.0.0")
        
        assert result is True
        mock_subprocess_run.assert_called_once_with(
            ["docker", "manifest", "inspect", "ghcr.io/testowner/demo-hello_python:v1.0.0"],
            capture_output=True,
            text=True,
            check=False
        )

    def test_non_ghcr_registry(self, mock_get_app_metadata, mock_subprocess_run):
        """Test with non-GHCR registry."""
        metadata = {
            "name": "hello_python",
            "domain": "demo", 
            "registry": "docker.io",
            "version": "latest"
        }
        
        with patch('tools.release_helper.validation.get_app_metadata', return_value=metadata):
            mock_subprocess_run.return_value = Mock(returncode=0, stderr="")
            
            result = check_version_exists_in_registry("//demo/hello_python:hello_python_metadata", "v1.0.0")
        
        assert result is True
        mock_subprocess_run.assert_called_once_with(
            ["docker", "manifest", "inspect", "docker.io/demo-hello_python:v1.0.0"],
            capture_output=True,
            text=True,
            check=False
        )


class TestValidateReleaseVersion:
    """Test cases for validate_release_version function."""

    def test_validate_latest_version(self, mock_get_app_metadata, mock_print):
        """Test that 'latest' version is always allowed."""
        # Should not raise any exception
        validate_release_version("//demo/hello_python:hello_python_metadata", "latest")

    def test_validate_valid_semantic_version_not_exists(self, mock_get_app_metadata, mock_print):
        """Test valid semantic version that doesn't exist in registry."""
        with patch('tools.release_helper.validation.check_version_exists_in_registry', return_value=False):
            # Should not raise any exception
            validate_release_version("//demo/hello_python:hello_python_metadata", "v1.0.0")

    def test_validate_invalid_semantic_version(self, mock_get_app_metadata):
        """Test invalid semantic version format."""
        with pytest.raises(ValueError, match="does not follow semantic versioning format"):
            validate_release_version("//demo/hello_python:hello_python_metadata", "1.0.0")

    def test_validate_version_already_exists(self, mock_get_app_metadata):
        """Test version that already exists in registry."""
        with patch('tools.release_helper.validation.check_version_exists_in_registry', return_value=True):
            with pytest.raises(ValueError, match="already exists"):
                validate_release_version("//demo/hello_python:hello_python_metadata", "v1.0.0")

    def test_validate_version_exists_allow_overwrite(self, mock_get_app_metadata, mock_print):
        """Test version that exists but overwrite is allowed."""
        with patch('tools.release_helper.validation.check_version_exists_in_registry', return_value=True):
            # Should not raise any exception
            validate_release_version("//demo/hello_python:hello_python_metadata", "v1.0.0", allow_overwrite=True)


class TestGetAppFullName:
    """Test cases for _get_app_full_name helper function."""

    def test_get_app_full_name(self):
        """Test _get_app_full_name formats correctly."""
        app = {"domain": "demo", "name": "hello_python"}
        result = _get_app_full_name(app)
        assert result == "demo-hello_python"


class TestGetAvailableDomains:
    """Test cases for get_available_domains function."""

    def test_get_available_domains(self, mock_list_all_apps, sample_apps):
        """Test getting list of available domains."""
        result = get_available_domains()
        expected = ["api", "demo"]  # Sorted unique domains
        assert result == expected


class TestValidateDomain:
    """Test cases for validate_domain function."""

    def test_validate_existing_domain(self, mock_list_all_apps, sample_apps):
        """Test validating an existing domain."""
        result = validate_domain("demo")
        
        expected_apps = [app for app in sample_apps if app["domain"] == "demo"]
        assert result == expected_apps

    def test_validate_nonexistent_domain(self, mock_list_all_apps, sample_apps):
        """Test validating a non-existent domain."""
        with pytest.raises(ValueError, match="Domain 'nonexistent' not found"):
            validate_domain("nonexistent")

    def test_validate_apps_not_found(self, mock_list_all_apps, sample_apps, mock_print):
        """Test validating non-existent apps."""
        requested = ["nonexistent-app"]
        
        with pytest.raises(ValueError, match="Invalid apps: nonexistent-app"):
            validate_apps(requested)


class TestIsDomainName:
    """Test cases for is_domain_name function."""

    def test_is_domain_name_true(self, mock_list_all_apps, sample_apps):
        """Test recognizing valid domain names."""
        assert is_domain_name("demo") is True
        assert is_domain_name("api") is True

    def test_is_domain_name_false(self, mock_list_all_apps, sample_apps):
        """Test recognizing non-domain names."""
        assert is_domain_name("hello_python") is False
        assert is_domain_name("nonexistent") is False


class TestValidateApps:
    """Test cases for validate_apps function."""

    def test_validate_apps_full_format(self, mock_list_all_apps, sample_apps):
        """Test validating apps using full format (domain-name)."""
        requested = ["demo-hello_python", "api-status_service"]
        result = validate_apps(requested)
        
        assert len(result) == 2
        assert result[0]["name"] == "hello_python"
        assert result[0]["domain"] == "demo"
        assert result[1]["name"] == "status_service" 
        assert result[1]["domain"] == "api"

    def test_validate_apps_path_format(self, mock_list_all_apps, sample_apps):
        """Test validating apps using path format (domain/name)."""
        requested = ["demo/hello_python", "api/status_service"]
        result = validate_apps(requested)
        
        assert len(result) == 2
        assert result[0]["name"] == "hello_python"
        assert result[0]["domain"] == "demo"

    def test_validate_apps_short_format_unambiguous(self, mock_list_all_apps, sample_apps):
        """Test validating apps using short format when unambiguous."""
        requested = ["hello_go", "status_service"]  # These names are unique
        result = validate_apps(requested)
        
        assert len(result) == 2
        assert result[0]["name"] == "hello_go"
        assert result[1]["name"] == "status_service"

    def test_validate_apps_short_format_ambiguous(self, sample_apps_with_collision):
        """Test validating apps using short format when ambiguous."""
        with patch('tools.release_helper.validation.list_all_apps', return_value=sample_apps_with_collision):
            requested = ["hello_python"]  # This name exists in both demo and api domains
            
            with pytest.raises(ValueError, match="ambiguous, could be"):
                validate_apps(requested)

    def test_validate_apps_domain_format(self, mock_list_all_apps, sample_apps):
        """Test validating using domain format (returns all apps in domain)."""
        requested = ["demo"]
        result = validate_apps(requested)
        
        demo_apps = [app for app in sample_apps if app["domain"] == "demo"]
        assert len(result) == len(demo_apps)
        assert all(app["domain"] == "demo" for app in result)

    def test_validate_apps_mixed_formats(self, mock_list_all_apps, sample_apps):
        """Test validating apps using mixed formats."""
        requested = ["demo-hello_python", "api/status_service", "hello_go", "api"]
        result = validate_apps(requested)
        
        # Should include: demo-hello_python, api/status_service, hello_go, and all api apps
        expected_names = {"hello_python", "status_service", "hello_go"}  # api domain adds hello_python + status_service
        actual_names = {app["name"] for app in result}
        assert expected_names.issubset(actual_names)

    def test_validate_apps_invalid_app(self, mock_list_all_apps, sample_apps):
        """Test validating non-existent apps."""
        requested = ["nonexistent-app"]
        
        with pytest.raises(ValueError, match="Invalid apps: nonexistent-app"):
            validate_apps(requested)

    def test_validate_apps_empty_list(self, mock_list_all_apps, sample_apps):
        """Test validating empty app list."""
        requested = []
        result = validate_apps(requested)
        
        assert result == []

    def test_validate_apps_all_formats_error_message(self, mock_list_all_apps, sample_apps):
        """Test that error message includes all available formats."""
        requested = ["invalid-app"]
        
        with pytest.raises(ValueError) as exc_info:
            validate_apps(requested)
        
        error_msg = str(exc_info.value)
        assert "Available apps:" in error_msg
        assert "Available domains:" in error_msg
        assert "full format" in error_msg
        assert "path format" in error_msg
        assert "short format" in error_msg
        assert "domain format" in error_msg