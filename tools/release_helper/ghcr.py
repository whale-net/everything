"""
GitHub Container Registry (GHCR) management utilities.

This module provides functionality to interact with GitHub Container Registry
for managing container packages and versions.
"""

import os
import sys
from dataclasses import dataclass
from typing import Dict, List, Optional

import httpx


@dataclass
class GHCRPackageVersion:
    """Represents a GHCR package version.
    
    Attributes:
        version_id: Unique ID for this version
        tags: List of tags associated with this version
        created_at: ISO 8601 timestamp when version was created
        updated_at: ISO 8601 timestamp when version was last updated
    """
    version_id: int
    tags: List[str]
    created_at: Optional[str] = None
    updated_at: Optional[str] = None

    def has_tag(self, tag: str) -> bool:
        """Check if this version has a specific tag.
        
        Args:
            tag: Tag to check for
            
        Returns:
            True if version has the tag
        """
        return tag in self.tags

    def is_untagged(self) -> bool:
        """Check if this version has no tags.
        
        Returns:
            True if version has no tags
        """
        return len(self.tags) == 0

    def __repr__(self) -> str:
        """String representation of version."""
        tags_str = ", ".join(self.tags) if self.tags else "untagged"
        return f"GHCRPackageVersion(id={self.version_id}, tags=[{tags_str}])"


class GHCRClient:
    """Client for interacting with GitHub Container Registry API.
    
    This client handles authentication, API requests, and provides methods
    for listing and deleting package versions in GHCR.
    """

    DEFAULT_TIMEOUT = 30.0  # Default timeout for HTTP requests in seconds
    
    def __init__(self, owner: str, token: Optional[str] = None):
        """Initialize the GHCR client.
        
        Args:
            owner: Repository owner (organization or user)
            token: GitHub token (defaults to GITHUB_TOKEN env var)
            
        Raises:
            ValueError: If no token is provided or found in environment
        """
        self.owner = owner
        self.token = token or os.getenv('GITHUB_TOKEN')
        
        if not self.token:
            raise ValueError("GitHub token is required. Set GITHUB_TOKEN environment variable.")
        
        self.base_url = "https://api.github.com"
        self.headers = {
            "Authorization": f"Bearer {self.token}",
            "Accept": "application/vnd.github.v3+json",
            "Content-Type": "application/json"
        }
        
        # Cache for owner type (orgs vs users)
        self._owner_type_cache: Optional[str] = None

    def _detect_owner_type(self) -> str:
        """Detect if owner is an organization or user.
        
        Returns:
            "orgs" if owner is an organization, "users" if user
        """
        if self._owner_type_cache:
            return self._owner_type_cache
        
        # Try to get owner info to determine type
        url = f"{self.base_url}/users/{self.owner}"
        
        with httpx.Client() as client:
            try:
                response = client.get(url, headers=self.headers, timeout=self.DEFAULT_TIMEOUT)
                if response.status_code == 200:
                    data = response.json()
                    owner_type = "orgs" if data.get("type") == "Organization" else "users"
                    self._owner_type_cache = owner_type
                    return owner_type
                else:
                    # Default to orgs if we can't determine
                    print(f"⚠️  Could not determine owner type, defaulting to 'orgs'", file=sys.stderr)
                    self._owner_type_cache = "orgs"
                    return "orgs"
            except httpx.HTTPError as e:
                print(f"⚠️  Error detecting owner type: {e}, defaulting to 'orgs'", file=sys.stderr)
                self._owner_type_cache = "orgs"
                return "orgs"

    def list_package_versions(self, package_name: str, 
                             package_type: str = "container") -> List[GHCRPackageVersion]:
        """List all versions of a package.
        
        Args:
            package_name: Package name (e.g., "demo-hello-python")
            package_type: Package type (default: "container")
            
        Returns:
            List of package versions with their metadata
        """
        owner_type = self._detect_owner_type()
        url = f"{self.base_url}/{owner_type}/{self.owner}/packages/{package_type}/{package_name}/versions"
        
        all_versions = []
        params = {"per_page": 100}  # Max per page
        
        with httpx.Client() as client:
            while True:
                try:
                    response = client.get(url, headers=self.headers, params=params, timeout=self.DEFAULT_TIMEOUT)
                    
                    if response.status_code == 404:
                        # Package doesn't exist or no versions
                        print(f"ℹ️  Package {package_name} not found or has no versions", file=sys.stderr)
                        return []
                    
                    if response.status_code == 200:
                        versions_data = response.json()
                        
                        # Parse versions
                        for version_data in versions_data:
                            # Skip None or invalid version data
                            if version_data is None or not isinstance(version_data, dict):
                                continue
                            
                            # Ensure version has an ID (required field)
                            version_id = version_data.get("id")
                            if version_id is None:
                                print(f"⚠️  Skipping package version without ID", file=sys.stderr)
                                continue
                            
                            # Safely extract tags from nested structure
                            metadata = version_data.get("metadata")
                            tags = []
                            if metadata is not None:
                                container = metadata.get("container")
                                if container is not None:
                                    tags = container.get("tags", [])
                            
                            version = GHCRPackageVersion(
                                version_id=version_id,
                                tags=tags,
                                created_at=version_data.get("created_at"),
                                updated_at=version_data.get("updated_at")
                            )
                            all_versions.append(version)
                        
                        # Check for pagination
                        link_header = response.headers.get("Link", "")
                        if 'rel="next"' not in link_header:
                            break  # No more pages
                        
                        # Extract next page URL from Link header
                        # Format: <url>; rel="next", <url>; rel="last"
                        for link in link_header.split(","):
                            if 'rel="next"' in link:
                                next_url = link.split(";")[0].strip("<>").strip()
                                url = next_url
                                params = {}  # Next URL already has params
                                break
                    else:
                        response.raise_for_status()
                        
                except httpx.HTTPError as e:
                    print(f"❌ Error listing package versions: {e}", file=sys.stderr)
                    raise
        
        return all_versions

    def delete_package_version(self, package_name: str, version_id: int,
                               package_type: str = "container") -> bool:
        """Delete a specific package version.
        
        Args:
            package_name: Package name (e.g., "demo-hello-python")
            version_id: Version ID from list_package_versions
            package_type: Package type (default: "container")
            
        Returns:
            True if deletion successful, False if version not found
            
        Raises:
            httpx.HTTPStatusError: If deletion fails (e.g., permission denied)
        """
        owner_type = self._detect_owner_type()
        url = f"{self.base_url}/{owner_type}/{self.owner}/packages/{package_type}/{package_name}/versions/{version_id}"
        
        with httpx.Client() as client:
            try:
                response = client.delete(url, headers=self.headers, timeout=self.DEFAULT_TIMEOUT)
                
                if response.status_code == 204:
                    # Successfully deleted
                    return True
                elif response.status_code == 404:
                    # Version doesn't exist
                    print(f"⚠️  Package version {version_id} not found", file=sys.stderr)
                    return False
                else:
                    response.raise_for_status()
                    
            except httpx.HTTPError as e:
                print(f"❌ Error deleting package version {version_id}: {e}", file=sys.stderr)
                raise
        
        return False

    def find_versions_by_tags(self, package_name: str, 
                             tags: List[str]) -> List[GHCRPackageVersion]:
        """Find package versions matching specific tags.
        
        Args:
            package_name: Package name
            tags: List of tags to match (e.g., ["v1.0.0", "v1.0.1"])
            
        Returns:
            List of matching versions with IDs
        """
        all_versions = self.list_package_versions(package_name)
        
        matching_versions = []
        for version in all_versions:
            # Check if any of the requested tags exist in this version's tags
            for tag in tags:
                if version.has_tag(tag):
                    matching_versions.append(version)
                    break  # Don't add the same version multiple times
        
        return matching_versions

    def validate_permissions(self) -> bool:
        """Validate that the GitHub token has the necessary permissions.
        
        Returns:
            True if token has write:packages and read:packages permissions
        """
        # Check if we can access the owner
        owner_type = self._detect_owner_type()
        url = f"{self.base_url}/{owner_type}/{self.owner}"
        
        with httpx.Client() as client:
            try:
                response = client.get(url, headers=self.headers, timeout=self.DEFAULT_TIMEOUT)
                
                if response.status_code == 200:
                    # Check OAuth scopes in headers
                    scopes = response.headers.get("X-OAuth-Scopes", "")
                    
                    # Need write:packages for deletion and read:packages for listing
                    has_write = "write:packages" in scopes
                    has_read = "read:packages" in scopes or "write:packages" in scopes  # write implies read
                    
                    if has_write and has_read:
                        return True
                    else:
                        print(f"⚠️  Missing required scopes. Current: {scopes}", file=sys.stderr)
                        print(f"   Required: write:packages, read:packages", file=sys.stderr)
                        return False
                elif response.status_code == 403:
                    print(f"❌ Access forbidden. Check token permissions.", file=sys.stderr)
                    return False
                else:
                    print(f"⚠️  Could not validate permissions: {response.status_code}", file=sys.stderr)
                    return False
                    
            except httpx.HTTPError as e:
                print(f"❌ Error validating permissions: {e}", file=sys.stderr)
                return False

    def get_package_info(self, package_name: str,
                        package_type: str = "container") -> Optional[Dict]:
        """Get package metadata from GHCR.
        
        Args:
            package_name: Package name (e.g., "demo-hello-python")
            package_type: Package type (default: "container")
            
        Returns:
            Package metadata dict if found, None otherwise
        """
        owner_type = self._detect_owner_type()
        url = f"{self.base_url}/{owner_type}/{self.owner}/packages/{package_type}/{package_name}"
        
        with httpx.Client() as client:
            try:
                response = client.get(url, headers=self.headers, timeout=self.DEFAULT_TIMEOUT)
                
                if response.status_code == 200:
                    return response.json()
                elif response.status_code == 404:
                    return None
                else:
                    response.raise_for_status()
                    
            except httpx.HTTPError as e:
                print(f"⚠️  Error getting package info: {e}", file=sys.stderr)
                return None
