"""
Unified cleanup orchestration for Git tags and GHCR packages.

This module coordinates the deletion of old Git tags and their corresponding
GHCR container packages following intelligent retention policies.
"""

import re
import sys
from dataclasses import dataclass, field
from datetime import datetime, timedelta
from typing import Dict, List, Optional, Tuple

from tools.release_helper.ghcr import GHCRClient, GHCRPackageVersion
from tools.release_helper.git import (
    get_all_tags,
    parse_semantic_version,
)


@dataclass
class CleanupPlan:
    """Plan for cleaning up tags and packages.
    
    Attributes:
        tags_to_delete: List of Git tag names to delete
        tags_to_keep: List of Git tag names to keep
        packages_to_delete: Dict mapping package names to list of version IDs to delete
        retention_policy: Dict containing the retention policy parameters
    """
    tags_to_delete: List[str]
    tags_to_keep: List[str]
    packages_to_delete: Dict[str, List[int]] = field(default_factory=dict)
    retention_policy: Dict = field(default_factory=dict)

    def total_tag_deletions(self) -> int:
        """Get total number of tags to delete."""
        return len(self.tags_to_delete)

    def total_package_deletions(self) -> int:
        """Get total number of package versions to delete."""
        return sum(len(versions) for versions in self.packages_to_delete.values())

    def is_empty(self) -> bool:
        """Check if cleanup plan is empty."""
        return len(self.tags_to_delete) == 0 and len(self.packages_to_delete) == 0


@dataclass
class CleanupResult:
    """Result of cleanup execution.
    
    Attributes:
        tags_deleted: List of successfully deleted tag names
        packages_deleted: Dict mapping package names to deleted version IDs
        errors: List of error messages
        dry_run: Whether this was a dry run
    """
    tags_deleted: List[str] = field(default_factory=list)
    packages_deleted: Dict[str, List[int]] = field(default_factory=dict)
    errors: List[str] = field(default_factory=list)
    dry_run: bool = True

    def is_successful(self) -> bool:
        """Check if cleanup was successful (no errors)."""
        return len(self.errors) == 0

    def summary(self) -> str:
        """Generate a summary of the cleanup result."""
        lines = []
        lines.append(f"Tags deleted: {len(self.tags_deleted)}")
        
        total_packages = sum(len(versions) for versions in self.packages_deleted.values())
        lines.append(f"Package versions deleted: {total_packages}")
        
        if self.errors:
            lines.append(f"Errors encountered: {len(self.errors)}")
        
        if self.dry_run:
            lines.append("(Dry run - no actual deletions)")
        
        return "\n".join(lines)


class ReleaseCleanup:
    """Orchestrate cleanup of Git tags and GHCR packages.
    
    This class coordinates the deletion of old releases, ensuring both
    Git tags and their corresponding GHCR container packages are cleaned
    up together following the same retention policy.
    """

    def __init__(self, owner: str, repo: str, token: Optional[str] = None):
        """Initialize the cleanup orchestrator.
        
        Args:
            owner: Repository owner (organization or user)
            repo: Repository name
            token: GitHub token (defaults to GITHUB_TOKEN env var)
        """
        self.owner = owner
        self.repo = repo
        self.ghcr_client = GHCRClient(owner, token)

    def plan_cleanup(
        self,
        keep_minor_versions: int = 2,
        min_age_days: int = 14
    ) -> CleanupPlan:
        """Plan what tags and packages to delete.
        
        Args:
            keep_minor_versions: Number of minor versions to keep
            min_age_days: Minimum age in days for deletion
            
        Returns:
            CleanupPlan with tags and packages to delete
        """
        # Get all tags
        all_tags = get_all_tags()
        
        # Identify tags to prune using the retention algorithm
        tags_to_delete, tags_to_keep = identify_tags_to_prune(
            all_tags,
            keep_minor_versions=keep_minor_versions,
            min_age_days=min_age_days
        )
        
        # Map tags to GHCR packages
        packages_to_delete: Dict[str, List[int]] = {}
        
        for tag in tags_to_delete:
            package_name = self._parse_tag_to_package_name(tag)
            if not package_name:
                print(f"⚠️  Could not parse package name from tag: {tag}", file=sys.stderr)
                continue
            
            version = self._extract_version_from_tag(tag)
            if not version:
                print(f"⚠️  Could not extract version from tag: {tag}", file=sys.stderr)
                continue
            
            # Find GHCR package versions matching this tag
            try:
                # First list all versions of this package
                all_versions = self.ghcr_client.list_package_versions(package_name)
                
                # Find versions that match our tag
                for pkg_version in all_versions:
                    if version in pkg_version.tags:
                        if package_name not in packages_to_delete:
                            packages_to_delete[package_name] = []
                        
                        packages_to_delete[package_name].append(pkg_version.version_id)
                        print(f"  Found GHCR version {pkg_version.version_id} for {package_name}:{version}")
                        
            except Exception as e:
                print(f"⚠️  Error finding GHCR versions for {package_name}: {e}", file=sys.stderr)
        
        return CleanupPlan(
            tags_to_delete=tags_to_delete,
            tags_to_keep=tags_to_keep,
            packages_to_delete=packages_to_delete,
            retention_policy={
                "keep_minor_versions": keep_minor_versions,
                "min_age_days": min_age_days
            }
        )

    def execute_cleanup(
        self,
        plan: CleanupPlan,
        dry_run: bool = True
    ) -> CleanupResult:
        """Execute the cleanup plan.
        
        Deletes tags first, then packages (safer order for rollback).
        
        Args:
            plan: CleanupPlan to execute
            dry_run: If True, don't actually delete anything
            
        Returns:
            CleanupResult with deletion results and any errors
        """
        result = CleanupResult(dry_run=dry_run)
        
        if dry_run:
            print("🧪 DRY RUN MODE - No actual deletions will occur")
            print("")
        
        # Phase 1: Delete Git tags
        print(f"📋 Deleting {len(plan.tags_to_delete)} Git tags...")
        for tag in plan.tags_to_delete:
            try:
                if dry_run:
                    print(f"  [DRY RUN] Would delete tag: {tag}")
                    result.tags_deleted.append(tag)
                else:
                    success = delete_remote_tag(tag, self.owner, self.repo)
                    if success:
                        result.tags_deleted.append(tag)
                        print(f"  ✅ Deleted tag: {tag}")
                    else:
                        error_msg = f"Failed to delete tag: {tag}"
                        result.errors.append(error_msg)
                        print(f"  ❌ {error_msg}", file=sys.stderr)
            except Exception as e:
                error_msg = f"Error deleting tag {tag}: {e}"
                result.errors.append(error_msg)
                print(f"  ❌ {error_msg}", file=sys.stderr)
        
        # Phase 2: Delete GHCR packages
        total_packages = sum(len(versions) for versions in plan.packages_to_delete.values())
        print(f"\n📦 Deleting {total_packages} GHCR package versions...")
        
        for package_name, version_ids in plan.packages_to_delete.items():
            if package_name not in result.packages_deleted:
                result.packages_deleted[package_name] = []
            
            for version_id in version_ids:
                try:
                    if dry_run:
                        print(f"  [DRY RUN] Would delete {package_name} version {version_id}")
                        result.packages_deleted[package_name].append(version_id)
                    else:
                        success = self.ghcr_client.delete_package_version(package_name, version_id)
                        if success:
                            result.packages_deleted[package_name].append(version_id)
                            print(f"  ✅ Deleted {package_name} version {version_id}")
                        else:
                            error_msg = f"Failed to delete {package_name} version {version_id}"
                            result.errors.append(error_msg)
                            print(f"  ❌ {error_msg}", file=sys.stderr)
                except Exception as e:
                    error_msg = f"Error deleting {package_name} version {version_id}: {e}"
                    result.errors.append(error_msg)
                    print(f"  ❌ {error_msg}", file=sys.stderr)
        
        return result

    def _parse_tag_to_package_name(self, tag: str) -> Optional[str]:
        """Parse a Git tag to extract the package name.
        
        Tags follow the format:
        - App tags: "domain-app.vX.Y.Z" -> package name: "domain-app"
        - Helm tags: "helm-domain-chart.vX.Y.Z" -> package name: "helm-domain-chart"
        
        Args:
            tag: Git tag name
            
        Returns:
            Package name or None if tag format is invalid
        """
        # Match pattern: anything.vX.Y.Z
        match = re.match(r'^([^.]+)\.v\d+\.\d+\.\d+', tag)
        if match:
            return match.group(1)
        return None

    def _extract_version_from_tag(self, tag: str) -> Optional[str]:
        """Extract version string from a Git tag.
        
        Args:
            tag: Git tag name (e.g., "demo-hello-python.v1.0.0")
            
        Returns:
            Version string (e.g., "v1.0.0") or None if not found
        """
        match = re.search(r'(v\d+\.\d+\.\d+(?:-[a-zA-Z0-9\-\.]+)?)', tag)
        if match:
            return match.group(1)
        return None


def get_tag_creation_date(tag: str) -> Optional[datetime]:
    """Get the creation date of a Git tag.
    
    Args:
        tag: Git tag name
        
    Returns:
        Creation datetime or None if tag doesn't exist
    """
    import subprocess
    
    try:
        # Get the date of the commit the tag points to
        result = subprocess.run(
            ["git", "log", "-1", "--format=%ai", tag],
            capture_output=True,
            text=True,
            check=True
        )
        date_str = result.stdout.strip()
        if date_str:
            # Parse the date string (format: 2025-01-15 10:30:45 -0800)
            return datetime.strptime(date_str[:19], "%Y-%m-%d %H:%M:%S")
    except subprocess.CalledProcessError:
        pass
    
    return None


def delete_remote_tag(tag_name: str, owner: str, repo: str) -> bool:
    """Delete a Git tag from the remote repository.
    
    Args:
        tag_name: Name of the tag to delete
        owner: Repository owner
        repo: Repository name
        
    Returns:
        True if deletion successful
    """
    import subprocess
    
    try:
        # Delete from remote
        subprocess.run(
            ["git", "push", "--delete", "origin", tag_name],
            capture_output=True,
            text=True,
            check=True
        )
        return True
    except subprocess.CalledProcessError as e:
        print(f"❌ Failed to delete remote tag {tag_name}: {e.stderr}", file=sys.stderr)
        return False


def identify_tags_to_prune(
    all_tags: List[str],
    keep_minor_versions: int = 2,
    min_age_days: int = 14
) -> Tuple[List[str], List[str]]:
    """Identify which tags should be pruned based on retention policy.
    
    Algorithm:
    1. Group tags by app/chart and parse versions
    2. Keep only latest patch of each minor version
    3. Keep last N minor versions across all majors
    4. Always keep latest minor version of each major version
    5. Only prune tags older than min_age_days
    
    Args:
        all_tags: List of all Git tags
        keep_minor_versions: Number of recent minor versions to keep
        min_age_days: Minimum age in days for deletion
        
    Returns:
        Tuple of (tags_to_delete, tags_to_keep)
    """
    # Group tags by app/chart
    tags_by_app: Dict[str, List[Tuple[str, Tuple[int, int, int]]]] = {}
    
    for tag in all_tags:
        # Parse tag to extract app name and version
        # Format: "domain-app.vX.Y.Z" or "helm-domain-chart.vX.Y.Z"
        match = re.match(r'^([^.]+)\.(v\d+\.\d+\.\d+)', tag)
        if not match:
            continue
        
        app_name = match.group(1)
        version_str = match.group(2)
        
        try:
            major, minor, patch, _ = parse_semantic_version(version_str)
            
            if app_name not in tags_by_app:
                tags_by_app[app_name] = []
            
            tags_by_app[app_name].append((tag, (major, minor, patch)))
        except ValueError:
            # Skip tags with invalid version format
            continue
    
    tags_to_delete = []
    tags_to_keep = []
    
    # Process each app/chart independently
    for app_name, app_tags in tags_by_app.items():
        # Step 1: Group by minor version and keep only latest patch
        minor_versions: Dict[Tuple[int, int], Tuple[str, int]] = {}
        
        for tag, (major, minor, patch) in app_tags:
            minor_key = (major, minor)
            if minor_key not in minor_versions or patch > minor_versions[minor_key][1]:
                # Store the tag with highest patch for this minor version
                minor_versions[minor_key] = (tag, patch)
        
        # Step 2: Separate into kept and deleted based on whether it's latest patch
        # All non-latest patches should be deleted if old enough
        kept_latest_patches = []
        for tag, (major, minor, patch) in app_tags:
            minor_key = (major, minor)
            latest_tag, latest_patch = minor_versions[minor_key]
            
            if tag == latest_tag:
                # This is the latest patch for this minor version
                kept_latest_patches.append((tag, major, minor))
            else:
                # This is an old patch - check age and delete
                tag_date = get_tag_creation_date(tag)
                if tag_date:
                    age_days = (datetime.now() - tag_date).days
                    if age_days >= min_age_days:
                        tags_to_delete.append(tag)
                        continue
                # If too recent, keep it
                tags_to_keep.append(tag)
        
        # Step 3: Sort latest patches by version (newest first)
        kept_latest_patches.sort(key=lambda x: (x[1], x[2]), reverse=True)
        
        # Step 4: Find latest minor version per major (for protection)
        latest_per_major: Dict[int, Tuple[int, str]] = {}
        for tag, major, minor in kept_latest_patches:
            if major not in latest_per_major or minor > latest_per_major[major][0]:
                latest_per_major[major] = (minor, tag)
        
        # Only apply "latest minor per major" protection if there are multiple majors
        # (the protection is meant to preserve backwards compatibility across majors)
        protected_tags = set()
        if len(latest_per_major) > 1:
            protected_tags = set(tag for _, tag in latest_per_major.values())
        
        # Step 5: Apply retention policy to latest patches only
        # Note: "keep last N minor versions" means we need AT LEAST N versions
        # If we have fewer than N versions total, all old versions can be pruned
        has_enough_versions = len(kept_latest_patches) >= keep_minor_versions
        
        for idx, (tag, major, minor) in enumerate(kept_latest_patches):
            # Check age
            tag_date = get_tag_creation_date(tag)
            if tag_date:
                age_days = (datetime.now() - tag_date).days
                if age_days < min_age_days:
                    tags_to_keep.append(tag)
                    continue
            
            # Keep if in last N minor versions (only if we have enough versions)
            if has_enough_versions and idx < keep_minor_versions:
                tags_to_keep.append(tag)
            # Keep if latest minor of its major version (when multiple majors exist)
            elif tag in protected_tags:
                tags_to_keep.append(tag)
            else:
                tags_to_delete.append(tag)
    
    return tags_to_delete, tags_to_keep
