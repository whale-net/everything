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

    def test_parse_tag_info_missing_version_prefix(self):
        """Test error handling for invalid tag format (safety guard)."""
        with pytest.raises(ValueError, match="Invalid tag format"):
            parse_tag_info("demo-hello_python1.0.0")

    def test_parse_tag_info_missing_dash(self):
        """Test error handling for missing dash separator (safety guard)."""
        with pytest.raises(ValueError, match="Invalid tag format"):
            parse_tag_info("demohello_python.v1.0.0")


class TestValidateTagFormat:
    """Test cases for validate_tag_format function."""

    def test_validate_tag_format_valid_tags(self):
        """Test validation of valid tag formats."""
        valid_tags = [
            "demo-hello_python.v1.0.0",
            "api-service-status.v2.1.3",
            "my_domain-my_app.v0.1.0-alpha"
        ]
        
        for tag in valid_tags:
            assert validate_tag_format(tag), f"Expected {tag} to be valid"

    def test_validate_tag_format_invalid_tags(self):
        """Test validation of invalid tag formats (safety guards)."""
        invalid_tags = [
            "demo-hello_python1.0.0",  # Missing .v
            "demohello_python.v1.0.0",  # Missing dash
            "",  # Empty string
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
        
        # Test essential structure rather than exact formatting
        assert "**Released:**" in result
        assert "**Previous Version:**" in result
        assert "## Changes" in result
        assert sample_app_release_data.commits[0].commit_message in result

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
        """Test markdown formatting with many files (truncation safety guard)."""
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
        
        # Test the important safety guard: truncation logic
        assert "more files" in result  # Should show truncation message

    def test_to_plain_text_invalid_tag_format(self):
        """Test plain text formatting with invalid tag format (fallback safety guard)."""
        data = AppReleaseData(
            app_name="hello_python",
            current_tag="invalid-tag-format",  # Invalid format
            previous_tag="v0.9.0",
            released_at="2024-01-15",
            commits=[]
        )
        
        result = ReleaseNotesFormatter.to_plain_text(data)
        
        # Test safety guard: fallback behavior for invalid tags
        assert "Release Notes:" in result and "hello_python" in result

    def test_to_json_with_changes(self, sample_app_release_data):
        """Test JSON formatting with changes."""
        result = ReleaseNotesFormatter.to_json(sample_app_release_data)
        
        # Test that it's valid JSON and has essential structure
        data = json.loads(result)
        assert data["app"] == "hello_python"
        assert data["commit_count"] == 1
        assert len(data["changes"]) == 1

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
    def test_get_commits_between_refs_ref_not_found(self, mock_subprocess_run, mock_print):
        """Test behavior when start ref doesn't exist."""
        mock_subprocess_run.side_effect = [
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
    def test_get_commits_between_refs_git_error(self, mock_subprocess_run, mock_print):
        """Test error handling when git command fails."""
        mock_subprocess_run.side_effect = [
            Mock(returncode=0),  # rev-parse check
            subprocess.CalledProcessError(1, "git log")  # git log fails
        ]
        
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
    def test_filter_commits_by_app_not_found(self, mock_list_apps, sample_apps, mock_print):
        """Test behavior when app is not found."""
        mock_list_apps.return_value = sample_apps
        
        commits = [
            ReleaseNote("abc123", "Fix bug", "John", "2024-01-15", ["demo/hello_python/main.py"])
        ]
        
        result = filter_commits_by_app(commits, "nonexistent_app")
        
        assert len(result) == 0

    @patch('tools.release_helper.release_notes.list_all_apps')
    def test_filter_commits_by_app_exception_handling(self, mock_list_apps, mock_print):
        """Test exception handling in commit filtering."""
        mock_list_apps.side_effect = Exception("List apps failed")
        
        commits = [
            ReleaseNote("abc123", "Fix bug", "John", "2024-01-15", ["demo/hello_python/main.py"])
        ]
        
        result = filter_commits_by_app(commits, "hello_python")
        
        # Should return all commits if filtering fails
        assert len(result) == 1
        assert result[0].commit_sha == "abc123"


class TestGenerateReleaseNotes:
    """Test cases for generate_release_notes function."""

    @patch('tools.release_helper.release_notes.get_previous_tag')
    @patch('tools.release_helper.release_notes.get_commits_between_refs')
    @patch('tools.release_helper.release_notes.filter_commits_by_app')
    def test_generate_release_notes_success(self, mock_filter, mock_get_commits, mock_get_previous_tag, mock_print):
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
    def test_generate_release_notes_no_previous_tag(self, mock_filter, mock_get_commits, mock_get_previous_tag, mock_print):
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
    def test_generate_release_notes_different_formats(self, mock_filter, mock_get_commits, mock_get_previous_tag, mock_print):
        """Test release notes generation works for different formats (safety guard)."""
        # Setup mocks
        sample_commit = ReleaseNote("abc123", "Fix bug", "John", "2024-01-15", ["file.py"])
        mock_get_previous_tag.return_value = "v0.9.0"
        mock_get_commits.return_value = [sample_commit]
        mock_filter.return_value = [sample_commit]
        
        # Test that all formats work without error
        for format_type in ["markdown", "plain", "json"]:
            result = generate_release_notes("hello_python", "v1.0.0", format_type=format_type)
            assert result  # Just ensure it produces output without error

    def test_generate_release_notes_invalid_format(self):
        """Test error handling for invalid format type (safety guard)."""
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