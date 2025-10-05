"""
Git operations for the release helper.
"""

import re
import subprocess
from datetime import datetime, timedelta
from typing import Dict, List, Optional, Tuple


def format_git_tag(domain: str, app_name: str, version: str) -> str:
    """Format a Git tag in the domain-appname.version format."""
    return f"{domain}-{app_name}.{version}"


def format_helm_chart_tag(chart_name: str, version: str) -> str:
    """Format a Git tag for a Helm chart in the chart-name.version format.
    
    Chart names already include the helm- prefix and namespace (e.g., "helm-demo-hello-fastapi"),
    so we don't add another prefix here. The helm- prefix is kept in tags to avoid collisions
    with app tags in the same namespace.
    
    Args:
        chart_name: Name of the Helm chart (e.g., "helm-demo-hello-fastapi", "helm-manman-manman-host")
        version: Version string (e.g., "v1.0.0")
    
    Returns:
        Formatted tag (e.g., "helm-demo-hello-fastapi.v1.0.0")
    """
    return f"{chart_name}.{version}"


def create_git_tag(tag_name: str, commit_sha: Optional[str] = None, message: Optional[str] = None) -> None:
    """Create a Git tag on the specified commit."""
    cmd = ["git", "tag"]

    if message:
        cmd.extend(["-a", tag_name, "-m", message])
    else:
        cmd.append(tag_name)

    if commit_sha:
        cmd.append(commit_sha)

    print(f"Creating Git tag: {tag_name}")
    subprocess.run(cmd, check=True)


def push_git_tag(tag_name: str) -> None:
    """Push a Git tag to the remote repository."""
    print(f"Pushing Git tag: {tag_name}")
    subprocess.run(["git", "push", "origin", tag_name], check=True)


def get_previous_tag() -> Optional[str]:
    """Get the previous Git tag."""
    try:
        result = subprocess.run(
            ["git", "describe", "--tags", "--abbrev=0", "HEAD^"],
            capture_output=True,
            text=True,
            check=True
        )
        return result.stdout.strip()
    except subprocess.CalledProcessError:
        return None


def get_all_tags() -> List[str]:
    """Get all Git tags sorted by version (newest first)."""
    try:
        result = subprocess.run(
            ["git", "tag", "--sort=-version:refname"],
            capture_output=True,
            text=True,
            check=True
        )
        return [tag.strip() for tag in result.stdout.strip().split('\n') if tag.strip()]
    except subprocess.CalledProcessError:
        return []


def get_app_tags(domain: str, app_name: str) -> List[str]:
    """Get all tags for a specific app, sorted by version (newest first)."""
    all_tags = get_all_tags()
    app_prefix = f"{domain}-{app_name}."
    app_tags = [tag for tag in all_tags if tag.startswith(app_prefix)]
    return app_tags


def get_helm_chart_tags(chart_name: str) -> List[str]:
    """Get all tags for a specific helm chart, sorted by version (newest first).
    
    Chart names already include the helm-namespace- prefix (e.g., "helm-demo-hello-fastapi").
    Tags keep this prefix to avoid collisions with app tags.
    
    Args:
        chart_name: Name of the Helm chart (e.g., "helm-demo-hello-fastapi", "helm-manman-manman-host")
    
    Returns:
        List of tags sorted by version (newest first)
    """
    all_tags = get_all_tags()
    chart_prefix = f"{chart_name}."
    chart_tags = [tag for tag in all_tags if tag.startswith(chart_prefix)]
    return chart_tags


def parse_version_from_tag(tag: str, domain: str, app_name: str) -> Optional[str]:
    """Parse version from an app tag.
    
    Args:
        tag: Git tag (e.g., "demo-hello_python.v1.2.3")
        domain: App domain (e.g., "demo")
        app_name: App name (e.g., "hello_python")
    
    Returns:
        Version string (e.g., "v1.2.3") or None if not a valid app tag
    """
    expected_prefix = f"{domain}-{app_name}."
    if not tag.startswith(expected_prefix):
        return None
    
    version = tag[len(expected_prefix):]
    # Validate that it looks like a semantic version
    if re.match(r'^v\d+\.\d+\.\d+(?:-[a-zA-Z0-9\-\.]+)?$', version):
        return version
    return None


def parse_version_from_helm_chart_tag(tag: str, chart_name: str) -> Optional[str]:
    """Parse version from a helm chart tag.
    
    Chart names already include the helm-namespace- prefix.
    Tags keep this prefix to avoid collisions with app tags.
    
    Args:
        tag: Git tag (e.g., "helm-demo-hello-fastapi.v1.2.3")
        chart_name: Chart name (e.g., "helm-demo-hello-fastapi")
    
    Returns:
        Version string (e.g., "v1.2.3") or None if not a valid chart tag
    """
    expected_prefix = f"{chart_name}."
    if not tag.startswith(expected_prefix):
        return None
    
    version = tag[len(expected_prefix):]
    # Validate that it looks like a semantic version
    if re.match(r'^v\d+\.\d+\.\d+(?:-[a-zA-Z0-9\-\.]+)?$', version):
        return version
    return None


def get_latest_app_version(domain: str, app_name: str) -> Optional[str]:
    """Get the latest version for a specific app.
    
    Args:
        domain: App domain (e.g., "demo")
        app_name: App name (e.g., "hello_python")
    
    Returns:
        Latest version string (e.g., "v1.2.3") or None if no versions found
    """
    app_tags = get_app_tags(domain, app_name)
    for tag in app_tags:
        version = parse_version_from_tag(tag, domain, app_name)
        if version:
            return version
    return None


def get_latest_helm_chart_version(chart_name: str) -> Optional[str]:
    """Get the latest version for a specific helm chart.
    
    Args:
        chart_name: Chart name (e.g., "manman-host", "hello-fastapi")
    
    Returns:
        Latest version string (e.g., "v1.2.3") or None if no versions found
    """
    chart_tags = get_helm_chart_tags(chart_name)
    for tag in chart_tags:
        version = parse_version_from_helm_chart_tag(tag, chart_name)
        if version:
            return version
    return None


def parse_semantic_version(version: str) -> Tuple[int, int, int, Optional[str]]:
    """Parse a semantic version string into components.
    
    Args:
        version: Version string (e.g., "v1.2.3" or "v1.2.3-beta1")
    
    Returns:
        Tuple of (major, minor, patch, prerelease)
    
    Raises:
        ValueError: If version format is invalid
    """
    # Remove 'v' prefix if present
    if version.startswith('v'):
        version = version[1:]
    
    # Split on '-' to separate prerelease
    parts = version.split('-', 1)
    version_part = parts[0]
    prerelease = parts[1] if len(parts) > 1 else None
    
    # Parse major.minor.patch
    version_components = version_part.split('.')
    if len(version_components) != 3:
        raise ValueError(f"Invalid semantic version format: {version}")
    
    try:
        major = int(version_components[0])
        minor = int(version_components[1])
        patch = int(version_components[2])
    except ValueError:
        raise ValueError(f"Invalid semantic version format: {version}")
    
    return major, minor, patch, prerelease


def increment_minor_version(current_version: str) -> str:
    """Increment the minor version and reset patch to 0.
    
    Args:
        current_version: Current version (e.g., "v1.2.3")
    
    Returns:
        New version with incremented minor (e.g., "v1.3.0")
    """
    major, minor, patch, prerelease = parse_semantic_version(current_version)
    return f"v{major}.{minor + 1}.0"


def increment_patch_version(current_version: str) -> str:
    """Increment the patch version.
    
    Args:
        current_version: Current version (e.g., "v1.2.3")
    
    Returns:
        New version with incremented patch (e.g., "v1.2.4")
    """
    major, minor, patch, prerelease = parse_semantic_version(current_version)
    return f"v{major}.{minor}.{patch + 1}"


def auto_increment_version(domain: str, app_name: str, increment_type: str) -> str:
    """Auto-increment version for an app based on the latest tag.
    
    Args:
        domain: App domain (e.g., "demo")
        app_name: App name (e.g., "hello_python")
        increment_type: Either "minor" or "patch"
    
    Returns:
        New version string
    
    Raises:
        ValueError: If increment_type is invalid or no previous version found
    """
    if increment_type not in ["minor", "patch"]:
        raise ValueError(f"Invalid increment type: {increment_type}. Must be 'minor' or 'patch'")
    
    latest_version = get_latest_app_version(domain, app_name)
    if not latest_version:
        # No previous version, start with v0.1.0 for minor or v0.0.1 for patch
        if increment_type == "minor":
            return "v0.1.0"
        else:  # patch
            return "v0.0.1"
    
    if increment_type == "minor":
        return increment_minor_version(latest_version)
    else:  # patch
        return increment_patch_version(latest_version)


def auto_increment_helm_chart_version(chart_name: str, increment_type: str) -> str:
    """Auto-increment version for a helm chart based on the latest tag.
    
    Args:
        chart_name: Chart name (e.g., "manman-host", "hello-fastapi")
        increment_type: Either "minor" or "patch"
    
    Returns:
        New version string
    
    Raises:
        ValueError: If increment_type is invalid
    """
    if increment_type not in ["minor", "patch"]:
        raise ValueError(f"Invalid increment type: {increment_type}. Must be 'minor' or 'patch'")
    
    latest_version = get_latest_helm_chart_version(chart_name)
    if not latest_version:
        # No previous version, start with v0.1.0 for minor or v0.0.1 for patch
        if increment_type == "minor":
            return "v0.1.0"
        else:  # patch
            return "v0.0.1"
    
    if increment_type == "minor":
        return increment_minor_version(latest_version)
    else:  # patch
        return increment_patch_version(latest_version)


def get_tag_creation_date(tag_name: str) -> Optional[datetime]:
    """Get the creation date of a Git tag.
    
    Args:
        tag_name: Tag name
    
    Returns:
        datetime object for tag creation date, or None if tag doesn't exist
    """
    try:
        # Get the date when the tag was created
        result = subprocess.run(
            ["git", "log", "-1", "--format=%aI", tag_name],
            capture_output=True,
            text=True,
            check=True
        )
        date_str = result.stdout.strip()
        if date_str:
            return datetime.fromisoformat(date_str.replace('Z', '+00:00'))
        return None
    except subprocess.CalledProcessError:
        return None


def delete_local_tag(tag_name: str) -> bool:
    """Delete a local Git tag.
    
    Args:
        tag_name: Tag name to delete
    
    Returns:
        True if successful, False otherwise
    """
    try:
        print(f"Deleting local tag: {tag_name}")
        subprocess.run(["git", "tag", "-d", tag_name], check=True, capture_output=True)
        return True
    except subprocess.CalledProcessError as e:
        print(f"Failed to delete local tag {tag_name}: {e}")
        return False


def identify_tags_to_prune(
    all_tags: List[str],
    min_age_days: int = 14,
    keep_latest_minor_versions: int = 2
) -> List[str]:
    """Identify tags that should be pruned based on age and version rules.
    
    Rules:
    - Keep the last N minor versions completely (all patches) (default: 2)
    - For each older minor version, keep only the latest patch version
    - Only consider pruning tags older than min_age_days (default: 14)
    
    Args:
        all_tags: List of all tags
        min_age_days: Minimum age in days before a tag can be pruned
        keep_latest_minor_versions: Number of latest minor versions to keep completely
    
    Returns:
        List of tags that can be safely pruned
    """
    # Group tags by prefix (domain-app or helm-chart)
    tag_groups: Dict[str, List[Tuple[str, Tuple[int, int, int, Optional[str]], datetime]]] = {}
    
    now = datetime.now()
    min_date = now - timedelta(days=min_age_days)
    
    for tag in all_tags:
        # Parse tag to extract prefix and version
        # Tags are in format: domain-app.vX.Y.Z or helm-chart-name.vX.Y.Z
        # Find the pattern ".vX.Y.Z"
        match = re.match(r'^(.+)\.(v\d+\.\d+\.\d+(?:-[a-zA-Z0-9\-\.]+)?)$', tag)
        if not match:
            continue
        
        prefix = match.group(1)
        version_str = match.group(2)
        
        # Parse version
        try:
            if version_str.startswith('v'):
                version_str = version_str[1:]
            
            # Split on '-' to separate prerelease
            version_parts = version_str.split('-', 1)
            version_numbers = version_parts[0]
            prerelease = version_parts[1] if len(version_parts) > 1 else None
            
            # Parse major.minor.patch
            nums = version_numbers.split('.')
            if len(nums) != 3:
                continue
            
            major = int(nums[0])
            minor = int(nums[1])
            patch = int(nums[2])
            
            # Get tag creation date
            tag_date = get_tag_creation_date(tag)
            if not tag_date:
                # If we can't get the date, skip this tag (be conservative)
                continue
            
            # Group by prefix
            if prefix not in tag_groups:
                tag_groups[prefix] = []
            
            tag_groups[prefix].append((tag, (major, minor, patch, prerelease), tag_date))
            
        except (ValueError, IndexError):
            # Skip tags that don't follow expected format
            continue
    
    # Now identify tags to prune for each group
    tags_to_prune = []
    
    for prefix, tags_data in tag_groups.items():
        # Sort by version (descending)
        tags_data.sort(key=lambda x: x[1], reverse=True)
        
        # Group by major.minor version
        minor_versions: Dict[Tuple[int, int], List[Tuple[str, Tuple[int, int, int, Optional[str]], datetime]]] = {}
        for tag, version, tag_date in tags_data:
            major, minor, patch, prerelease = version
            key = (major, minor)
            if key not in minor_versions:
                minor_versions[key] = []
            minor_versions[key].append((tag, version, tag_date))
        
        # Sort minor versions (descending)
        sorted_minor_versions = sorted(minor_versions.keys(), reverse=True)
        
        # Keep the latest N minor versions completely (all patches)
        kept_minor_versions = sorted_minor_versions[:keep_latest_minor_versions]
        
        # For older minor versions, keep only the latest patch version
        older_minor_versions = sorted_minor_versions[keep_latest_minor_versions:]
        
        # For kept minor versions, don't prune any patches (even if old)
        # But we could prune old patches here if we want - let me follow the requirement literally
        # "leave the last 2 minor versions" - keep them completely
        
        # For older minor versions, keep only the latest patch, prune the rest if old enough
        for minor_key in older_minor_versions:
            versions_list = minor_versions[minor_key]
            # Sort by patch version (descending)
            versions_list.sort(key=lambda x: x[1][2], reverse=True)
            
            # Keep the first one (latest patch), mark the rest for pruning if old enough
            for i, (tag, version, tag_date) in enumerate(versions_list):
                if i > 0 and tag_date < min_date:
                    # Not the latest patch and old enough
                    tags_to_prune.append(tag)
                elif i == 0 and tag_date < min_date:
                    # This is the latest patch of this minor version
                    # According to the requirement, we should keep it even if it's old
                    # "leave...the latest patch of each"
                    pass
    
    return tags_to_prune