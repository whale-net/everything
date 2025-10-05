"""
Command line interface for the release helper.
"""

import json
import os
import sys
from typing import Optional

import typer
from typing_extensions import Annotated

from tools.release_helper.changes import detect_changed_apps
from tools.release_helper.git import get_previous_tag
from tools.release_helper.images import build_image, release_multiarch_image
from tools.release_helper.metadata import list_all_apps
from tools.release_helper.release import find_app_bazel_target, plan_release, tag_and_push_image
from tools.release_helper.release_notes import generate_release_notes, generate_release_notes_for_all_apps
from tools.release_helper.summary import generate_release_summary
from tools.release_helper.validation import validate_release_version
from tools.release_helper.github_release import create_app_release, create_releases_for_apps, create_releases_for_apps_with_notes
from tools.release_helper.helm import (
    list_all_helm_charts,
    get_helm_chart_metadata,
    find_helm_chart_bazel_target,
    resolve_app_versions_for_chart,
    package_helm_chart_for_release,
    publish_helm_repo_to_github_pages,
    generate_helm_repo_index,
    merge_helm_repo_index,
    unpublish_helm_chart_versions,
)

app = typer.Typer(help="Release helper for Everything monorepo")


@app.command("list-app-versions")
def list_app_versions(
    app_name: Annotated[Optional[str], typer.Argument(help="App name (optional - lists all apps if not specified)")] = None,
):
    """List versions for apps by checking git tags."""
    from tools.release_helper.git import get_latest_app_version
    from tools.release_helper.metadata import get_app_metadata
    
    if app_name:
        # List versions for specific app
        try:
            bazel_target = find_app_bazel_target(app_name)
            metadata = get_app_metadata(bazel_target)
            latest_version = get_latest_app_version(metadata['domain'], metadata['name'])
            if latest_version:
                typer.echo(f"{app_name}: {latest_version}")
            else:
                typer.echo(f"{app_name}: no versions found")
        except ValueError as e:
            typer.echo(f"Error: {e}", err=True)
            raise typer.Exit(1)
    else:
        # List versions for all apps
        apps = list_all_apps()
        for app_info in apps:
            try:
                metadata = get_app_metadata(app_info['bazel_target'])
                latest_version = get_latest_app_version(metadata['domain'], metadata['name'])
                if latest_version:
                    typer.echo(f"{app_info['name']}: {latest_version}")
                else:
                    typer.echo(f"{app_info['name']}: no versions found")
            except Exception as e:
                typer.echo(f"{app_info['name']}: error - {e}")


@app.command("increment-version")
def increment_version_cmd(
    app_name: Annotated[str, typer.Argument(help="App name")],
    increment_type: Annotated[str, typer.Argument(help="Increment type: 'minor' or 'patch'")],
):
    """Calculate the next version for an app based on increment type."""
    from tools.release_helper.git import auto_increment_version
    from tools.release_helper.metadata import get_app_metadata
    
    if increment_type not in ["minor", "patch"]:
        typer.echo("Error: increment_type must be 'minor' or 'patch'", err=True)
        raise typer.Exit(1)
    
    try:
        bazel_target = find_app_bazel_target(app_name)
        metadata = get_app_metadata(bazel_target)
        new_version = auto_increment_version(metadata['domain'], metadata['name'], increment_type)
        typer.echo(f"{app_name}: {new_version}")
    except ValueError as e:
        typer.echo(f"Error: {e}", err=True)
        raise typer.Exit(1)


@app.command()
def list_apps():
    """List all apps with release metadata."""
    apps = list_all_apps()
    for app_info in apps:
        typer.echo(f"{app_info['name']} (domain: {app_info['domain']}, target: {app_info['bazel_target']})")


@app.command()
def build(
    app_name: Annotated[str, typer.Argument(help="App name")],
    platform: Annotated[Optional[str], typer.Option(help="Target platform (amd64, arm64)")] = None,
):
    """Build and load container image for a specific platform."""
    # Try to find the app by name first, then use as bazel target if not found
    try:
        from tools.release_helper.release import find_app_bazel_target
        bazel_target = find_app_bazel_target(app_name)
    except ValueError:
        # Maybe it's already a bazel target
        bazel_target = app_name
    
    image_tag = build_image(bazel_target, platform)
    typer.echo(f"Image loaded as: {image_tag}")


@app.command()
def release_multiarch(
    app_name: Annotated[str, typer.Argument(help="App name")],
    version: Annotated[str, typer.Option(help="Version tag")] = "latest",
    platforms: Annotated[Optional[str], typer.Option(help="Comma-separated list of platforms (default: amd64,arm64)")] = None,
    registry: Annotated[str, typer.Option(help="Container registry")] = "ghcr.io",
    commit: Annotated[Optional[str], typer.Option(help="Commit SHA for additional tag")] = None,
    dry_run: Annotated[bool, typer.Option("--dry-run", help="Show what would be pushed without actually pushing")] = False,
):
    """Build and release multi-architecture container images with manifest lists.
    
    This command:
    1. Builds container images for multiple architectures
    2. Pushes platform-specific tags (e.g., app:v1.0.0-amd64, app:v1.0.0-arm64)
    3. Creates manifest lists that automatically serve the correct architecture
    4. Pushes manifest lists (e.g., app:v1.0.0 points to both architectures)
    """
    # Parse platforms
    platform_list = platforms.split(",") if platforms else ["amd64", "arm64"]
    platform_list = [p.strip() for p in platform_list]
    
    # Find the app
    try:
        from tools.release_helper.release import find_app_bazel_target
        from tools.release_helper.metadata import get_app_metadata
        bazel_target = find_app_bazel_target(app_name)
        metadata = get_app_metadata(bazel_target)
        domain = metadata["domain"]
        actual_app_name = metadata["name"]
        image_name = f"{domain}-{actual_app_name}"
    except ValueError:
        bazel_target = app_name
        domain = "unknown"
        actual_app_name = app_name
        image_name = app_name
    
    if dry_run:
        typer.echo("=" * 80)
        typer.echo("DRY RUN: Multi-architecture release plan")
        typer.echo("=" * 80)
        typer.echo(f"App: {actual_app_name}")
        typer.echo(f"Version: {version}")
        typer.echo(f"Platforms: {', '.join(platform_list)}")
        typer.echo(f"Registry: {registry}")
        typer.echo("")
        
        # Show what platform-specific images would be pushed
        typer.echo("Platform-specific images that would be pushed:")
        for platform in platform_list:
            owner = os.environ.get("GITHUB_REPOSITORY_OWNER", "whale-net").lower()
            repo_path = f"{registry}/{owner}/{image_name}"
            typer.echo(f"  - {repo_path}:{version}-{platform}")
            typer.echo(f"  - {repo_path}:latest-{platform}")
            if commit:
                typer.echo(f"  - {repo_path}:{commit}-{platform}")
        
        typer.echo("")
        typer.echo("Manifest lists that would be created (auto-select platform):")
        owner = os.environ.get("GITHUB_REPOSITORY_OWNER", "whale-net").lower()
        repo_path = f"{registry}/{owner}/{image_name}"
        typer.echo(f"  - {repo_path}:{version} → points to all platforms")
        typer.echo(f"  - {repo_path}:latest → points to all platforms")
        if commit:
            typer.echo(f"  - {repo_path}:{commit} → points to all platforms")
        
        typer.echo("")
        typer.echo("=" * 80)
        typer.echo("DRY RUN: No images were actually built or pushed")
        typer.echo("=" * 80)
        return
    
    # Perform multi-architecture release
    typer.echo(f"Starting multi-architecture release for {app_name}")
    typer.echo(f"Version: {version}")
    typer.echo(f"Platforms: {', '.join(platform_list)}")
    typer.echo(f"Registry: {registry}")
    
    try:
        release_multiarch_image(
            bazel_target=bazel_target,
            version=version,
            registry=registry,
            platforms=platform_list,
            commit_sha=commit
        )
        typer.echo(f"✅ Successfully released {actual_app_name}:{version} for {len(platform_list)} platforms")
        owner = os.environ.get("GITHUB_REPOSITORY_OWNER", "whale-net").lower()
        typer.echo(f"Users can now run: docker pull {registry}/{owner}/{image_name}:{version}")
        
    except Exception as e:
        typer.echo(f"❌ Failed to release multi-architecture image: {e}", err=True)
        raise typer.Exit(1)


@app.command()
def release(
    app_name: Annotated[str, typer.Argument(help="App name")],
    version: Annotated[str, typer.Option(help="Version tag")] = "latest",
    commit: Annotated[Optional[str], typer.Option(help="Commit SHA for additional tag")] = None,
    dry_run: Annotated[bool, typer.Option("--dry-run", help="Show what would be pushed without actually pushing")] = False,
    allow_overwrite: Annotated[bool, typer.Option("--allow-overwrite", help="Allow overwriting existing versions (dangerous!)")] = False,
    create_git_tag: Annotated[bool, typer.Option("--create-git-tag", help="Create and push a Git tag for this release")] = False,
):
    """Build, tag, and push container image."""
    tag_and_push_image(app_name, version, commit, dry_run, allow_overwrite, create_git_tag)


@app.command()
def plan(
    event_type: Annotated[str, typer.Option(help="Type of trigger event")],
    apps: Annotated[Optional[str], typer.Option(help="Comma-separated list of apps, domain names, or 'all' (for manual releases)")] = None,
    version: Annotated[Optional[str], typer.Option(help="Release version")] = None,
    increment_minor: Annotated[bool, typer.Option("--increment-minor", help="Auto-increment minor version (resets patch to 0)")] = False,
    increment_patch: Annotated[bool, typer.Option("--increment-patch", help="Auto-increment patch version")] = False,
    base_commit: Annotated[Optional[str], typer.Option(help="Compare changes against this commit (compares HEAD to this commit)")] = None,
    format: Annotated[str, typer.Option(help="Output format")] = "json",
    include_demo: Annotated[bool, typer.Option("--include-demo", help="Include demo domain apps when using 'all'")] = False,
):
    """Plan a release and output CI matrix."""
    if event_type not in ["workflow_dispatch", "tag_push", "pull_request", "push", "fallback"]:
        typer.echo("Error: event-type must be one of: workflow_dispatch, tag_push, pull_request, push, fallback", err=True)
        raise typer.Exit(1)
    
    if format not in ["json", "github"]:
        typer.echo("Error: format must be one of: json, github", err=True)
        raise typer.Exit(1)

    # Validate mutually exclusive version options
    version_options = [version is not None, increment_minor, increment_patch]
    if sum(version_options) > 1:
        typer.echo("Error: version, --increment-minor, and --increment-patch are mutually exclusive", err=True)
        raise typer.Exit(1)
    
    # Determine version mode
    version_mode = None
    if version is not None:
        version_mode = "specific"
    elif increment_minor:
        version_mode = "increment_minor"
    elif increment_patch:
        version_mode = "increment_patch"

    plan_result = plan_release(
        event_type=event_type,
        requested_apps=apps,
        version=version,
        version_mode=version_mode,
        base_commit=base_commit,
        include_demo=include_demo
    )

    if format == "github":
        # Output GitHub Actions format
        matrix_json = json.dumps(plan_result["matrix"])
        typer.echo(f"matrix={matrix_json}")
        if plan_result["apps"]:
            typer.echo(f"apps={' '.join(plan_result['apps'])}")
        else:
            typer.echo("apps=")
    else:
        # JSON output
        typer.echo(json.dumps(plan_result, indent=2))


@app.command()
def changes(
    base_commit: Annotated[Optional[str], typer.Option(help="Compare changes against this commit (compares HEAD to this commit, defaults to previous tag)")] = None,
):
    """Detect changed apps since a commit."""
    base_commit = base_commit or get_previous_tag()
    if base_commit:
        typer.echo(f"Detecting changes against commit: {base_commit}", err=True)
    else:
        typer.echo("No base commit specified and no previous tag found, considering all apps as changed", err=True)

    changed_apps = detect_changed_apps(base_commit)
    for app_info in changed_apps:
        typer.echo(app_info['name'])  # Print just the app name for compatibility


@app.command("validate-version")
def validate_version_cmd(
    app_name: Annotated[str, typer.Argument(help="App name")],
    version: Annotated[str, typer.Argument(help="Version to validate")],
    allow_overwrite: Annotated[bool, typer.Option("--allow-overwrite", help="Allow overwriting existing versions")] = False,
):
    """Validate version format and availability."""
    try:
        # Try to find the app by name first
        from tools.release_helper.release import find_app_bazel_target
        bazel_target = find_app_bazel_target(app_name)
        validate_release_version(bazel_target, version, allow_overwrite)
        typer.echo(f"✓ Version '{version}' is valid for app '{app_name}'")
    except ValueError as e:
        typer.echo(f"Version validation failed: {e}", err=True)
        raise typer.Exit(1)


@app.command()
def summary(
    matrix: Annotated[str, typer.Option(help="Release matrix JSON")],
    version: Annotated[str, typer.Option(help="Release version")],
    event_type: Annotated[str, typer.Option(help="Event type")],
    dry_run: Annotated[bool, typer.Option("--dry-run", help="Whether this was a dry run")] = False,
    repository_owner: Annotated[str, typer.Option(help="GitHub repository owner")] = "",
):
    """Generate release summary for GitHub Actions."""
    if event_type not in ["workflow_dispatch", "tag_push"]:
        typer.echo("Error: event-type must be one of: workflow_dispatch, tag_push", err=True)
        raise typer.Exit(1)

    summary_result = generate_release_summary(
        matrix_json=matrix,
        version=version,
        event_type=event_type,
        dry_run=dry_run,
        repository_owner=repository_owner
    )
    typer.echo(summary_result)


@app.command()
def release_notes(
    app_name: Annotated[str, typer.Argument(help="App name to generate release notes for")],
    current_tag: Annotated[str, typer.Option("--current-tag", help="Current tag/version")] = "HEAD",
    previous_tag: Annotated[Optional[str], typer.Option("--previous-tag", help="Previous tag to compare against (auto-detected if not provided)")] = None,
    format_type: Annotated[str, typer.Option("--format", help="Output format")] = "markdown",
):
    """Generate release notes for a specific app."""
    if format_type not in ["markdown", "plain", "json"]:
        typer.echo("Error: format must be one of: markdown, plain, json", err=True)
        raise typer.Exit(1)
    
    try:
        notes = generate_release_notes(app_name, current_tag, previous_tag, format_type)
        typer.echo(notes)
    except Exception as e:
        typer.echo(f"Error generating release notes: {e}", err=True)
        raise typer.Exit(1)


@app.command()
def release_notes_all(
    current_tag: Annotated[str, typer.Option("--current-tag", help="Current tag/version")] = "HEAD",
    previous_tag: Annotated[Optional[str], typer.Option("--previous-tag", help="Previous tag to compare against (auto-detected if not provided)")] = None,
    format_type: Annotated[str, typer.Option("--format", help="Output format")] = "markdown",
    output_dir: Annotated[Optional[str], typer.Option("--output-dir", help="Directory to save release notes files")] = None,
):
    """Generate release notes for all apps."""
    if format_type not in ["markdown", "plain", "json"]:
        typer.echo("Error: format must be one of: markdown, plain, json", err=True)
        raise typer.Exit(1)
    
    try:
        all_notes = generate_release_notes_for_all_apps(current_tag, previous_tag, format_type)
        
        if output_dir:
            import os
            from pathlib import Path
            from tools.release_helper.metadata import list_all_apps
            
            Path(output_dir).mkdir(parents=True, exist_ok=True)
            
            # Get app domain information for proper file naming
            all_apps = list_all_apps()
            app_domain_map = {app['name']: app['domain'] for app in all_apps}
            
            for app_name, notes in all_notes.items():
                ext = "md" if format_type == "markdown" else "txt" if format_type == "plain" else "json"
                domain = app_domain_map.get(app_name, "unknown")
                file_path = Path(output_dir) / f"{domain}-{app_name}-{current_tag}.{ext}"
                
                with open(file_path, 'w') as f:
                    f.write(notes)
                    
                typer.echo(f"Release notes for {domain}-{app_name} saved to {file_path}")
        else:
            # Output all to stdout  
            from tools.release_helper.metadata import list_all_apps
            
            # Get app domain information for proper display
            all_apps = list_all_apps()
            app_domain_map = {app['name']: app['domain'] for app in all_apps}
            
            for app_name, notes in all_notes.items():
                domain = app_domain_map.get(app_name, "unknown")
                typer.echo(f"{'='*60}")
                typer.echo(f"Release Notes for {domain}-{app_name}")
                typer.echo(f"{'='*60}")
                typer.echo(notes)
                typer.echo()
                
    except Exception as e:
        typer.echo(f"Error generating release notes: {e}", err=True)
        raise typer.Exit(1)


@app.command("create-github-release")
def create_github_release(
    app_name: Annotated[str, typer.Argument(help="App name to create release for")],
    tag_name: Annotated[str, typer.Option("--tag", help="Git tag name for the release")],
    owner: Annotated[str, typer.Option("--owner", help="Repository owner")] = "",
    repo: Annotated[str, typer.Option("--repo", help="Repository name")] = "",
    commit_sha: Annotated[Optional[str], typer.Option("--commit", help="Specific commit SHA to target")] = None,
    prerelease: Annotated[bool, typer.Option("--prerelease", help="Mark as prerelease")] = False,
    previous_tag: Annotated[Optional[str], typer.Option("--previous-tag", help="Previous tag to compare against (auto-detected if not provided)")] = None,
):
    """Create a GitHub release for a specific app."""
    try:
        # Generate release notes
        typer.echo(f"Generating release notes for {app_name}...")
        release_notes = generate_release_notes(app_name, tag_name, previous_tag, "markdown")
        
        # Create GitHub release
        typer.echo(f"Creating GitHub release for {app_name}...")
        result = create_app_release(
            app_name=app_name,
            tag_name=tag_name,
            release_notes=release_notes,
            owner=owner,
            repo=repo,
            commit_sha=commit_sha,
            prerelease=prerelease
        )
        
        if result:
            if "html_url" in result:
                typer.echo(f"✅ GitHub release created: {result['html_url']}")
            else:
                typer.echo(f"ℹ️  {result.get('message', 'Release processed successfully')}")
        else:
            typer.echo("❌ Failed to create GitHub release", err=True)
            raise typer.Exit(1)
            
    except Exception as e:
        typer.echo(f"Error creating GitHub release: {e}", err=True)
        raise typer.Exit(1)


@app.command("create-combined-github-release-with-notes")
def create_combined_github_release_with_notes(
    version: Annotated[str, typer.Argument(help="Release version (can be empty if using matrix with per-app versions)")],
    owner: Annotated[str, typer.Option("--owner", help="Repository owner")] = "",
    repo: Annotated[str, typer.Option("--repo", help="Repository name")] = "",
    commit_sha: Annotated[Optional[str], typer.Option("--commit", help="Specific commit SHA to target")] = None,
    prerelease: Annotated[bool, typer.Option("--prerelease", help="Mark as prerelease")] = False,
    previous_tag: Annotated[Optional[str], typer.Option("--previous-tag", help="Previous tag to compare against (auto-detected if not provided)")] = None,
    apps: Annotated[Optional[str], typer.Option("--apps", help="Comma-separated list of apps to include (defaults to all)")] = None,
    release_notes_dir: Annotated[Optional[str], typer.Option("--release-notes-dir", help="Directory containing pre-generated release notes files")] = None,
):
    """Create GitHub releases for multiple apps using pre-generated release notes."""
    try:
        # Check if we have a MATRIX environment variable with per-app versions
        matrix_env = os.getenv('MATRIX')
        app_versions = {}
        
        if matrix_env:
            try:
                matrix_data = json.loads(matrix_env)
                for item in matrix_data.get('include', []):
                    app_name = item.get('app')
                    app_version = item.get('version')
                    if app_name and app_version:
                        app_versions[app_name] = app_version
                        
                if app_versions:
                    typer.echo(f"Found per-app versions in matrix: {app_versions}")
            except (json.JSONDecodeError, KeyError) as e:
                typer.echo(f"Warning: Failed to parse MATRIX environment variable: {e}", err=True)
        
        # Determine which apps to include
        if apps:
            app_list = [app.strip() for app in apps.split(',')]
        else:
            # Get all apps
            all_apps = list_all_apps()
            app_list = [app['name'] for app in all_apps]
        
        # Create releases for all specified apps using pre-generated notes
        typer.echo(f"Creating GitHub releases for {len(app_list)} apps using pre-generated release notes...")
        
        # Validate that we have either a version or per-app versions
        if not app_versions and not version:
            typer.echo("❌ No version specified and no per-app versions found in matrix", err=True)
            raise typer.Exit(1)
        
        # Use the enhanced function that can handle both single version and per-app versions
        results = create_releases_for_apps_with_notes(
            app_list=app_list,
            version=version if not app_versions else None,
            owner=owner,
            repo=repo,
            commit_sha=commit_sha,
            prerelease=prerelease,
            previous_tag=previous_tag,
            release_notes_dir=release_notes_dir,
            app_versions=app_versions if app_versions else None
        )
        
        # Report results
        successful_releases = [app for app, result in results.items() if result is not None]
        failed_releases = [app for app, result in results.items() if result is None]
        
        if successful_releases:
            typer.echo(f"✅ Successfully created releases for: {', '.join(successful_releases)}")
        
        if failed_releases:
            typer.echo(f"❌ Failed to create releases for: {', '.join(failed_releases)}", err=True)
            raise typer.Exit(1)
        
        if not successful_releases:
            typer.echo("❌ No releases were created successfully", err=True)
            raise typer.Exit(1)
            
    except Exception as e:
        typer.echo(f"Error creating GitHub releases: {e}", err=True)
        raise typer.Exit(1)


@app.command("create-combined-github-release")
def create_combined_github_release(
    version: Annotated[str, typer.Argument(help="Release version")],
    owner: Annotated[str, typer.Option("--owner", help="Repository owner")] = "",
    repo: Annotated[str, typer.Option("--repo", help="Repository name")] = "",
    commit_sha: Annotated[Optional[str], typer.Option("--commit", help="Specific commit SHA to target")] = None,
    prerelease: Annotated[bool, typer.Option("--prerelease", help="Mark as prerelease")] = False,
    previous_tag: Annotated[Optional[str], typer.Option("--previous-tag", help="Previous tag to compare against (auto-detected if not provided)")] = None,
    apps: Annotated[Optional[str], typer.Option("--apps", help="Comma-separated list of apps to include (defaults to all)")] = None,
):
    """Create GitHub releases for multiple apps."""
    try:
        # Determine which apps to include
        if apps:
            app_list = [app.strip() for app in apps.split(',')]
        else:
            # Get all apps
            all_apps = list_all_apps()
            app_list = [app['name'] for app in all_apps]
        
        # Create releases for all specified apps
        typer.echo(f"Creating GitHub releases for {len(app_list)} apps...")
        results = create_releases_for_apps(
            app_list=app_list,
            version=version,
            owner=owner,
            repo=repo,
            commit_sha=commit_sha,
            prerelease=prerelease,
            previous_tag=previous_tag
        )
        
        # Report results
        successful_releases = [app for app, result in results.items() if result is not None]
        failed_releases = [app for app, result in results.items() if result is None]
        
        if successful_releases:
            typer.echo(f"✅ Successfully created releases for: {', '.join(successful_releases)}")
        
        if failed_releases:
            typer.echo(f"❌ Failed to create releases for: {', '.join(failed_releases)}", err=True)
            raise typer.Exit(1)
        
        if not successful_releases:
            typer.echo("❌ No releases were created successfully", err=True)
            raise typer.Exit(1)
            
    except Exception as e:
        typer.echo(f"Error creating GitHub releases: {e}", err=True)
        raise typer.Exit(1)


# Helm Chart Commands

@app.command("list-helm-charts")
def list_helm_charts_cmd():
    """List all helm charts with release metadata."""
    charts = list_all_helm_charts()
    for chart_info in charts:
        apps_str = ', '.join(chart_info['apps']) if chart_info['apps'] else 'none'
        typer.echo(f"{chart_info['name']} (domain: {chart_info['domain']}, namespace: {chart_info['namespace']}, apps: {apps_str})")


@app.command("helm-chart-info")
def helm_chart_info(
    chart_name: Annotated[str, typer.Argument(help="Helm chart name")],
):
    """Get detailed information about a helm chart."""
    try:
        chart_target = find_helm_chart_bazel_target(chart_name)
        metadata = get_helm_chart_metadata(chart_target)
        
        typer.echo(f"Chart: {metadata['name']}")
        typer.echo(f"Domain: {metadata['domain']}")
        typer.echo(f"Namespace: {metadata['namespace']}")
        typer.echo(f"Environment: {metadata['environment']}")
        typer.echo(f"Version: {metadata['version']}")
        typer.echo(f"Apps: {', '.join(metadata.get('apps', []))}")
        typer.echo(f"Bazel Target: {chart_target}")
        
    except ValueError as e:
        typer.echo(f"Error: {e}", err=True)
        raise typer.Exit(1)


@app.command("resolve-chart-app-versions")
def resolve_chart_app_versions(
    chart_name: Annotated[str, typer.Argument(help="Helm chart name")],
    use_released: Annotated[bool, typer.Option("--use-released/--use-latest", help="Use released versions from git tags or 'latest'")] = True,
):
    """Resolve app versions for a helm chart."""
    try:
        chart_target = find_helm_chart_bazel_target(chart_name)
        metadata = get_helm_chart_metadata(chart_target)
        
        app_versions = resolve_app_versions_for_chart(metadata, use_released)
        
        typer.echo(f"App versions for chart '{chart_name}':")
        for app_name, version in app_versions.items():
            typer.echo(f"  {app_name}: {version}")
            
    except ValueError as e:
        typer.echo(f"Error: {e}", err=True)
        raise typer.Exit(1)


@app.command("build-helm-chart")
def build_helm_chart_cmd(
    chart_name: Annotated[str, typer.Argument(help="Helm chart name")],
    chart_version: Annotated[Optional[str], typer.Option("--version", help="Explicit chart version (if not provided, will auto-version)")] = None,
    output_dir: Annotated[Optional[str], typer.Option("--output-dir", help="Output directory for packaged chart")] = None,
    use_released_versions: Annotated[bool, typer.Option("--use-released/--use-latest", help="Use released app versions or 'latest'")] = True,
    auto_version: Annotated[bool, typer.Option("--auto-version", help="Automatically determine chart version from git tags")] = True,
    bump_type: Annotated[str, typer.Option("--bump", help="Version bump type: major, minor, or patch")] = "patch",
):
    """Build and package a helm chart with automatic or manual versioning.
    
    By default, uses automatic versioning (--auto-version) which reads the current
    chart version from git tags (helm/<chart-name>/v*) and increments it based on
    --bump type (patch by default).
    
    To use manual versioning, provide --version and use --no-auto-version.
    """
    try:
        from pathlib import Path
        
        output_path_obj = Path(output_dir) if output_dir else None
        
        # Validate inputs
        if not auto_version and not chart_version:
            typer.echo("❌ Error: --version must be provided when --no-auto-version is used", err=True)
            raise typer.Exit(1)
        
        if bump_type not in ["major", "minor", "patch"]:
            typer.echo("❌ Error: --bump must be one of: major, minor, patch", err=True)
            raise typer.Exit(1)
        
        chart_path, version_used = package_helm_chart_for_release(
            chart_name=chart_name,
            chart_version=chart_version,
            output_dir=output_path_obj,
            use_released_app_versions=use_released_versions,
            auto_version=auto_version,
            bump_type=bump_type
        )
        
        typer.echo(f"✅ Chart packaged: {chart_path}")
        typer.echo(f"📦 Version: {version_used}")
        
    except Exception as e:
        typer.echo(f"❌ Failed to build helm chart: {e}", err=True)
        raise typer.Exit(1)


@app.command("plan-helm-release")
def plan_helm_release(
    charts: Annotated[Optional[str], typer.Option(help="Comma-separated list of chart names, domain names, or 'all'")] = None,
    version: Annotated[Optional[str], typer.Option(help="Release version for charts")] = None,
    format: Annotated[str, typer.Option(help="Output format")] = "json",
    include_demo: Annotated[bool, typer.Option("--include-demo", help="Include demo domain charts when using 'all'")] = False,
):
    """Plan a helm chart release and output CI matrix."""
    if format not in ["json", "github"]:
        typer.echo("Error: format must be one of: json, github", err=True)
        raise typer.Exit(1)

    # Get all helm charts
    all_charts = list_all_helm_charts()
    
    # Filter charts based on input
    selected_charts = []
    if charts:
        chart_input = charts.strip().lower()
        if chart_input == "all":
            selected_charts = all_charts
            # Exclude demo domain unless explicitly included
            if not include_demo:
                selected_charts = [c for c in selected_charts if c['domain'] != 'demo']
                typer.echo("Excluding demo domain charts from 'all' (use --include-demo to include)", err=True)
        else:
            # Parse comma-separated list
            requested = [c.strip() for c in chart_input.split(',')]
            
            for req in requested:
                # Check if it's a domain or chart name
                matching = [c for c in all_charts if c['name'] == req or c['domain'] == req]
                selected_charts.extend(matching)
            
            # Remove duplicates
            seen = set()
            selected_charts = [c for c in selected_charts if not (c['name'] in seen or seen.add(c['name']))]
    else:
        # Default to all charts
        selected_charts = all_charts
    
    if not selected_charts:
        typer.echo("No charts selected for release", err=True)
        plan_result = {"matrix": {"include": []}, "charts": []}
    else:
        # Build matrix
        matrix_include = []
        for chart in selected_charts:
            matrix_include.append({
                "chart": chart['name'],
                "domain": chart['domain'],
                "version": version or "0.1.0",
            })
        
        plan_result = {
            "matrix": {"include": matrix_include},
            "charts": [c['name'] for c in selected_charts]
        }
    
    if format == "github":
        # Output GitHub Actions format
        matrix_json = json.dumps(plan_result["matrix"])
        typer.echo(f"matrix={matrix_json}")
        if plan_result["charts"]:
            typer.echo(f"charts={' '.join(plan_result['charts'])}")
        else:
            typer.echo("charts=")
    else:
        # JSON output
        typer.echo(json.dumps(plan_result, indent=2))


@app.command("publish-helm-repo")
def publish_helm_repo(
    charts_dir: Annotated[str, typer.Argument(help="Directory containing .tgz chart files")],
    owner: Annotated[str, typer.Option("--owner", help="GitHub repository owner")] = "",
    repo: Annotated[str, typer.Option("--repo", help="GitHub repository name")] = "",
    base_url: Annotated[Optional[str], typer.Option("--base-url", help="Base URL for charts (auto-generated if not provided)")] = None,
    commit_message: Annotated[Optional[str], typer.Option("--commit-message", help="Commit message for the update")] = None,
    dry_run: Annotated[bool, typer.Option("--dry-run", help="Show what would be done without pushing")] = False,
):
    """Publish Helm charts to GitHub Pages by pushing to gh-pages branch.
    
    This command will:
    1. Clone or create the gh-pages branch
    2. Add new chart packages (.tgz files)
    3. Generate or update the Helm repository index.yaml
    4. Commit and push changes to gh-pages
    """
    try:
        from pathlib import Path
        
        charts_path = Path(charts_dir)
        if not charts_path.exists():
            typer.echo(f"Error: Charts directory not found: {charts_dir}", err=True)
            raise typer.Exit(1)
        
        # Check if there are any .tgz files
        chart_files = list(charts_path.glob("*.tgz"))
        if not chart_files:
            typer.echo(f"Error: No .tgz chart files found in {charts_dir}", err=True)
            raise typer.Exit(1)
        
        typer.echo(f"Found {len(chart_files)} chart(s) to publish:")
        for chart_file in chart_files:
            typer.echo(f"  - {chart_file.name}")
        
        # Use environment variables if owner/repo not provided
        if not owner:
            owner = os.getenv('GITHUB_REPOSITORY_OWNER', '')
        if not repo:
            repo_full = os.getenv('GITHUB_REPOSITORY', '')
            if '/' in repo_full:
                repo = repo_full.split('/')[-1]
        
        if not owner or not repo:
            typer.echo("Error: --owner and --repo are required (or set GITHUB_REPOSITORY_OWNER and GITHUB_REPOSITORY env vars)", err=True)
            raise typer.Exit(1)
        
        if dry_run:
            typer.echo(f"\nDRY RUN: Would publish to https://{owner}.github.io/{repo}")
        else:
            typer.echo(f"\nPublishing to https://{owner}.github.io/{repo}")
        
        # Publish to GitHub Pages
        success = publish_helm_repo_to_github_pages(
            charts_dir=charts_path,
            repository_owner=owner,
            repository_name=repo,
            base_url=base_url,
            commit_message=commit_message,
            dry_run=dry_run
        )
        
        if success:
            typer.echo(f"\n✅ Successfully published Helm repository!")
            if not dry_run:
                typer.echo(f"\nUsers can now add the repository with:")
                typer.echo(f"  helm repo add {repo} https://{owner}.github.io/{repo}/charts")
                typer.echo(f"  helm repo update")
        else:
            typer.echo("❌ Failed to publish Helm repository", err=True)
            raise typer.Exit(1)
            
    except Exception as e:
        typer.echo(f"Error publishing Helm repository: {e}", err=True)
        raise typer.Exit(1)


@app.command("generate-helm-index")
def generate_helm_index_cmd(
    charts_dir: Annotated[str, typer.Argument(help="Directory containing .tgz chart files")],
    base_url: Annotated[str, typer.Option("--base-url", help="Base URL where charts will be hosted")],
    merge_with: Annotated[Optional[str], typer.Option("--merge-with", help="Path to existing index.yaml to merge with")] = None,
):
    """Generate a Helm repository index.yaml file.
    
    This creates or updates an index.yaml file that catalogs all Helm charts in the directory.
    If --merge-with is provided, it will merge with an existing index to preserve history.
    """
    try:
        from pathlib import Path
        
        charts_path = Path(charts_dir)
        if not charts_path.exists():
            typer.echo(f"Error: Charts directory not found: {charts_dir}", err=True)
            raise typer.Exit(1)
        
        # Check if there are any .tgz files
        chart_files = list(charts_path.glob("*.tgz"))
        if not chart_files:
            typer.echo(f"Warning: No .tgz chart files found in {charts_dir}", err=True)
        
        typer.echo(f"Generating Helm repository index for {len(chart_files)} chart(s)...")
        
        if merge_with:
            merge_path = Path(merge_with)
            if not merge_path.exists():
                typer.echo(f"Warning: Merge file not found: {merge_with}, creating new index", err=True)
                merge_path = None
            else:
                typer.echo(f"Merging with existing index: {merge_with}")
            
            index_path = merge_helm_repo_index(charts_path, merge_path, base_url)
        else:
            index_path = generate_helm_repo_index(charts_path, base_url)
        
        typer.echo(f"✅ Generated index: {index_path}")
        
    except Exception as e:
        typer.echo(f"Error generating Helm index: {e}", err=True)
        raise typer.Exit(1)


@app.command("unpublish-helm-chart")
def unpublish_helm_chart_cmd(
    index_file: Annotated[str, typer.Argument(help="Path to the index.yaml file")],
    chart_name: Annotated[str, typer.Option("--chart", help="Name of the chart to unpublish versions from")],
    versions: Annotated[str, typer.Option("--versions", help="Comma-separated list of versions to unpublish (e.g., 'v1.0.0,v1.1.0')")],
):
    """Remove specific versions of a chart from the Helm repository index.
    
    This command modifies the index.yaml file to remove specified versions of a chart.
    The actual .tgz files are NOT deleted - only removed from the index.
    
    Example:
        bazel run //tools:release -- unpublish-helm-chart /path/to/index.yaml \\
            --chart hello-fastapi --versions v1.0.0,v1.1.0
    """
    try:
        from pathlib import Path
        
        index_path = Path(index_file)
        if not index_path.exists():
            typer.echo(f"Error: Index file not found: {index_file}", err=True)
            raise typer.Exit(1)
        
        # Parse versions
        version_list = [v.strip() for v in versions.split(',')]
        
        typer.echo(f"Unpublishing versions {version_list} of chart '{chart_name}' from {index_file}")
        
        # Unpublish the versions
        success = unpublish_helm_chart_versions(index_path, chart_name, version_list)
        
        if success:
            typer.echo(f"\n✅ Successfully unpublished versions from '{chart_name}'")
            typer.echo(f"Note: The .tgz files were not deleted, only removed from the index")
        else:
            typer.echo("❌ Failed to unpublish chart versions", err=True)
            raise typer.Exit(1)
            
    except Exception as e:
        typer.echo(f"Error unpublishing chart: {e}", err=True)
        raise typer.Exit(1)


def main():
    """Main entry point for the CLI."""
    try:
        app()
    except Exception as e:
        typer.echo(f"Error: {e}", err=True)
        raise typer.Exit(1)


if __name__ == "__main__":
    main()

