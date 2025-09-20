"""
GitHub release creation utilities for the release helper.
"""

import json
import os
import sys
from typing import Dict, Optional, List
from dataclasses import dataclass
from pathlib import Path

import httpx

from tools.release_helper.metadata import get_app_metadata, list_all_apps
from tools.release_helper.release import find_app_bazel_target
from tools.release_helper.release_notes import generate_release_notes


@dataclass
class GitHubReleaseData:
    """Represents data for creating a GitHub release."""
    tag_name: str
    name: str
    body: str
    draft: bool = False
    prerelease: bool = False
    target_commitish: Optional[str] = None


class GitHubReleaseClient:
    """Client for interacting with GitHub Releases API."""
    
    DEFAULT_TIMEOUT = 30.0  # Default timeout for HTTP requests in seconds
    
    def __init__(self, owner: str, repo: str, token: Optional[str] = None):
        """Initialize the GitHub release client.
        
        Args:
            owner: Repository owner
            repo: Repository name
            token: GitHub token (defaults to GITHUB_TOKEN env var)
        """
        self.owner = owner
        self.repo = repo
        self.token = token or os.getenv('GITHUB_TOKEN')
        
        if not self.token:
            raise ValueError("GitHub token is required. Set GITHUB_TOKEN environment variable.")
        
        self.base_url = "https://api.github.com"
        self.headers = {
            "Authorization": f"Bearer {self.token}",
            "Accept": "application/vnd.github.v3+json",
            "Content-Type": "application/json"
        }
    
    def validate_permissions(self) -> bool:
        """Validate that the GitHub token has the necessary permissions.
        
        Returns:
            True if token has necessary permissions, False otherwise
        """
        url = f"{self.base_url}/repos/{self.owner}/{self.repo}"
        
        with httpx.Client() as client:
            try:
                response = client.get(url, headers=self.headers, timeout=self.DEFAULT_TIMEOUT)
                if response.status_code == 200:
                    repo_data = response.json()
                    permissions = repo_data.get('permissions', {})
                    
                    # Check if we have write permissions (needed for creating releases)
                    if permissions.get('push', False) or permissions.get('admin', False):
                        return True
                    else:
                        print(f"❌ GitHub token does not have write permissions for {self.owner}/{self.repo}", file=sys.stderr)
                        print("   Ensure the token has 'repo' or 'public_repo' scope", file=sys.stderr)
                        return False
                else:
                    print(f"❌ Cannot access repository {self.owner}/{self.repo}. Status: {response.status_code}", file=sys.stderr)
                    if response.status_code == 404:
                        print("   Repository not found or token doesn't have access", file=sys.stderr)
                    elif response.status_code == 403:
                        print("   Access forbidden - check token permissions", file=sys.stderr)
                    return False
            except httpx.HTTPError as e:
                print(f"❌ Error validating permissions: {e}", file=sys.stderr)
                return False
    
    def create_release(self, release_data: GitHubReleaseData) -> Dict:
        """Create a GitHub release.
        
        Args:
            release_data: Release data to create
            
        Returns:
            GitHub API response as dictionary
            
        Raises:
            httpx.HTTPError: If the API request fails
        """
        url = f"{self.base_url}/repos/{self.owner}/{self.repo}/releases"
        
        payload = {
            "tag_name": release_data.tag_name,
            "name": release_data.name,
            "body": release_data.body,
            "draft": release_data.draft,
            "prerelease": release_data.prerelease
        }
        
        if release_data.target_commitish:
            payload["target_commitish"] = release_data.target_commitish
        
        print(f"Creating GitHub release: {release_data.name} ({release_data.tag_name})")
        
        with httpx.Client() as client:
            response = client.post(url, headers=self.headers, json=payload, timeout=self.DEFAULT_TIMEOUT)
            
            if response.status_code == 201:
                release_info = response.json()
                print(f"✅ Successfully created GitHub release: {release_info['html_url']}")
                return release_info
            elif response.status_code == 422:
                # Release might already exist
                try:
                    error_data = response.json()
                    error_msg = error_data.get('message', 'Unknown error')
                    if 'already_exists' in error_msg.lower() or 'already exists' in error_msg.lower():
                        print(f"ℹ️  Release {release_data.tag_name} already exists, skipping creation")
                        return {"message": "Release already exists", "tag_name": release_data.tag_name}
                    else:
                        print(f"❌ Failed to create release: {error_msg}", file=sys.stderr)
                        if 'errors' in error_data:
                            for error in error_data['errors']:
                                print(f"   - {error.get('message', error)}", file=sys.stderr)
                        response.raise_for_status()
                except json.JSONDecodeError:
                    print(f"❌ Failed to create release. Status: {response.status_code}", file=sys.stderr)
                    print(f"Response: {response.text}", file=sys.stderr)
                    response.raise_for_status()
            else:
                print(f"❌ Failed to create GitHub release. Status: {response.status_code}", file=sys.stderr)
                try:
                    error_data = response.json()
                    print(f"Error: {error_data.get('message', 'Unknown error')}", file=sys.stderr)
                except json.JSONDecodeError:
                    print(f"Response: {response.text}", file=sys.stderr)
                response.raise_for_status()
    
    def get_release_by_tag(self, tag_name: str) -> Optional[Dict]:
        """Get a release by tag name.
        
        Args:
            tag_name: Git tag name
            
        Returns:
            Release data if found, None otherwise
        """
        url = f"{self.base_url}/repos/{self.owner}/{self.repo}/releases/tags/{tag_name}"
        
        with httpx.Client() as client:
            try:
                response = client.get(url, headers=self.headers, timeout=self.DEFAULT_TIMEOUT)
                if response.status_code == 200:
                    return response.json()
                else:
                    return None
            except httpx.HTTPError:
                return None
    
    def list_releases(self, per_page: int = 30) -> List[Dict]:
        """List releases for the repository.
        
        Args:
            per_page: Number of releases to fetch per page
            
        Returns:
            List of release data
        """
        url = f"{self.base_url}/repos/{self.owner}/{self.repo}/releases"
        params = {"per_page": per_page}
        
        with httpx.Client() as client:
            try:
                response = client.get(url, headers=self.headers, params=params, timeout=self.DEFAULT_TIMEOUT)
                response.raise_for_status()
                return response.json()
            except httpx.HTTPError as e:
                print(f"❌ Failed to list releases: {e}", file=sys.stderr)
                return []


def create_app_release(
    app_name: str,
    tag_name: str,
    release_notes: str,
    owner: str,
    repo: str,
    commit_sha: Optional[str] = None,
    prerelease: bool = False,
    token: Optional[str] = None
) -> Optional[Dict]:
    """Create a GitHub release for a specific app.
    
    Args:
        app_name: Name of the app
        tag_name: Git tag name for the release
        release_notes: Release notes content
        owner: Repository owner
        repo: Repository name
        commit_sha: Specific commit SHA to target
        prerelease: Whether this is a prerelease
        token: GitHub token (defaults to GITHUB_TOKEN env var)
        
    Returns:
        GitHub release data if successful, None otherwise
    """
    try:
        client = GitHubReleaseClient(owner, repo, token)
        
        # Validate permissions first
        if not client.validate_permissions():
            print(f"❌ Insufficient permissions to create releases in {owner}/{repo}", file=sys.stderr)
            return None
        
        # Check if release already exists
        existing_release = client.get_release_by_tag(tag_name)
        if existing_release:
            print(f"ℹ️  Release {tag_name} already exists: {existing_release['html_url']}")
            return existing_release
        
        # Create release data
        release_data = GitHubReleaseData(
            tag_name=tag_name,
            name=f"{app_name} {tag_name}",
            body=release_notes,
            draft=False,
            prerelease=prerelease,
            target_commitish=commit_sha
        )
        
        return client.create_release(release_data)
        
    except Exception as e:
        print(f"❌ Failed to create GitHub release for {app_name}: {e}", file=sys.stderr)
        return None


def create_releases_for_apps(
    app_list: List[str],
    version: str,
    owner: str,
    repo: str,
    commit_sha: Optional[str] = None,
    prerelease: bool = False,
    previous_tag: Optional[str] = None,
    token: Optional[str] = None
) -> Dict[str, Optional[Dict]]:
    """Create GitHub releases for multiple apps by calling create_app_release for each.
    
    Args:
        app_list: List of app names to create releases for
        version: Release version
        owner: Repository owner
        repo: Repository name
        commit_sha: Specific commit SHA to target
        prerelease: Whether this is a prerelease
        previous_tag: Previous tag to compare against (auto-detected if not provided)
        token: GitHub token (defaults to GITHUB_TOKEN env var)
        
    Returns:
        Dictionary mapping app names to their release data (None if failed)
    """
    from tools.release_helper.metadata import get_app_metadata, list_all_apps
    from tools.release_helper.release import find_app_bazel_target
    from tools.release_helper.release_notes import generate_release_notes
    
    results = {}
    
    print(f"Creating GitHub releases for {len(app_list)} apps...")
    
    for app_name in app_list:
        try:
            print(f"Processing {app_name}...")
            
            # Find the app's metadata to determine the tag format
            try:
                bazel_target = find_app_bazel_target(app_name)
                metadata = get_app_metadata(bazel_target)
                domain = metadata['domain']
                tag_name = f"{domain}-{app_name}.{version}"
            except Exception as e:
                print(f"❌ Could not determine tag format for {app_name}: {e}", file=sys.stderr)
                results[app_name] = None
                continue
            
            # Generate release notes for this app
            try:
                release_notes = generate_release_notes(app_name, tag_name, previous_tag, "markdown")
            except Exception as e:
                print(f"❌ Failed to generate release notes for {app_name}: {e}", file=sys.stderr)
                results[app_name] = None
                continue
            
            # Create the individual app release
            result = create_app_release(
                app_name=app_name,
                tag_name=tag_name,
                release_notes=release_notes,
                owner=owner,
                repo=repo,
                commit_sha=commit_sha,
                prerelease=prerelease,
                token=token
            )
            
            results[app_name] = result
            
        except Exception as e:
            print(f"❌ Failed to create release for {app_name}: {e}", file=sys.stderr)
            results[app_name] = None
    
    # Report summary
    successful_count = sum(1 for result in results.values() if result is not None)
    failed_count = len(app_list) - successful_count
    
    print(f"✅ Successfully created {successful_count} releases")
    if failed_count > 0:
        print(f"❌ Failed to create {failed_count} releases")
    
    return results


def create_releases_for_apps_with_notes(
    app_list: List[str],
    version: str,
    owner: str,
    repo: str,
    commit_sha: Optional[str] = None,
    prerelease: bool = False,
    previous_tag: Optional[str] = None,
    token: Optional[str] = None,
    release_notes_dir: Optional[str] = None
) -> Dict[str, Optional[Dict]]:
    """Create GitHub releases for multiple apps using pre-generated release notes from files.
    
    Args:
        app_list: List of app names to create releases for
        version: Release version
        owner: Repository owner
        repo: Repository name
        commit_sha: Specific commit SHA to target
        prerelease: Whether this is a prerelease
        previous_tag: Previous tag to compare against (auto-detected if not provided)
        token: GitHub token (defaults to GITHUB_TOKEN env var)
        release_notes_dir: Directory containing pre-generated release notes files
        
    Returns:
        Dictionary mapping app names to their release data (None if failed)
    """
    
    results = {}
    
    print(f"Creating GitHub releases for {len(app_list)} apps using pre-generated release notes...")
    
    for app_name in app_list:
        try:
            print(f"Processing {app_name}...")
            
            # Find the app's metadata to determine the tag format
            try:
                bazel_target = find_app_bazel_target(app_name)
                metadata = get_app_metadata(bazel_target)
                domain = metadata['domain']
                tag_name = f"{domain}-{app_name}.{version}"
            except Exception as e:
                print(f"❌ Could not determine tag format for {app_name}: {e}", file=sys.stderr)
                results[app_name] = None
                continue
            
            # Try to load pre-generated release notes first
            release_notes = None
            if release_notes_dir:
                notes_file = Path(release_notes_dir) / f"{app_name}.md"
                if notes_file.exists():
                    try:
                        release_notes = notes_file.read_text(encoding='utf-8')
                        print(f"✅ Using pre-generated release notes for {app_name}")
                    except Exception as e:
                        print(f"⚠️  Failed to read pre-generated release notes for {app_name}: {e}", file=sys.stderr)
            
            # Fall back to generating release notes if not found or failed to load
            if not release_notes:
                try:
                    print(f"Generating release notes for {app_name} (no pre-generated notes found)")
                    release_notes = generate_release_notes(app_name, tag_name, previous_tag, "markdown")
                except Exception as e:
                    print(f"❌ Failed to generate release notes for {app_name}: {e}", file=sys.stderr)
                    results[app_name] = None
                    continue
            
            # Create the individual app release
            result = create_app_release(
                app_name=app_name,
                tag_name=tag_name,
                release_notes=release_notes,
                owner=owner,
                repo=repo,
                commit_sha=commit_sha,
                prerelease=prerelease,
                token=token
            )
            
            results[app_name] = result
            
        except Exception as e:
            print(f"❌ Failed to process {app_name}: {e}", file=sys.stderr)
            results[app_name] = None
    
    # Report summary
    successful_count = sum(1 for result in results.values() if result is not None)
    failed_count = len(app_list) - successful_count
    
    print(f"✅ Successfully created {successful_count} releases")
    if failed_count > 0:
        print(f"❌ Failed to create {failed_count} releases")
    
    return results