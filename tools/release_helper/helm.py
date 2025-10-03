"""
Helm chart utilities for the release helper.
"""

import json
import os
import re
import shutil
import subprocess
import tempfile
import yaml
from datetime import datetime
from pathlib import Path
from typing import Dict, List, Optional

from tools.release_helper.core import run_bazel, find_workspace_root
from tools.release_helper.git import get_latest_app_version, get_latest_helm_chart_version


def get_chart_version_from_git(chart_name: str) -> Optional[str]:
    """Get the latest version of a chart from git tags.
    
    Args:
        chart_name: Name of the chart (e.g., "helm-manman-host-services")
        
    Returns:
        Latest version string (e.g., "v1.2.3") or None if no tags found
    """
    # Use the centralized git tag handling from git.py which uses the correct
    # tag format: {chart-name}.{version} (e.g., "helm-manman-host-services.v0.0.1")
    return get_latest_helm_chart_version(chart_name)


def get_chart_version_from_file(chart_dir: Path) -> Optional[str]:
    """Read the current version from a Chart.yaml file.
    
    Args:
        chart_dir: Path to the chart directory
        
    Returns:
        Version string from Chart.yaml or None if not found
    """
    chart_yaml_path = chart_dir / "Chart.yaml"
    if not chart_yaml_path.exists():
        return None
    
    try:
        with open(chart_yaml_path, 'r') as f:
            chart_data = yaml.safe_load(f)
        return chart_data.get('version')
    except Exception:
        return None


def increment_version(version: str, bump_type: str = "patch") -> str:
    """Increment a semantic version string.
    
    Args:
        version: Version string (e.g., "v1.2.3" or "1.2.3")
        bump_type: Type of bump - "major", "minor", or "patch"
        
    Returns:
        Incremented version string in same format as input
    """
    # Handle v prefix
    has_v = version.startswith('v')
    clean_version = version.lstrip('v')
    
    try:
        parts = [int(p) for p in clean_version.split('.')]
        if len(parts) != 3:
            raise ValueError(f"Invalid version format: {version}")
        
        major, minor, patch = parts
        
        if bump_type == "major":
            major += 1
            minor = 0
            patch = 0
        elif bump_type == "minor":
            minor += 1
            patch = 0
        else:  # patch
            patch += 1
        
        new_version = f"{major}.{minor}.{patch}"
        return f"v{new_version}" if has_v else new_version
    except (ValueError, AttributeError) as e:
        raise ValueError(f"Failed to increment version '{version}': {e}")


def has_chart_changed(chart_name: str, base_commit: str = "HEAD~1") -> bool:
    """Detect if a chart's files have changed.
    
    Args:
        chart_name: Name of the chart
        base_commit: Base commit to compare against (default: previous commit)
        
    Returns:
        True if chart files have changed, False otherwise
    """
    try:
        # Find chart location by querying bazel
        all_charts = list_all_helm_charts()
        matching_chart = next((c for c in all_charts if c['name'] == chart_name), None)
        
        if not matching_chart:
            # If we can't find the chart, assume it changed to be safe
            return True
        
        # Extract package path from bazel target
        bazel_target = matching_chart['bazel_target']
        package_path = bazel_target[2:].split(':')[0]  # //demo:chart -> demo
        
        # Check if any files in the chart package have changed
        result = subprocess.run(
            ["git", "diff", "--name-only", base_commit, "HEAD", "--", f"{package_path}/"],
            capture_output=True,
            text=True,
            check=True
        )
        
        changed_files = [f for f in result.stdout.strip().split('\n') if f.strip()]
        return len(changed_files) > 0
    except Exception as e:
        print(f"Warning: Could not detect changes for chart '{chart_name}': {e}")
        # If we can't determine, assume it changed to be safe
        return True


def determine_chart_version(
    chart_name: str, 
    chart_dir: Optional[Path] = None,
    bump_type: str = "patch",
    base_version: Optional[str] = None
) -> str:
    """Determine the version to use for a chart.
    
    This function:
    1. Tries to get the latest version from git tags (helm/<chart-name>/v*)
    2. Falls back to reading from Chart.yaml if provided (ignores dev versions)
    3. Falls back to base_version if provided
    4. Otherwise starts at v0.1.0
    5. Increments based on bump_type
    
    Args:
        chart_name: Name of the chart
        chart_dir: Optional path to chart directory to read Chart.yaml
        bump_type: Type of version bump ("major", "minor", "patch")
        base_version: Optional base version to use if no existing version found
        
    Returns:
        Version string to use for the chart (e.g., "v1.2.3")
    """
    # Try to get version from git tags (primary source of truth)
    current_version = get_chart_version_from_git(chart_name)
    
    # Fall back to Chart.yaml, but skip dev versions
    if not current_version and chart_dir:
        file_version = get_chart_version_from_file(chart_dir)
        # Only use file version if it's not a dev/development version
        if file_version and not any(suffix in file_version for suffix in ['-dev', '-alpha', '-beta', '-rc']):
            current_version = file_version
    
    # Fall back to base version or default
    if not current_version:
        current_version = base_version or "v0.0.0"
    
    # Increment the version
    new_version = increment_version(current_version, bump_type)
    
    return new_version


def get_helm_chart_metadata(bazel_target: str) -> Dict:
    """Get release metadata for a helm chart by building and reading its metadata target.
    
    Args:
        bazel_target: Full bazel target path (e.g., "//demo:fastapi_chart_chart_metadata")
    """
    # Build the metadata target
    run_bazel(["build", bazel_target])

    # Extract path from target for finding the generated file
    # Target format: //path/to/chart:target_name
    if not bazel_target.startswith("//"):
        raise ValueError(f"Invalid bazel target format: {bazel_target}")
    
    target_parts = bazel_target[2:].split(":")
    if len(target_parts) != 2:
        raise ValueError(f"Invalid bazel target format: {bazel_target}")
    
    package_path = target_parts[0]
    target_name = target_parts[1]

    # Read the generated JSON file
    workspace_root = find_workspace_root()
    metadata_file = workspace_root / f"bazel-bin/{package_path}/{target_name}_chart_metadata.json"
    if not metadata_file.exists():
        raise FileNotFoundError(f"Metadata file not found: {metadata_file}")

    with open(metadata_file) as f:
        return json.load(f)


def list_all_helm_charts() -> List[Dict[str, str]]:
    """List all helm charts in the monorepo that have release metadata.
    
    Returns:
        List of dicts with 'bazel_target', 'name', 'domain', 'apps' for each chart
    """
    # Query for all helm chart metadata targets
    result = run_bazel(["query", "kind(helm_chart_metadata, //...)", "--output=label"])

    charts = []
    for line in result.stdout.strip().split('\n'):
        if line and '_chart_metadata' in line:
            # Get metadata to extract chart info
            try:
                metadata = get_helm_chart_metadata(line)
                charts.append({
                    'bazel_target': line,
                    'name': metadata['name'],
                    'domain': metadata['domain'],
                    'namespace': metadata['namespace'],
                    'apps': metadata.get('apps', []),
                    'chart_target': metadata.get('chart_target', ''),
                })
            except Exception as e:
                print(f"Warning: Could not get metadata for {line}: {e}")
                continue

    return sorted(charts, key=lambda x: x['name'])


def find_helm_chart_bazel_target(chart_name: str) -> str:
    """Find the bazel target for a helm chart by name.
    
    Args:
        chart_name: Name of the helm chart (e.g., "hello-fastapi")
        
    Returns:
        Bazel target path (e.g., "//demo:fastapi_chart_chart_metadata")
        
    Raises:
        ValueError: If chart not found or multiple charts match
    """
    all_charts = list_all_helm_charts()
    
    # Filter by exact chart name match
    matching_charts = [c for c in all_charts if c['name'] == chart_name]
    
    if not matching_charts:
        raise ValueError(f"No helm chart found with name '{chart_name}'")
    
    if len(matching_charts) > 1:
        targets = [c['bazel_target'] for c in matching_charts]
        raise ValueError(f"Multiple helm charts found with name '{chart_name}': {targets}")
    
    return matching_charts[0]['bazel_target']


def resolve_app_versions_for_chart(chart_metadata: Dict, use_released_versions: bool = True) -> Dict[str, str]:
    """Resolve the versions of apps included in a helm chart.
    
    Args:
        chart_metadata: Metadata dict from get_helm_chart_metadata
        use_released_versions: If True, use latest git tags. If False, use "latest"
        
    Returns:
        Dict mapping app name to version (e.g., {"hello_fastapi": "v1.0.0"})
    """
    app_versions = {}
    
    for app_name in chart_metadata.get('apps', []):
        if use_released_versions:
            # Get domain from the chart to construct proper tag
            # We need to query the app's metadata to get its domain
            try:
                from tools.release_helper.release import find_app_bazel_target
                from tools.release_helper.metadata import get_app_metadata
                
                app_target = find_app_bazel_target(app_name)
                app_metadata = get_app_metadata(app_target)
                app_domain = app_metadata['domain']
                
                # Get latest version from git tags
                latest_version = get_latest_app_version(app_domain, app_name)
                
                if latest_version:
                    app_versions[app_name] = latest_version
                else:
                    # Fallback to "latest" if no version found
                    print(f"Warning: No released version found for {app_name}, using 'latest'")
                    app_versions[app_name] = "latest"
            except Exception as e:
                print(f"Warning: Could not resolve version for {app_name}: {e}, using 'latest'")
                app_versions[app_name] = "latest"
        else:
            # Use "latest" for all apps
            app_versions[app_name] = "latest"
    
    return app_versions


def build_helm_chart(chart_target: str, chart_name: str, chart_version: str, app_versions: Optional[Dict[str, str]] = None) -> Path:
    """Build a helm chart with specified version and app versions.
    
    Args:
        chart_target: Bazel target for the chart (e.g., "//demo:fastapi_chart")
        chart_name: Name of the chart (e.g., "hello-fastapi")
        chart_version: Version to use for the chart (e.g., "v1.0.0")
        app_versions: Optional dict of app name -> version overrides
        
    Returns:
        Path to the generated chart tarball
    """
    workspace_root = find_workspace_root()
    
    # Extract package path and target name
    if not chart_target.startswith("//"):
        raise ValueError(f"Invalid bazel target format: {chart_target}")
    
    target_parts = chart_target[2:].split(":")
    if len(target_parts) != 2:
        raise ValueError(f"Invalid bazel target format: {chart_target}")
    
    package_path = target_parts[0]
    target_name = target_parts[1]
    
    # Build the chart with bazel
    # TODO: In the future, we might want to inject app versions into the chart build
    # For now, we build the chart as-is and assume it uses version placeholders
    print(f"Building bazel target: {chart_target}")
    run_bazel(["build", chart_target])
    
    # Find the generated tarball
    # The helm_chart rule outputs a tarball named {chart_name}.tar.gz (not {target_name})
    chart_tarball = workspace_root / f"bazel-bin/{package_path}/{chart_name}.tar.gz"
    
    if not chart_tarball.exists():
        # Try with target name as fallback
        alt_tarball = workspace_root / f"bazel-bin/{package_path}/{target_name}.tar.gz"
        if alt_tarball.exists():
            chart_tarball = alt_tarball
        else:
            raise FileNotFoundError(f"Chart tarball not found at {chart_tarball} or {alt_tarball}")
    
    print(f"Found chart tarball: {chart_tarball}")
    return chart_tarball


def package_helm_chart_for_release(
    chart_name: str,
    chart_version: Optional[str] = None,
    output_dir: Optional[Path] = None,
    use_released_app_versions: bool = True,
    auto_version: bool = False,
    bump_type: str = "patch"
) -> tuple[Path, str]:
    """Package a helm chart for release with resolved app versions.
    
    Supports both manual versioning (chart_version provided) and automatic per-chart
    versioning (auto_version=True). When auto-versioning, each chart maintains its
    own version independently based on git tags.
    
    When use_released_app_versions=True, this function queries git tags to find the
    latest semver version for each app in the chart and updates the imageTag values
    in the chart's values.yaml. This ensures published Helm charts reference specific
    versions instead of "latest".
    
    Args:
        chart_name: Name of the helm chart (e.g., "hello-fastapi")
        chart_version: Explicit version for the chart (if None and auto_version=True, determines automatically)
        output_dir: Optional output directory for the packaged chart
        use_released_app_versions: Whether to resolve app versions from git tags (default: True)
        auto_version: If True, automatically determine version from git tags/Chart.yaml
        bump_type: Type of version bump when auto-versioning ("major", "minor", "patch")
        
    Returns:
        Tuple of (Path to packaged chart tarball, version used)
    """
    # Find the chart
    chart_metadata_target = find_helm_chart_bazel_target(chart_name)
    chart_metadata = get_helm_chart_metadata(chart_metadata_target)
    
    # Resolve app versions from git tags or use "latest"
    # When use_released_app_versions=True, this queries git tags like "demo-hello_python.v1.2.3"
    # to find the latest semver version for each app
    app_versions = resolve_app_versions_for_chart(chart_metadata, use_released_app_versions)
    
    # Get the actual chart target (without _chart_metadata suffix)
    chart_package = chart_metadata_target.rsplit(":", 1)[0]
    chart_target_name = chart_metadata.get('chart_target', '').lstrip(':')
    chart_target = f"{chart_package}:{chart_target_name}"
    
    # Build the chart - use the chart_name from metadata for finding the output file
    actual_chart_name = chart_metadata.get('name', chart_name)
    
    # For auto-versioning, we need to build first to get the chart directory,
    # then determine version, then repackage with the correct version
    print(f"Building bazel target: {chart_target}")
    run_bazel(["build", chart_target])
    
    workspace_root = find_workspace_root()
    package_path = chart_package.lstrip('//')
    
    # Try to find the chart directory (before it's packaged)
    # The helm_chart rule creates a directory with the chart contents
    chart_dir = workspace_root / f"bazel-bin/{package_path}/{actual_chart_name}_chart/{actual_chart_name}"
    
    # Determine the version to use
    if auto_version and not chart_version:
        chart_version = determine_chart_version(
            chart_name=actual_chart_name,
            chart_dir=chart_dir if chart_dir.exists() else None,
            bump_type=bump_type
        )
        print(f"Auto-determined chart version for {actual_chart_name}: {chart_version}")
    elif not chart_version:
        raise ValueError("chart_version must be provided when auto_version=False")
    
    print(f"Packaging chart '{actual_chart_name}' version {chart_version}")
    print(f"App versions: {app_versions}")
    
    # Package the chart with the determined version
    if chart_dir.exists():
        # Use package_chart_with_version for proper versioning
        if not output_dir:
            output_dir = Path(tempfile.mkdtemp())
        output_dir.mkdir(parents=True, exist_ok=True)
        
        packaged_chart = package_chart_with_version(
            chart_dir=chart_dir,
            chart_name=actual_chart_name,
            chart_version=chart_version,
            output_dir=output_dir,
            auto_version=False,  # We already determined the version
            app_versions=app_versions  # Pass resolved app versions
        )
        return packaged_chart, chart_version
    else:
        # Fallback to old method if we can't find the unpacked chart directory
        chart_tarball = build_helm_chart(chart_target, actual_chart_name, chart_version, app_versions)
        
        if output_dir:
            output_dir.mkdir(parents=True, exist_ok=True)
            output_path = output_dir / f"{chart_name}-{chart_version}.tgz"
            shutil.copy(chart_tarball, output_path)
            return output_path, chart_version
        
        return chart_tarball, chart_version


def package_chart_with_version(
    chart_dir: Path,
    chart_name: str,
    chart_version: Optional[str] = None,
    output_dir: Optional[Path] = None,
    bump_type: str = "patch",
    auto_version: bool = False,
    app_versions: Optional[Dict[str, str]] = None
) -> Path:
    """Package a Helm chart directory into a versioned tarball using helm package.
    
    Supports both manual versioning (chart_version provided) and automatic versioning
    (auto_version=True). When auto-versioning, it reads the current version from git
    tags or Chart.yaml and increments it based on bump_type.
    
    Args:
        chart_dir: Path to the chart directory
        chart_name: Name of the chart
        chart_version: Explicit version to use (if None and auto_version=True, will determine automatically)
        output_dir: Directory to output the packaged chart (default: create temp dir)
        bump_type: Type of version bump when auto-versioning ("major", "minor", "patch")
        auto_version: If True, automatically determine version from git tags/Chart.yaml
        app_versions: Optional dict mapping app names to versions for updating imageTag values
        
    Returns:
        Path to the generated .tgz file
    """
    # Determine version to use
    if auto_version and not chart_version:
        chart_version = determine_chart_version(
            chart_name=chart_name,
            chart_dir=chart_dir,
            bump_type=bump_type
        )
        print(f"Auto-determined chart version for {chart_name}: {chart_version}")
    elif not chart_version:
        raise ValueError("chart_version must be provided when auto_version=False")
    
    # Create output directory if not provided
    if not output_dir:
        output_dir = Path(tempfile.mkdtemp())
    output_dir.mkdir(parents=True, exist_ok=True)
    
    # Copy chart to a temporary directory to avoid permission issues with bazel-bin
    # Bazel output directories are often read-only
    temp_chart_dir = Path(tempfile.mkdtemp()) / chart_name
    shutil.copytree(chart_dir, temp_chart_dir, symlinks=False, ignore_dangling_symlinks=True)
    
    # Make all files in the temporary directory writable
    for root, dirs, files in os.walk(temp_chart_dir):
        for d in dirs:
            os.chmod(os.path.join(root, d), 0o755)
        for f in files:
            os.chmod(os.path.join(root, f), 0o644)
    
    # Update Chart.yaml with the version in the temporary copy
    chart_yaml_path = temp_chart_dir / "Chart.yaml"
    if chart_yaml_path.exists():
        # Read, update version, and write back using yaml library
        with open(chart_yaml_path, 'r') as f:
            chart_data = yaml.safe_load(f)
        
        chart_data['version'] = chart_version
        
        with open(chart_yaml_path, 'w') as f:
            yaml.safe_dump(chart_data, f, default_flow_style=False, sort_keys=False)
    
    # Update values.yaml with resolved app versions (imageTag)
    # This ensures published Helm charts use specific semver tags instead of "latest"
    # when use_released_app_versions=True in package_helm_chart_for_release
    if app_versions:
        values_yaml_path = temp_chart_dir / "values.yaml"
        if values_yaml_path.exists():
            with open(values_yaml_path, 'r') as f:
                values_data = yaml.safe_load(f)
            
            # Update imageTag for each app in the apps section
            if 'apps' in values_data:
                for app_name, app_version in app_versions.items():
                    if app_name in values_data['apps']:
                        values_data['apps'][app_name]['imageTag'] = app_version
                        print(f"Updated {app_name} imageTag to {app_version}")
            
            with open(values_yaml_path, 'w') as f:
                yaml.safe_dump(values_data, f, default_flow_style=False, sort_keys=False)
    
    # Use helm package command to create the tarball from the temporary copy
    result = subprocess.run(
        ["helm", "package", str(temp_chart_dir), "-d", str(output_dir)],
        capture_output=True,
        text=True,
        check=False
    )
    
    # Clean up temporary directory
    shutil.rmtree(temp_chart_dir.parent, ignore_errors=True)
    
    if result.returncode != 0:
        raise RuntimeError(f"helm package failed: {result.stderr}")
    
    # Return the expected packaged file path
    packaged_file = output_dir / f"{chart_name}-{chart_version}.tgz"
    if not packaged_file.exists():
        raise FileNotFoundError(f"Expected packaged chart not found: {packaged_file}")
    
    return packaged_file


def generate_helm_repo_index(charts_dir: Path, base_url: str) -> Path:
    """Generate a Helm repository index.yaml file.
    
    Args:
        charts_dir: Directory containing .tgz chart files
        base_url: Base URL where charts will be hosted (e.g., https://org.github.io/repo)
        
    Returns:
        Path to the generated index.yaml
    """
    # Use helm repo index command
    result = subprocess.run(
        ["helm", "repo", "index", str(charts_dir), "--url", base_url],
        capture_output=True,
        text=True,
        check=False
    )
    
    if result.returncode != 0:
        raise RuntimeError(f"helm repo index failed: {result.stderr}")
    
    index_path = charts_dir / "index.yaml"
    if not index_path.exists():
        raise FileNotFoundError(f"Expected index.yaml not found: {index_path}")
    
    return index_path


def merge_helm_repo_index(new_charts_dir: Path, existing_index_path: Optional[Path], base_url: str) -> Path:
    """Generate or update a Helm repository index, merging with existing charts.
    
    Args:
        new_charts_dir: Directory containing new .tgz chart files
        existing_index_path: Path to existing index.yaml (if any)
        base_url: Base URL where charts will be hosted
        
    Returns:
        Path to the generated/updated index.yaml
    """
    # If there's an existing index, copy it to a temporary location first
    if existing_index_path and existing_index_path.exists():
        # Create a temporary file for the existing index to avoid same-file issues
        temp_index = Path(tempfile.mkdtemp()) / "existing-index.yaml"
        shutil.copy(existing_index_path, temp_index)
        
        # Use helm repo index --merge to update
        result = subprocess.run(
            ["helm", "repo", "index", str(new_charts_dir), "--url", base_url, "--merge", str(temp_index)],
            capture_output=True,
            text=True,
            check=False
        )
        
        # Clean up temporary file
        shutil.rmtree(temp_index.parent, ignore_errors=True)
    else:
        # Generate new index
        result = subprocess.run(
            ["helm", "repo", "index", str(new_charts_dir), "--url", base_url],
            capture_output=True,
            text=True,
            check=False
        )
    
    if result.returncode != 0:
        raise RuntimeError(f"helm repo index failed: {result.stderr}")
    
    index_path = new_charts_dir / "index.yaml"
    if not index_path.exists():
        raise FileNotFoundError(f"Expected index.yaml not found: {index_path}")
    
    return index_path


def unpublish_helm_chart_versions(
    index_path: Path,
    chart_name: str,
    versions: List[str]
) -> bool:
    """Remove specific versions of a chart from the Helm repository index.
    
    Args:
        index_path: Path to the index.yaml file
        chart_name: Name of the chart (e.g., "hello-fastapi")
        versions: List of versions to remove (e.g., ["v1.0.0", "v1.1.0"])
        
    Returns:
        True if successful, False otherwise
    """
    if not index_path.exists():
        raise FileNotFoundError(f"Index file not found: {index_path}")
    
    # Load the index
    with open(index_path, 'r') as f:
        index_data = yaml.safe_load(f)
    
    if not index_data or 'entries' not in index_data:
        raise ValueError(f"Invalid index.yaml format: missing 'entries'")
    
    # Find the chart in the index
    if chart_name not in index_data['entries']:
        raise ValueError(f"Chart '{chart_name}' not found in index")
    
    # Get the list of chart versions
    chart_versions = index_data['entries'][chart_name]
    
    # Filter out the versions to unpublish
    original_count = len(chart_versions)
    filtered_versions = [
        v for v in chart_versions 
        if v.get('version') not in versions
    ]
    removed_count = original_count - len(filtered_versions)
    
    if removed_count == 0:
        print(f"Warning: No versions were removed. Versions specified: {versions}")
        print(f"Available versions: {[v.get('version') for v in chart_versions]}")
        return False
    
    # Update the index
    if len(filtered_versions) == 0:
        # Remove the entire chart entry if no versions remain
        del index_data['entries'][chart_name]
        print(f"Removed all versions of '{chart_name}' from index (chart entry deleted)")
    else:
        index_data['entries'][chart_name] = filtered_versions
        print(f"Removed {removed_count} version(s) of '{chart_name}' from index")
    
    # Write the updated index back
    with open(index_path, 'w') as f:
        yaml.safe_dump(index_data, f, default_flow_style=False, sort_keys=False)
    
    print(f"✅ Successfully updated {index_path}")
    return True


def publish_helm_repo_to_github_pages(
    charts_dir: Path,
    repository_owner: str,
    repository_name: str,
    base_url: Optional[str] = None,
    commit_message: Optional[str] = None,
    dry_run: bool = False
) -> bool:
    """Publish Helm charts to GitHub Pages by pushing to gh-pages branch.
    
    Args:
        charts_dir: Directory containing .tgz chart files
        repository_owner: GitHub repository owner
        repository_name: GitHub repository name
        base_url: Base URL for charts (auto-generated if not provided)
        commit_message: Commit message for the update
        dry_run: If True, only show what would be done
        
    Returns:
        True if successful, False otherwise
    """
    if not base_url:
        base_url = f"https://{repository_owner}.github.io/{repository_name}/charts"
    
    if not commit_message:
        commit_message = f"Update Helm repository index - {datetime.now().strftime('%Y-%m-%d %H:%M:%S')}"
    
    workspace_root = find_workspace_root()
    
    # Create a temporary directory for gh-pages work
    with tempfile.TemporaryDirectory() as tmpdir:
        gh_pages_dir = Path(tmpdir) / "gh-pages"
        gh_pages_dir.mkdir()
        
        # Configure git remote URL with authentication token if available
        github_token = os.getenv('GITHUB_TOKEN', '')
        if github_token:
            repo_url = f"https://x-access-token:{github_token}@github.com/{repository_owner}/{repository_name}.git"
        else:
            repo_url = f"https://github.com/{repository_owner}/{repository_name}.git"
        
        print(f"Cloning gh-pages branch from https://github.com/{repository_owner}/{repository_name}.git...")
        result = subprocess.run(
            ["git", "clone", "--branch", "gh-pages", "--depth", "1", repo_url, str(gh_pages_dir)],
            capture_output=True,
            text=True,
            cwd=workspace_root
        )
        
        # If gh-pages doesn't exist, create it as an orphan branch
        if result.returncode != 0:
            print("gh-pages branch doesn't exist, creating orphan branch...")
            result2 = subprocess.run(
                ["git", "clone", "--depth", "1", repo_url, str(gh_pages_dir)],
                capture_output=True,
                text=True,
                cwd=workspace_root
            )
            if result2.returncode != 0:
                print(f"Failed to clone repository for orphan branch: {result2.stderr}")
                return False
            subprocess.run(
                ["git", "checkout", "--orphan", "gh-pages"],
                check=True,
                cwd=gh_pages_dir
            )
            
            # Check if there are any files to remove before running git rm -rf .
            files_to_remove = [f for f in os.listdir(gh_pages_dir) if f != '.git']
            if files_to_remove:
                try:
                    result_rm = subprocess.run(
                        ["git", "rm", "-rf", "."],
                        capture_output=True,
                        text=True,
                        check=True,
                        cwd=gh_pages_dir
                    )
                except subprocess.CalledProcessError as e:
                    print(f"Error removing files from orphan gh-pages branch: {e.stderr}")
                    return False
            
            # Create a basic README for the Helm repo
            readme_content = f"""# Helm Chart Repository

This branch contains Helm charts for {repository_name}.

## Usage

Add this Helm repository:

```bash
helm repo add {repository_name} {base_url}
helm repo update
```

Search for charts:

```bash
helm search repo {repository_name}
```

Install a chart:

```bash
helm install my-release {repository_name}/<chart-name>
```

## Available Charts

See the [index.yaml]({base_url}/index.yaml) for all available charts and versions.
"""
            (gh_pages_dir / "README.md").write_text(readme_content)
            subprocess.run(
                ["git", "add", "README.md"],
                check=True,
                cwd=gh_pages_dir
            )
            subprocess.run(
                ["git", "commit", "-m", "Initialize gh-pages branch for Helm repository"],
                check=True,
                cwd=gh_pages_dir
            )
        
        # Check if there's an existing index.yaml in charts subdirectory
        charts_subdir = gh_pages_dir / "charts"
        charts_subdir.mkdir(parents=True, exist_ok=True)
        existing_index = charts_subdir / "index.yaml"
        
        # Copy new charts to gh-pages/charts directory
        for chart_file in charts_dir.glob("*.tgz"):
            dest = charts_subdir / chart_file.name
            print(f"Adding chart: {chart_file.name}")
            shutil.copy(chart_file, dest)
        
        # Generate or merge index.yaml in charts subdirectory
        print("Generating Helm repository index...")
        if existing_index.exists():
            merge_helm_repo_index(charts_subdir, existing_index, base_url)
        else:
            generate_helm_repo_index(charts_subdir, base_url)
        
        if dry_run:
            print("DRY RUN: Would commit and push the following changes:")
            result = subprocess.run(
                ["git", "status", "--short"],
                capture_output=True,
                text=True,
                cwd=gh_pages_dir
            )
            print(result.stdout)
            return True
        
        # Commit changes
        subprocess.run(
            ["git", "add", "."],
            check=True,
            cwd=gh_pages_dir
        )
        
        # Check if there are changes to commit
        result = subprocess.run(
            ["git", "diff", "--staged", "--quiet"],
            capture_output=True,
            cwd=gh_pages_dir
        )
        
        if result.returncode == 0:
            print("No changes to commit")
            return True
        
        subprocess.run(
            ["git", "commit", "-m", commit_message],
            check=True,
            cwd=gh_pages_dir
        )
        
        # Push to gh-pages
        print(f"Pushing to gh-pages branch...")
        subprocess.run(
            ["git", "push", "origin", "gh-pages"],
            check=True,
            cwd=gh_pages_dir
        )
        
        print(f"✅ Successfully published Helm repository to {base_url}")
        return True

