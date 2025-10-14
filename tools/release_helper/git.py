"""
Git operations for the release helper.
"""

import re
import subprocess
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


def check_tag_exists(tag_name: str) -> bool:
    """Check if a Git tag exists.
    
    Args:
        tag_name: Tag name to check
        
    Returns:
        True if tag exists, False otherwise
    """
    try:
        result = subprocess.run(
            ["git", "tag", "-l", tag_name],
            capture_output=True,
            text=True,
            check=True
        )
        return bool(result.stdout.strip())
    except subprocess.CalledProcessError:
        return False


def create_git_tag(tag_name: str, commit_sha: Optional[str] = None, message: Optional[str] = None, force: bool = False) -> None:
    """Create a Git tag on the specified commit.
    
    Args:
        tag_name: Name of the tag to create
        commit_sha: Optional commit SHA to tag (defaults to HEAD)
        message: Optional annotation message for annotated tags
        force: If True, overwrite existing tag. If False and tag exists, skip creation.
    """
    # Check if tag already exists
    if check_tag_exists(tag_name):
        if force:
            print(f"Tag {tag_name} already exists, forcing overwrite...")
        else:
            print(f"Tag {tag_name} already exists, skipping creation")
            return
    
    cmd = ["git", "tag"]
    
    # Add force flag if requested
    if force:
        cmd.append("-f")

    if message:
        cmd.extend(["-a", tag_name, "-m", message])
    else:
        cmd.append(tag_name)

    if commit_sha:
        cmd.append(commit_sha)

    print(f"Creating Git tag: {tag_name}")
    subprocess.run(cmd, check=True)


def push_git_tag(tag_name: str, force: bool = False) -> None:
    """Push a Git tag to the remote repository.
    
    Args:
        tag_name: Name of the tag to push
        force: If True, force push the tag (overwrites remote tag)
    """
    cmd = ["git", "push"]
    if force:
        cmd.append("--force")
    cmd.extend(["origin", tag_name])
    
    print(f"Pushing Git tag: {tag_name}" + (" (force)" if force else ""))
    subprocess.run(cmd, check=True)


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
    """Get all Git tags sorted by version (newest first).
    
    Fetches tags from remote to ensure we have the latest tags available,
    which is critical for helm-only releases that need to find app versions.
    """
    try:
        # First, fetch all tags from remote to ensure we have the latest
        # This is especially important in CI/CD environments where the repo
        # might be shallow cloned or not have all tags locally
        subprocess.run(
            ["git", "fetch", "--tags", "--force"],
            capture_output=True,
            text=True,
            check=False  # Don't fail if fetch has issues, we'll try to use local tags
        )
        
        # Now get all local tags (which should include newly fetched ones)
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