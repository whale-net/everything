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
from tools.release_helper.release_notes import generate_release_notes, parse_tag_info


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
        # If running in GitHub Actions, we can trust the token has appropriate permissions
        # since the workflow explicitly shows "Contents: write" permission
        if os.getenv('GITHUB_ACTIONS'):
            print("🔍 Running in GitHub Actions environment. Verifying token validity and logging permissions...", file=sys.stderr)
            url = f"{self.base_url}/repos/{self.owner}/{self.repo}"
            with httpx.Client() as client:
                try:
                    response = client.get(url, headers=self.headers, timeout=self.DEFAULT_TIMEOUT)
                    if response.status_code == 200:
                        scopes = response.headers.get("X-OAuth-Scopes", "")
                        print(f"✅ Token is valid. Available OAuth scopes: {scopes}", file=sys.stderr)
                        return True
                    else:
                        print(f"❌ Token is invalid or does not have access to repository {self.owner}/{self.repo}. Status: {response.status_code}", file=sys.stderr)
                        if response.status_code == 404:
                            print("   Repository not found or token doesn't have access", file=sys.stderr)
                        elif response.status_code == 403:
                            print("   Access forbidden - check token permissions", file=sys.stderr)
                        return False
                except httpx.HTTPError as e:
                    print(f"❌ Error validating token in GitHub Actions: {e}", file=sys.stderr)
                    return False
            
        url = f"{self.base_url}/repos/{self.owner}/{self.repo}"
        
        with httpx.Client() as client:
            try:
                response = client.get(url, headers=self.headers, timeout=self.DEFAULT_TIMEOUT)
                if response.status_code == 200:
                    repo_data = response.json()
                    permissions = repo_data.get('permissions', {})
                    
                    # Debug: Print available permissions for troubleshooting
                    if os.getenv('DEBUG_PERMISSIONS'):
                        print(f"🔍 Debug: Available permissions: {permissions}", file=sys.stderr)
                    
                    # Check for various permission patterns that indicate write access
                    # GitHub Actions tokens and PATs may have different permission structures
                    has_write_access = (
                        permissions.get('push', False) or           # Traditional push permission  
                        permissions.get('admin', False) or          # Admin permission
                        permissions.get('maintain', False) or       # Maintain permission
                        permissions.get('contents', 'none') == 'write' or  # Contents write (GitHub Actions)
                        permissions.get('write', False)             # Generic write permission
                    )
                    
                    if has_write_access:
                        return True
                    else:
                        # For GitHub Actions tokens, the permissions might not be reflected
                        # in the repo API response. We'll try a different approach:
                        # Check if we can access the releases endpoint
                        releases_url = f"{self.base_url}/repos/{self.owner}/{self.repo}/releases"
                        releases_response = client.get(releases_url, headers=self.headers, timeout=self.DEFAULT_TIMEOUT)
                        
                        if releases_response.status_code == 200:
                            # If we can access releases, we likely have sufficient permissions
                            print(f"⚠️  Permission validation unclear, but releases endpoint accessible. Proceeding with caution.", file=sys.stderr)
                            return True
                        elif releases_response.status_code == 403:
                            print(f"❌ GitHub token does not have write permissions for {self.owner}/{self.repo}", file=sys.stderr)
                            print("   Ensure the token has 'contents: write' permission or 'repo' scope", file=sys.stderr)
                            print(f"   Available permissions: {permissions}", file=sys.stderr)
                            return False
                        else:
                            print(f"❌ Cannot determine permissions. Releases endpoint status: {releases_response.status_code}", file=sys.stderr)
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
    
    def upload_release_asset(self, release_id: int, file_path: str, asset_name: str, content_type: str = "application/json") -> Optional[Dict]:
        """Upload an asset to a GitHub release.
        
        Args:
            release_id: ID of the release
            file_path: Path to the file to upload
            asset_name: Name for the asset
            content_type: MIME type of the asset
            
        Returns:
            Asset data if successful, None otherwise
        """
        # GitHub requires a different host for upload
        upload_url = f"https://uploads.github.com/repos/{self.owner}/{self.repo}/releases/{release_id}/assets"
        
        try:
            with open(file_path, 'rb') as f:
                file_content = f.read()
            
            upload_headers = {
                "Authorization": f"Bearer {self.token}",
                "Accept": "application/vnd.github.v3+json",
                "Content-Type": content_type,
            }
            
            params = {"name": asset_name}
            
            print(f"Uploading asset {asset_name} to release {release_id}...")
            
            with httpx.Client(timeout=60.0) as client:  # Longer timeout for file upload
                response = client.post(
                    upload_url,
                    headers=upload_headers,
                    params=params,
                    content=file_content
                )
                
                if response.status_code == 201:
                    asset_info = response.json()
                    print(f"✅ Successfully uploaded asset: {asset_info['name']}")
                    return asset_info
                else:
                    print(f"⚠️  Failed to upload asset {asset_name}. Status: {response.status_code}", file=sys.stderr)
                    try:
                        error_data = response.json()
                        print(f"Error: {error_data.get('message', 'Unknown error')}", file=sys.stderr)
                    except json.JSONDecodeError:
                        print(f"Response: {response.text}", file=sys.stderr)
                    return None
                    
        except Exception as e:
            print(f"❌ Failed to upload asset {asset_name}: {e}", file=sys.stderr)
            return None
    
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
    
    def verify_tag_exists(self, tag_name: str, expected_commit_sha: Optional[str] = None, max_retries: int = 3) -> bool:
        """Verify that a git tag exists in the repository and optionally check its commit SHA.
        
        This method implements retry logic to handle cases where a tag was just pushed
        and may not be immediately visible in GitHub's API.
        
        Args:
            tag_name: Git tag name to verify
            expected_commit_sha: Optional commit SHA to verify the tag points to
            max_retries: Maximum number of retry attempts (default: 3)
            
        Returns:
            True if tag exists (and points to expected commit if specified), False otherwise
        """
        import time
        
        url = f"{self.base_url}/repos/{self.owner}/{self.repo}/git/ref/tags/{tag_name}"
        
        for attempt in range(max_retries):
            with httpx.Client() as client:
                try:
                    response = client.get(url, headers=self.headers, timeout=self.DEFAULT_TIMEOUT)
                    
                    if response.status_code == 200:
                        ref_data = response.json()
                        tag_sha = ref_data.get('object', {}).get('sha')
                        
                        if expected_commit_sha:
                            # The tag might point to a tag object (annotated tag) or directly to a commit
                            # For annotated tags, we need to dereference to get the actual commit
                            if ref_data.get('object', {}).get('type') == 'tag':
                                # This is an annotated tag, need to get the tag object to find the commit
                                tag_obj_url = f"{self.base_url}/repos/{self.owner}/{self.repo}/git/tags/{tag_sha}"
                                tag_obj_response = client.get(tag_obj_url, headers=self.headers, timeout=self.DEFAULT_TIMEOUT)
                                if tag_obj_response.status_code == 200:
                                    tag_obj_data = tag_obj_response.json()
                                    actual_commit_sha = tag_obj_data.get('object', {}).get('sha')
                                    if actual_commit_sha and actual_commit_sha.startswith(expected_commit_sha[:7]):
                                        print(f"✅ Verified tag {tag_name} points to commit {expected_commit_sha[:7]}")
                                        return True
                                    else:
                                        print(f"⚠️  Tag {tag_name} exists but points to different commit: {actual_commit_sha[:7]} (expected {expected_commit_sha[:7]})", file=sys.stderr)
                                        return False
                            else:
                                # Lightweight tag pointing directly to commit
                                if tag_sha and tag_sha.startswith(expected_commit_sha[:7]):
                                    print(f"✅ Verified tag {tag_name} points to commit {expected_commit_sha[:7]}")
                                    return True
                                else:
                                    print(f"⚠️  Tag {tag_name} exists but points to different commit: {tag_sha[:7]} (expected {expected_commit_sha[:7]})", file=sys.stderr)
                                    return False
                        else:
                            # Just check if tag exists, don't verify commit
                            print(f"✅ Verified tag {tag_name} exists")
                            return True
                    
                    elif response.status_code == 404:
                        if attempt < max_retries - 1:
                            print(f"⏳ Tag {tag_name} not found (attempt {attempt + 1}/{max_retries}), retrying in 2 seconds...", file=sys.stderr)
                            time.sleep(2)
                            continue
                        else:
                            print(f"❌ Tag {tag_name} not found after {max_retries} attempts", file=sys.stderr)
                            return False
                    else:
                        print(f"❌ Unexpected response when checking tag {tag_name}: {response.status_code}", file=sys.stderr)
                        return False
                        
                except httpx.HTTPError as e:
                    if attempt < max_retries - 1:
                        print(f"⚠️  Error checking tag {tag_name} (attempt {attempt + 1}/{max_retries}): {e}", file=sys.stderr)
                        time.sleep(2)
                        continue
                    else:
                        print(f"❌ Failed to verify tag {tag_name} after {max_retries} attempts: {e}", file=sys.stderr)
                        return False
        
        return False


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
    
    This function creates a GitHub release and verifies that the git tag exists
    before creating the release. This prevents GitHub from creating a new tag
    if there's a timing issue where the tag was just pushed but isn't visible yet.
    
    Args:
        app_name: Name of the app
        tag_name: Git tag name for the release (should already exist in the repository)
        release_notes: Release notes content
        owner: Repository owner
        repo: Repository name
        commit_sha: Specific commit SHA to target (used for tag verification)
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
        
        # Verify that the git tag exists before creating the release
        # This prevents GitHub from creating a new lightweight tag instead of using the existing annotated tag
        if commit_sha and not client.verify_tag_exists(tag_name, commit_sha):
            print(f"⚠️  Warning: Git tag {tag_name} not found or doesn't point to commit {commit_sha}", file=sys.stderr)
            print(f"   GitHub will create a new tag at {commit_sha} if release is created", file=sys.stderr)
        
        # Create release title using better format
        try:
            domain, parsed_app_name, version = parse_tag_info(tag_name)
            release_title = f"{domain} {parsed_app_name} {version}"
        except ValueError:
            # Fallback to original format if tag parsing fails
            release_title = f"{app_name} {tag_name}"
        
        # Create release data
        release_data = GitHubReleaseData(
            tag_name=tag_name,
            name=release_title,
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
    version: Optional[str] = None,
    owner: str = "",
    repo: str = "",
    commit_sha: Optional[str] = None,
    prerelease: bool = False,
    previous_tag: Optional[str] = None,
    token: Optional[str] = None,
    release_notes_dir: Optional[str] = None,
    app_versions: Optional[Dict[str, str]] = None,
    openapi_specs_dir: Optional[str] = None
) -> Dict[str, Optional[Dict]]:
    """Create GitHub releases for multiple apps using pre-generated release notes from files.
    
    Args:
        app_list: List of app names to create releases for
        version: Release version (used for all apps if app_versions not provided)
        owner: Repository owner
        repo: Repository name
        commit_sha: Specific commit SHA to target
        prerelease: Whether this is a prerelease
        previous_tag: Previous tag to compare against (auto-detected if not provided)
        token: GitHub token (defaults to GITHUB_TOKEN env var)
        release_notes_dir: Directory containing pre-generated release notes files
        app_versions: Optional dictionary mapping app names to their individual versions
        openapi_specs_dir: Directory containing OpenAPI spec files to upload as release assets
        
    Returns:
        Dictionary mapping app names to their release data (None if failed)
    """
    
    results = {}
    
    # Determine if we're using individual versions or a single version
    using_individual_versions = app_versions is not None
    
    if using_individual_versions:
        print(f"Creating GitHub releases for {len(app_list)} apps with individual versions...")
    else:
        if not version:
            raise ValueError("Either 'version' or 'app_versions' must be provided")
        print(f"Creating GitHub releases for {len(app_list)} apps using pre-generated release notes...")
    
    for app_name in app_list:
        try:
            print(f"Processing {app_name}...")
            
            # Determine the version for this app
            if using_individual_versions:
                app_version = app_versions.get(app_name)
                if not app_version:
                    print(f"❌ No version found for {app_name} in app_versions: {app_versions}", file=sys.stderr)
                    results[app_name] = None
                    continue
            else:
                app_version = version
            
            # Find the app's metadata to determine the tag format
            try:
                bazel_target = find_app_bazel_target(app_name)
                metadata = get_app_metadata(bazel_target)
                domain = metadata['domain']
                tag_name = f"{domain}-{app_name}.{app_version}"
            except Exception as e:
                print(f"❌ Could not determine tag format for {app_name}: {e}", file=sys.stderr)
                results[app_name] = None
                continue
            
            # Try to load pre-generated release notes first
            release_notes = None
            if release_notes_dir:
                notes_file = Path(release_notes_dir) / f"{domain}-{app_name}.md"
                if notes_file.exists():
                    try:
                        release_notes = notes_file.read_text(encoding='utf-8')
                        print(f"✅ Using pre-generated release notes for {domain}-{app_name}")
                    except Exception as e:
                        print(f"⚠️  Failed to read pre-generated release notes for {domain}-{app_name}: {e}", file=sys.stderr)
            
            # Fall back to generating release notes if not found or failed to load
            if not release_notes:
                try:
                    print(f"Generating release notes for {domain}-{app_name} (no pre-generated notes found)")
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
            
            # Upload OpenAPI spec if available and release was created successfully
            if result and openapi_specs_dir and result.get('id'):
                openapi_spec_file = Path(openapi_specs_dir) / f"{domain}-{app_name}-openapi.json"
                if openapi_spec_file.exists():
                    try:
                        print(f"Uploading OpenAPI spec for {app_name}...")
                        client = GitHubReleaseClient(owner, repo, token)
                        asset_name = f"{domain}-{app_name}-openapi.json"
                        client.upload_release_asset(
                            release_id=result['id'],
                            file_path=str(openapi_spec_file),
                            asset_name=asset_name,
                            content_type="application/json"
                        )
                    except Exception as e:
                        print(f"⚠️  Failed to upload OpenAPI spec for {app_name}: {e}", file=sys.stderr)
                        # Don't fail the release if asset upload fails
                else:
                    print(f"ℹ️  No OpenAPI spec found for {app_name} at {openapi_spec_file}")
            
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