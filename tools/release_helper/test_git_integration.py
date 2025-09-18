"""
Integration tests for git operations using pytest-git.
These tests use actual git repositories to test real git interactions.
"""

import os
import subprocess
import tempfile
from contextlib import contextmanager
from pathlib import Path
import pytest

from tools.release_helper.git import (
    format_git_tag,
    create_git_tag,
    get_previous_tag,
)
from tools.release_helper.changes import _get_changed_files


@contextmanager
def chdir(path):
    """Context manager for temporarily changing directory."""
    original_cwd = os.getcwd()
    try:
        os.chdir(path)
        yield
    finally:
        os.chdir(original_cwd)


class TestGitIntegration:
    """Integration tests with real git repositories."""

    def test_create_and_get_tag_integration(self, git_repo):
        """Test creating a tag and then retrieving the previous tag."""
        # Configure git user for commits
        git_repo.run(["git", "config", "user.email", "test@example.com"])
        git_repo.run(["git", "config", "user.name", "Test User"])
        
        # Create initial commit
        test_file = git_repo.workspace / "test.txt"
        test_file.write_text("initial content")
        git_repo.run(["git", "add", "test.txt"])
        git_repo.run(["git", "commit", "-m", "Initial commit"])
        
        # Create first tag
        tag_name = "v1.0.0"
        
        # Change to the git repo directory for tag creation
        with chdir(git_repo.workspace):
            create_git_tag(tag_name, message="First release")
        
        # Verify tag was created
        result = git_repo.run(["git", "tag", "-l"], capture=True)
        assert tag_name in result
        
        # Create second commit
        test_file.write_text("updated content")
        git_repo.run(["git", "add", "test.txt"])
        git_repo.run(["git", "commit", "-m", "Second commit"])
        
        # Create second tag
        tag_name2 = "v2.0.0"
        with chdir(git_repo.workspace):
            create_git_tag(tag_name2, message="Second release")
        
        # Test getting previous tag
        with chdir(git_repo.workspace):
            previous_tag = get_previous_tag()
        
        assert previous_tag == tag_name

    def test_get_previous_tag_no_tags(self, git_repo):
        """Test get_previous_tag when no tags exist."""
        # Configure git user for commits
        git_repo.run(["git", "config", "user.email", "test@example.com"])
        git_repo.run(["git", "config", "user.name", "Test User"])
        
        # Create initial commit
        test_file = git_repo.workspace / "test.txt"
        test_file.write_text("initial content")
        git_repo.run(["git", "add", "test.txt"])
        git_repo.run(["git", "commit", "-m", "Initial commit"])
        
        # Test getting previous tag when no tags exist
        with chdir(git_repo.workspace):
            previous_tag = get_previous_tag()
        
        assert previous_tag is None

    def test_get_changed_files_integration(self, git_repo):
        """Test getting changed files with real git operations."""
        # Configure git user for commits
        git_repo.run(["git", "config", "user.email", "test@example.com"])
        git_repo.run(["git", "config", "user.name", "Test User"])
        
        # Create initial commit
        file1 = git_repo.workspace / "file1.py"
        file1.write_text("print('hello')")
        git_repo.run(["git", "add", "file1.py"])
        git_repo.run(["git", "commit", "-m", "Initial commit"])
        
        # Get the commit SHA for base comparison
        result = git_repo.run(["git", "rev-parse", "HEAD"], capture=True)
        base_commit = result.strip()
        
        # Create some changes
        file2 = git_repo.workspace / "file2.go"
        file2.write_text("package main")
        subdir = git_repo.workspace / "subdir"
        subdir.mkdir()
        file3 = subdir / "file3.yaml"
        file3.write_text("key: value")
        
        git_repo.run(["git", "add", "file2.go", "subdir/file3.yaml"])
        git_repo.run(["git", "commit", "-m", "Add new files"])
        
        # Test getting changed files
        with chdir(git_repo.workspace):
            changed_files = _get_changed_files(base_commit)
        
        assert "file2.go" in changed_files
        assert "subdir/file3.yaml" in changed_files
        assert len(changed_files) == 2

    def test_format_git_tag_with_real_repo(self, git_repo):
        """Test format_git_tag function - this doesn't need git but tests in context."""
        # Test various tag formats that might be used in real scenarios
        assert format_git_tag("api", "user-service", "1.0.0") == "api-user-service-1.0.0"
        assert format_git_tag("web", "frontend", "2.1.0-beta") == "web-frontend-2.1.0-beta"
        assert format_git_tag("data", "ml-pipeline", "1.2.3") == "data-ml-pipeline-1.2.3"

    def test_create_tag_with_commit_sha(self, git_repo):
        """Test creating a tag on a specific commit SHA."""
        # Configure git user for commits
        git_repo.run(["git", "config", "user.email", "test@example.com"])
        git_repo.run(["git", "config", "user.name", "Test User"])
        
        # Create first commit
        file1 = git_repo.workspace / "file1.txt"
        file1.write_text("first")
        git_repo.run(["git", "add", "file1.txt"])
        git_repo.run(["git", "commit", "-m", "First commit"])
        
        # Get the commit SHA
        result = git_repo.run(["git", "rev-parse", "HEAD"], capture=True)
        first_commit_sha = result.strip()
        
        # Create second commit
        file1.write_text("second")
        git_repo.run(["git", "add", "file1.txt"])
        git_repo.run(["git", "commit", "-m", "Second commit"])
        
        # Create tag on first commit
        tag_name = "v1.0.0"
        with chdir(git_repo.workspace):
            create_git_tag(tag_name, commit_sha=first_commit_sha, message="Tag on first commit")
        
        # Verify the tag points to the correct commit
        result = git_repo.run(["git", "rev-list", "-n", "1", tag_name], capture=True)
        tag_commit_sha = result.strip()
        assert tag_commit_sha == first_commit_sha

    def test_git_operations_error_handling(self):
        """Test error handling in git operations."""
        # Test get_previous_tag in a repository with no commits using separate temp directory
        with tempfile.TemporaryDirectory() as temp_dir:
            empty_repo_path = Path(temp_dir) / "empty_repo"
            empty_repo_path.mkdir()
            
            with chdir(empty_repo_path):
                # Initialize empty repo
                subprocess.run(["git", "init"], check=True, capture_output=True)
                
                # This should return None since there are no commits
                previous_tag = get_previous_tag()
                assert previous_tag is None