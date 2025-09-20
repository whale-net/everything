"""
Unit tests for the release_notes portion of the release helper.

This module provides comprehensive unit tests for the release_notes.py module,
covering all release notes generation functions:
- parse_tag_info(): Tag parsing and validation
- validate_tag_format(): Tag format validation
- ReleaseNote and AppReleaseData: Data classes
- ReleaseNotesFormatter: Multi-format output generation
- get_commits_between_refs(): Git commit retrieval
- filter_commits_by_app(): App-specific commit filtering
- generate_release_notes(): Complete release notes generation
- generate_release_notes_for_all_apps(): Bulk generation

The tests use mocking to avoid actual Git operations and file system access,
making them fast and reliable for CI/CD environments.
"""

import subprocess
import json
import pytest
from datetime import datetime
from unittest.mock import Mock, patch, MagicMock

from tools.release_helper.release_notes import (
    parse_tag_info,
    validate_tag_format,
    ReleaseNote,
    AppReleaseData,
    ReleaseNotesFormatter,
    get_commits_between_refs,
    filter_commits_by_app,
    generate_release_notes,
    generate_release_notes_for_all_apps
)


@pytest.fixture
def sample_release_note():
    """Fixture providing sample release note data."""
    return ReleaseNote(
        commit_sha="abc12345",
        commit_message="Fix bug in authentication",
        author="John Doe",
        date="2024-01-15 10:30:00 +0000",
        files_changed=["demo/hello_python/main.py", "demo/hello_python/auth.py"]
    )


@pytest.fixture
def sample_app_release_data(sample_release_note):
    """Fixture providing sample app release data."""
    return AppReleaseData(
        app_name="hello_python",
        current_tag="demo-hello_python.v1.0.0",
        previous_tag="demo-hello_python.v0.9.0",
        released_at="2024-01-15 12:00:00 UTC",
        commits=[sample_release_note]
    )


@pytest.fixture
def sample_apps():
    """Fixture providing sample app data for testing."""
    return [
        {
            "name": "hello_python",
            "domain": "demo",
            "bazel_target": "//demo/hello_python:hello_python_metadata"
        },
        {
            "name": "hello_go", 
            "domain": "demo",
            "bazel_target": "//demo/hello_go:hello_go_metadata"
        }
    ]


class TestParseTagInfo:
    """Test cases for parse_tag_info function."""

    def test_parse_tag_info_valid_format(self):
        """Test parsing valid tag format."""
        result = parse_tag_info("demo-hello_python.v1.0.0")
        assert result == ("demo", "hello_python", "v1.0.0")

    def test_parse_tag_info_complex_domain_name(self):
        """Test parsing tag with complex domain name."""
        result = parse_tag_info("api-service-hello_python.v2.1.3")
        assert result == ("api-service", "hello_python", "v2.1.3")

    def test_parse_tag_info_complex_app_name(self):
        """Test parsing tag with complex app name."""
        result = parse_tag_info("demo-hello_python_v2.v1.0.0")
        assert result == ("demo", "hello_python_v2", "v1.0.0")

    def test_parse_tag_info_prerelease_version(self):
        """Test parsing tag with prerelease version."""
        result = parse_tag_info("demo-hello_python.v1.0.0-beta1")
        assert result == ("demo", "hello_python", "v1.0.0-beta1")

    def test_parse_tag_info_missing_version_prefix(self):
        """Test error when tag is missing '.v' version prefix."""
        with pytest.raises(ValueError, match="Invalid tag format"):
            parse_tag_info("demo-hello_python1.0.0")

    def test_parse_tag_info_missing_dash(self):
        """Test error when tag is missing dash separator."""
        with pytest.raises(ValueError, match="Invalid tag format"):
            parse_tag_info("demohello_python.v1.0.0")

    def test_parse_tag_info_multiple_version_prefixes(self):
        """Test error when tag has multiple '.v' prefixes."""
        with pytest.raises(ValueError, match="Invalid tag format"):
            parse_tag_info("demo-hello.v1.v1.0.0")

    def test_parse_tag_info_empty_string(self):
        """Test error with empty string."""
        with pytest.raises(ValueError, match="Invalid tag format"):
            parse_tag_info("")

    def test_parse_tag_info_only_dash(self):
        """Test error with only dash character."""
        with pytest.raises(ValueError, match="Invalid tag format"):
            parse_tag_info("-")

    def test_parse_tag_info_only_version_prefix(self):
        """Test error with only version prefix."""
        with pytest.raises(ValueError, match="Invalid tag format"):
            parse_tag_info(".v1.0.0")


class TestValidateTagFormat:
    """Test cases for validate_tag_format function."""

    def test_validate_tag_format_valid_tags(self):
        """Test validation of valid tag formats."""
        valid_tags = [
            "demo-hello_python.v1.0.0",
            "api-service-status.v2.1.3",
            "my_domain-my_app.v0.1.0-alpha",
            "complex-domain-name-complex_app_name.v1.2.3-rc1"
        ]
        
        for tag in valid_tags:
            assert validate_tag_format(tag), f"Expected {tag} to be valid"

    def test_validate_tag_format_invalid_tags(self):
        """Test validation of invalid tag formats."""
        invalid_tags = [
            "demo-hello_python1.0.0",  # Missing .v
            "demohello_python.v1.0.0",  # Missing dash
            "",  # Empty string
            "demo.v1.0.0",  # Missing app name
            "demo-hello_python.v1.v1.0.0"  # Multiple .v
        ]
        
        for tag in invalid_tags:
            assert not validate_tag_format(tag), f"Expected {tag} to be invalid"


class TestReleaseNoteDataClass:
    """Test cases for ReleaseNote data class."""

    def test_release_note_creation(self):
        """Test creating ReleaseNote instance."""
        note = ReleaseNote(
            commit_sha="abc123",
            commit_message="Fix bug",
            author="Jane Doe",
            date="2024-01-15",
            files_changed=["file1.py", "file2.py"]
        )
        
        assert note.commit_sha == "abc123"
        assert note.commit_message == "Fix bug"
        assert note.author == "Jane Doe"
        assert note.date == "2024-01-15"
        assert note.files_changed == ["file1.py", "file2.py"]


class TestAppReleaseDataClass:
    """Test cases for AppReleaseData data class."""

    def test_app_release_data_creation(self, sample_release_note):
        """Test creating AppReleaseData instance."""
        data = AppReleaseData(
            app_name="hello_python",
            current_tag="v1.0.0",
            previous_tag="v0.9.0",
            released_at="2024-01-15",
            commits=[sample_release_note]
        )
        
        assert data.app_name == "hello_python"
        assert data.current_tag == "v1.0.0"
        assert data.previous_tag == "v0.9.0"
        assert data.released_at == "2024-01-15"
        assert len(data.commits) == 1

    def test_app_release_data_commit_count(self, sample_release_note):
        """Test commit_count property."""
        data = AppReleaseData(
            app_name="test",
            current_tag="v1.0.0",
            previous_tag="v0.9.0",
            released_at="2024-01-15",
            commits=[sample_release_note, sample_release_note]
        )
        
        assert data.commit_count == 2

    def test_app_release_data_has_changes_true(self, sample_release_note):
        """Test has_changes property when there are changes."""
        data = AppReleaseData(
            app_name="test",
            current_tag="v1.0.0",
            previous_tag="v0.9.0",
            released_at="2024-01-15",
            commits=[sample_release_note]
        )
        
        assert data.has_changes is True

    def test_app_release_data_has_changes_false(self):
        """Test has_changes property when there are no changes."""
        data = AppReleaseData(
            app_name="test",
            current_tag="v1.0.0",
            previous_tag="v0.9.0",
            released_at="2024-01-15",
            commits=[]
        )
        
        assert data.has_changes is False

    def test_app_release_data_summary_with_changes(self, sample_release_note):
        """Test summary property with changes."""
        data = AppReleaseData(
            app_name="hello_python",
            current_tag="v1.0.0",
            previous_tag="v0.9.0",
            released_at="2024-01-15",
            commits=[sample_release_note]
        )
        
        assert data.summary == "1 commits affecting hello_python"

    def test_app_release_data_summary_no_changes(self):
        """Test summary property without changes."""
        data = AppReleaseData(
            app_name="hello_python",
            current_tag="v1.0.0",
            previous_tag="v0.9.0",
            released_at="2024-01-15",
            commits=[]
        )
        
        assert data.summary == "No changes affecting hello_python found"


class TestReleaseNotesFormatter:
    """Test cases for ReleaseNotesFormatter class."""

    def test_to_markdown_with_changes(self, sample_app_release_data):
        """Test markdown formatting with changes."""
        result = ReleaseNotesFormatter.to_markdown(sample_app_release_data)
        
        assert "**Released:** 2024-01-15 12:00:00 UTC" in result
        assert "**Previous Version:** demo-hello_python.v0.9.0" in result
        assert "**Commits:** 1" in result
        assert "## Changes" in result
        assert "### [abc12345] Fix bug in authentication" in result
        assert "**Author:** John Doe" in result
        assert "**Files:** demo/hello_python/main.py, demo/hello_python/auth.py" in result
        assert "*Generated automatically by the release helper*" in result

    def test_to_markdown_no_changes(self):
        """Test markdown formatting without changes."""
        data = AppReleaseData(
            app_name="hello_python",
            current_tag="v1.0.0",
            previous_tag="v0.9.0",
            released_at="2024-01-15",
            commits=[]
        )
        
        result = ReleaseNotesFormatter.to_markdown(data)
        
        assert "No changes affecting hello_python found" in result
        assert "**Commits:** 0" in result

    def test_to_markdown_many_files(self):
        """Test markdown formatting with many files (truncation)."""
        note = ReleaseNote(
            commit_sha="abc123",
            commit_message="Update many files",
            author="Jane Doe",
            date="2024-01-15",
            files_changed=[f"file{i}.py" for i in range(10)]  # 10 files
        )
        
        data = AppReleaseData(
            app_name="hello_python",
            current_tag="v1.0.0",
            previous_tag="v0.9.0",
            released_at="2024-01-15",
            commits=[note]
        )
        
        result = ReleaseNotesFormatter.to_markdown(data)
        
        assert "*... and 5 more files*" in result  # Should show truncation message

    def test_to_plain_text_with_changes(self, sample_app_release_data):
        """Test plain text formatting with changes."""
        result = ReleaseNotesFormatter.to_plain_text(sample_app_release_data)
        
        assert "demo hello_python v1.0.0" in result  # Parsed title
        assert "Released: 2024-01-15 12:00:00 UTC" in result
        assert "Previous Version: demo-hello_python.v0.9.0" in result
        assert "Commits: 1" in result
        assert "1. [abc12345] Fix bug in authentication" in result
        assert "   Author: John Doe" in result

    def test_to_plain_text_invalid_tag_format(self):
        """Test plain text formatting with invalid tag format."""
        data = AppReleaseData(
            app_name="hello_python",
            current_tag="invalid-tag-format",  # Invalid format
            previous_tag="v0.9.0",
            released_at="2024-01-15",
            commits=[]
        )
        
        result = ReleaseNotesFormatter.to_plain_text(data)
        
        assert "Release Notes: hello_python invalid-tag-format" in result  # Fallback title

    def test_to_json_with_changes(self, sample_app_release_data):
        """Test JSON formatting with changes."""
        result = ReleaseNotesFormatter.to_json(sample_app_release_data)
        
        data = json.loads(result)
        assert data["app"] == "hello_python"
        assert data["version"] == "demo-hello_python.v1.0.0"
        assert data["previous_version"] == "demo-hello_python.v0.9.0"
        assert data["commit_count"] == 1
        assert len(data["changes"]) == 1
        assert data["changes"][0]["sha"] == "abc12345"
        assert data["changes"][0]["message"] == "Fix bug in authentication"
        assert data["summary"] == "1 commits affecting hello_python"

    def test_to_json_no_changes(self):
        """Test JSON formatting without changes."""
        data = AppReleaseData(
            app_name="hello_python",
            current_tag="v1.0.0",
            previous_tag="v0.9.0",
            released_at="2024-01-15",
            commits=[]
        )
        
        result = ReleaseNotesFormatter.to_json(data)
        
        parsed = json.loads(result)
        assert parsed["commit_count"] == 0
        assert parsed["changes"] == []
        assert "No changes affecting" in parsed["summary"]


class TestGetCommitsBetweenRefs:
    """Test cases for get_commits_between_refs function."""

    @patch('subprocess.run')
    def test_get_commits_between_refs_success(self, mock_subprocess):
        """Test successful commit retrieval."""
        # Mock git rev-parse check (ref exists)
        mock_subprocess.side_effect = [
            Mock(returncode=0),  # rev-parse check
            Mock(  # git log
                returncode=0,
                stdout="abc12345|Fix bug|John Doe|2024-01-15 10:30:00 +0000\ndef67890|Add feature|Jane Doe|2024-01-14 15:20:00 +0000",
                stderr=""
            ),
            Mock(  # git diff-tree for first commit
                returncode=0,
                stdout="file1.py\nfile2.py",
                stderr=""
            ),
            Mock(  # git diff-tree for second commit
                returncode=0,
                stdout="file3.py",
                stderr=""
            )
        ]
        
        result = get_commits_between_refs("v0.9.0", "v1.0.0")
        
        assert len(result) == 2
        assert result[0].commit_sha == "abc12345"
        assert result[0].commit_message == "Fix bug"
        assert result[0].author == "John Doe"
        assert result[0].files_changed == ["file1.py", "file2.py"]
        assert result[1].commit_sha == "def67890"

    @patch('subprocess.run')
    def test_get_commits_between_refs_ref_not_found(self, mock_subprocess):
        """Test behavior when start ref doesn't exist."""
        mock_subprocess.side_effect = [
            Mock(returncode=1),  # rev-parse check fails
            Mock(  # fallback git log
                returncode=0,
                stdout="abc12345|Recent commit|John Doe|2024-01-15 10:30:00 +0000",
                stderr=""
            ),
            Mock(  # git diff-tree
                returncode=0,
                stdout="file1.py",
                stderr=""
            )
        ]
        
        with patch('builtins.print'):  # Mock print to avoid output during test
            result = get_commits_between_refs("nonexistent-ref", "v1.0.0")
        
        assert len(result) == 1
        assert result[0].commit_sha == "abc12345"

    @patch('subprocess.run')
    def test_get_commits_between_refs_no_commits(self, mock_subprocess):
        """Test behavior when there are no commits between refs."""
        mock_subprocess.side_effect = [
            Mock(returncode=0),  # rev-parse check
            Mock(returncode=0, stdout="", stderr="")  # git log (empty)
        ]
        
        result = get_commits_between_refs("v0.9.0", "v1.0.0")
        
        assert len(result) == 0

    @patch('subprocess.run')
    def test_get_commits_between_refs_git_error(self, mock_subprocess):
        """Test error handling when git command fails."""
        mock_subprocess.side_effect = [
            Mock(returncode=0),  # rev-parse check
            subprocess.CalledProcessError(1, "git log")  # git log fails
        ]
        
        with patch('builtins.print'):  # Mock print to avoid output during test
            result = get_commits_between_refs("v0.9.0", "v1.0.0")
        
        assert len(result) == 0

    @patch('subprocess.run')
    def test_get_commits_between_refs_diff_tree_error(self, mock_subprocess):
        """Test handling of diff-tree errors."""
        mock_subprocess.side_effect = [
            Mock(returncode=0),  # rev-parse check
            Mock(  # git log
                returncode=0,
                stdout="abc12345|Fix bug|John Doe|2024-01-15 10:30:00 +0000",
                stderr=""
            ),
            subprocess.CalledProcessError(1, "git diff-tree")  # diff-tree fails
        ]
        
        result = get_commits_between_refs("v0.9.0", "v1.0.0")
        
        assert len(result) == 1
        assert result[0].files_changed == []  # Should fallback to empty list

    @patch('subprocess.run')
    def test_get_commits_between_refs_malformed_output(self, mock_subprocess):
        """Test handling of malformed git log output."""
        mock_subprocess.side_effect = [
            Mock(returncode=0),  # rev-parse check
            Mock(  # git log with malformed output
                returncode=0,
                stdout="abc12345|Fix bug|John Doe\nmalformed-line\ndef67890|Add feature|Jane Doe|2024-01-14",
                stderr=""
            )
        ]
        
        result = get_commits_between_refs("v0.9.0", "v1.0.0")
        
        # Should skip malformed lines
        assert len(result) == 0


class TestFilterCommitsByApp:
    """Test cases for filter_commits_by_app function."""

    @patch('tools.release_helper.release_notes.list_all_apps')
    def test_filter_commits_by_app_success(self, mock_list_apps, sample_apps):
        """Test successful commit filtering for an app."""
        mock_list_apps.return_value = sample_apps
        
        commits = [
            ReleaseNote("abc123", "Fix app bug", "John", "2024-01-15", ["demo/hello_python/main.py"]),
            ReleaseNote("def456", "Update go app", "Jane", "2024-01-14", ["demo/hello_go/main.go"]),
            ReleaseNote("ghi789", "Update docs", "Bob", "2024-01-13", ["README.md"])
        ]
        
        result = filter_commits_by_app(commits, "hello_python")
        
        # Should include the python app commit and the docs commit (affects all apps)
        assert len(result) == 2
        assert result[0].commit_sha == "abc123"
        assert result[1].commit_sha == "ghi789"

    @patch('tools.release_helper.release_notes.list_all_apps')
    def test_filter_commits_by_app_infrastructure_changes(self, mock_list_apps, sample_apps):
        """Test filtering includes infrastructure changes."""
        mock_list_apps.return_value = sample_apps
        
        commits = [
            ReleaseNote("abc123", "Update CI", "John", "2024-01-15", [".github/workflows/ci.yml"]),
            ReleaseNote("def456", "Update Docker", "Jane", "2024-01-14", ["docker/Dockerfile"]),
            ReleaseNote("ghi789", "Update BUILD", "Bob", "2024-01-13", ["BUILD.bazel"]),
            ReleaseNote("jkl012", "Unrelated", "Alice", "2024-01-12", ["other/file.txt"])
        ]
        
        result = filter_commits_by_app(commits, "hello_python")
        
        # Should include infrastructure changes but not unrelated files
        assert len(result) == 3
        commit_shas = [c.commit_sha for c in result]
        assert "abc123" in commit_shas  # CI change
        assert "def456" in commit_shas  # Docker change
        assert "ghi789" in commit_shas  # BUILD change
        assert "jkl012" not in commit_shas  # Unrelated

    @patch('tools.release_helper.release_notes.list_all_apps')
    def test_filter_commits_by_app_not_found(self, mock_list_apps, sample_apps):
        """Test behavior when app is not found."""
        mock_list_apps.return_value = sample_apps
        
        commits = [
            ReleaseNote("abc123", "Fix bug", "John", "2024-01-15", ["demo/hello_python/main.py"])
        ]
        
        with patch('builtins.print'):  # Mock print to avoid output during test
            result = filter_commits_by_app(commits, "nonexistent_app")
        
        assert len(result) == 0

    @patch('tools.release_helper.release_notes.list_all_apps')
    def test_filter_commits_by_app_exception_handling(self, mock_list_apps):
        """Test exception handling in commit filtering."""
        mock_list_apps.side_effect = Exception("List apps failed")
        
        commits = [
            ReleaseNote("abc123", "Fix bug", "John", "2024-01-15", ["demo/hello_python/main.py"])
        ]
        
        with patch('builtins.print'):  # Mock print to avoid output during test
            result = filter_commits_by_app(commits, "hello_python")
        
        # Should return all commits if filtering fails
        assert len(result) == 1
        assert result[0].commit_sha == "abc123"


class TestGenerateReleaseNotes:
    """Test cases for generate_release_notes function."""

    @patch('tools.release_helper.release_notes.get_previous_tag')
    @patch('tools.release_helper.release_notes.get_commits_between_refs')
    @patch('tools.release_helper.release_notes.filter_commits_by_app')
    @patch('builtins.print')
    def test_generate_release_notes_success(self, mock_print, mock_filter, mock_get_commits, mock_get_previous_tag):
        """Test successful release notes generation."""
        # Setup mocks
        sample_commit = ReleaseNote("abc123", "Fix bug", "John", "2024-01-15", ["file.py"])
        mock_get_previous_tag.return_value = "v0.9.0"
        mock_get_commits.return_value = [sample_commit]
        mock_filter.return_value = [sample_commit]
        
        result = generate_release_notes("hello_python", "v1.0.0")
        
        assert "**Released:**" in result
        assert "**Previous Version:** v0.9.0" in result
        assert "[abc123] Fix bug" in result
        mock_get_commits.assert_called_once_with("v0.9.0", "v1.0.0")

    @patch('tools.release_helper.release_notes.get_previous_tag')
    @patch('tools.release_helper.release_notes.get_commits_between_refs')
    @patch('tools.release_helper.release_notes.filter_commits_by_app')
    @patch('builtins.print')
    def test_generate_release_notes_no_previous_tag(self, mock_print, mock_filter, mock_get_commits, mock_get_previous_tag):
        """Test release notes generation when no previous tag is found."""
        # Setup mocks
        mock_get_previous_tag.return_value = None
        mock_get_commits.return_value = []
        mock_filter.return_value = []
        
        result = generate_release_notes("hello_python", "v1.0.0")
        
        mock_get_commits.assert_called_once_with("HEAD~10", "v1.0.0")
        assert "**Previous Version:** HEAD~10" in result

    @patch('tools.release_helper.release_notes.get_previous_tag')
    @patch('tools.release_helper.release_notes.get_commits_between_refs')
    @patch('tools.release_helper.release_notes.filter_commits_by_app')
    @patch('builtins.print')
    def test_generate_release_notes_explicit_previous_tag(self, mock_print, mock_filter, mock_get_commits, mock_get_previous_tag):
        """Test release notes generation with explicit previous tag."""
        # Setup mocks
        mock_get_commits.return_value = []
        mock_filter.return_value = []
        
        result = generate_release_notes("hello_python", "v1.0.0", previous_tag="v0.8.0")
        
        # Should not call get_previous_tag when explicit tag is provided
        mock_get_previous_tag.assert_not_called()
        mock_get_commits.assert_called_once_with("v0.8.0", "v1.0.0")

    @patch('tools.release_helper.release_notes.get_previous_tag')
    @patch('tools.release_helper.release_notes.get_commits_between_refs')
    @patch('tools.release_helper.release_notes.filter_commits_by_app')
    def test_generate_release_notes_different_formats(self, mock_filter, mock_get_commits, mock_get_previous_tag):
        """Test release notes generation in different formats."""
        # Setup mocks
        sample_commit = ReleaseNote("abc123", "Fix bug", "John", "2024-01-15", ["file.py"])
        mock_get_previous_tag.return_value = "v0.9.0"
        mock_get_commits.return_value = [sample_commit]
        mock_filter.return_value = [sample_commit]
        
        with patch('builtins.print'):  # Mock print to avoid output during test
            # Test markdown format
            markdown_result = generate_release_notes("hello_python", "v1.0.0", format_type="markdown")
            assert "## Changes" in markdown_result
            
            # Test plain format
            plain_result = generate_release_notes("hello_python", "v1.0.0", format_type="plain")
            assert "Changes:" in plain_result
            
            # Test JSON format
            json_result = generate_release_notes("hello_python", "v1.0.0", format_type="json")
            parsed = json.loads(json_result)
            assert parsed["app"] == "hello_python"

    def test_generate_release_notes_invalid_format(self):
        """Test error with invalid format type."""
        with pytest.raises(ValueError, match="Unsupported format type"):
            generate_release_notes("hello_python", "v1.0.0", format_type="invalid")


class TestGenerateReleaseNotesForAllApps:
    """Test cases for generate_release_notes_for_all_apps function."""

    @patch('tools.release_helper.release_notes.list_all_apps')
    @patch('tools.release_helper.release_notes.generate_release_notes')
    def test_generate_release_notes_for_all_apps(self, mock_generate, mock_list_apps, sample_apps):
        """Test generating release notes for all apps."""
        mock_list_apps.return_value = sample_apps
        mock_generate.side_effect = [
            "Release notes for hello_python",
            "Release notes for hello_go"
        ]
        
        result = generate_release_notes_for_all_apps("v1.0.0", "v0.9.0")
        
        assert len(result) == 2
        assert "hello_python" in result
        assert "hello_go" in result
        assert result["hello_python"] == "Release notes for hello_python"
        assert result["hello_go"] == "Release notes for hello_go"
        
        # Verify generate_release_notes was called for each app
        assert mock_generate.call_count == 2

    @patch('tools.release_helper.release_notes.list_all_apps')
    @patch('tools.release_helper.release_notes.generate_release_notes')
    def test_generate_release_notes_for_all_apps_empty_list(self, mock_generate, mock_list_apps):
        """Test generating release notes when no apps exist."""
        mock_list_apps.return_value = []
        
        result = generate_release_notes_for_all_apps("v1.0.0")
        
        assert result == {}
        mock_generate.assert_not_called()

    @patch('tools.release_helper.release_notes.list_all_apps')
    @patch('tools.release_helper.release_notes.generate_release_notes')
    def test_generate_release_notes_for_all_apps_different_format(self, mock_generate, mock_list_apps, sample_apps):
        """Test generating release notes for all apps in different format."""
        mock_list_apps.return_value = sample_apps[:1]  # Just one app
        mock_generate.return_value = '{"app": "hello_python"}'
        
        result = generate_release_notes_for_all_apps("v1.0.0", format_type="json")
        
        # Verify format was passed through
        mock_generate.assert_called_once_with("hello_python", "v1.0.0", None, "json")


if __name__ == "__main__":
    # Run tests if executed directly
    pytest.main([__file__, "-v"])