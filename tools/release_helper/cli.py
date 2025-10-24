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
from tools.release_helper.release import find_app_bazel_target, plan_release
from tools.release_helper.release_notes import generate_release_notes, generate_release_notes_for_all_apps
from tools.release_helper.summary import generate_release_summary
from tools.release_helper.validation import validate_release_version
from tools.release_helper.github_release import create_app_release, create_releases_for_apps_with_notes
from tools.release_helper.helm import (
    list_all_helm_charts,
    find_helm_chart_bazel_target,
    package_helm_chart_for_release,
    unpublish_helm_chart_versions,
)

app = typer.Typer(help="Release helper for Everything monorepo")


def parse_app_list(apps: str) -> list[str]:
    """Parse app list from either comma or space-separated format.
    
    Args:
        apps: App list string, either comma-separated or space-separated
        
    Returns:
        List of app names with whitespace stripped
        
    Raises:
        ValueError: If apps is empty, None, or results in no valid apps
    """
    if not apps or not apps.strip():
        raise ValueError("App list cannot be empty")
    
    # Split by comma if present, otherwise split by whitespace
    if ',' in apps:
        result = [app.strip() for app in apps.split(',') if app.strip()]
    else:
        result = [app.strip() for app in apps.split() if app.strip()]
    
    if not result:
        raise ValueError("App list resulted in no valid apps after parsing")
    
    return result


@app.command()
def list_apps(
    format: Annotated[Optional[str], typer.Option(help="Output format (text or json)")] = "text",
):
    """List all apps with release metadata."""
    apps = list_all_apps()
    if format == "json":
        import json
        typer.echo(json.dumps(apps, indent=2))
    else:
        for app_info in apps:
            typer.echo(f"{app_info['name']} (domain: {app_info['domain']}, target: {app_info['bazel_target']})")


@app.command()
def list(
    format: Annotated[Optional[str], typer.Option(help="Output format (text or json)")] = "text",
):
    """Alias for list-apps. List all apps with release metadata."""
    apps = list_all_apps()
    if format == "json":
        import json
        typer.echo(json.dumps(apps, indent=2))
    else:
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
        
        # Show the build process
        typer.echo("Build Process:")
        typer.echo(f"  1. Build platform-specific images:")
        for platform in platform_list:
            typer.echo(f"     • {actual_app_name}_image_{platform} (--platforms=//tools:linux_{platform == 'arm64' and 'arm64' or 'x86_64'})")
        typer.echo(f"  2. Build OCI image index: {actual_app_name}_image")
        typer.echo(f"  3. Push image index with all tags")
        
        typer.echo("")
        typer.echo("=" * 80)
        typer.echo("PUBLISHED TAGS (what users will see):")
        typer.echo("=" * 80)
        owner = os.environ.get("GITHUB_REPOSITORY_OWNER", "whale-net").lower()
        repo_path = f"{registry}/{owner}/{image_name}"
        typer.echo(f"  ✅ {repo_path}:{version}")
        typer.echo(f"     └─ OCI image index → auto-selects from: {', '.join(platform_list)}")
        typer.echo("")
        typer.echo(f"  ✅ {repo_path}:latest")
        typer.echo(f"     └─ OCI image index → auto-selects from: {', '.join(platform_list)}")
        if commit:
            typer.echo("")
            typer.echo(f"  ✅ {repo_path}:{commit}")
            typer.echo(f"     └─ OCI image index → auto-selects from: {', '.join(platform_list)}")
        
        typer.echo("")
        typer.echo("=" * 80)
        typer.echo("ℹ️  Only the OCI image index is published (no platform-specific tags).")
        typer.echo("   Docker automatically serves the correct architecture when users pull.")
        typer.echo("=" * 80)
        typer.echo("")
        typer.echo("DRY RUN: No images were actually built or pushed")
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
def plan_openapi_builds(
    apps: Annotated[str, typer.Option(help="Space or comma-separated list of apps to check for OpenAPI specs")],
    format: Annotated[str, typer.Option(help="Output format (json or github)")] = "github",
):
    """Plan OpenAPI spec builds for apps that have fastapi_app configured.
    
    This command filters the input apps to only those that have OpenAPI spec targets,
    avoiding wasteful builds for apps without OpenAPI specs.
    """
    if format not in ["json", "github"]:
        typer.echo("Error: format must be one of: json, github", err=True)
        raise typer.Exit(1)
    
    # Parse app list using helper function
    app_list = parse_app_list(apps)
    
    # Filter to apps with OpenAPI spec targets
    # Use validate_apps to handle all naming formats and detect ambiguity
    from tools.release_helper.validation import validate_apps
    
    try:
        validated_apps = validate_apps(app_list)
    except ValueError as e:
        typer.echo(f"Error validating apps: {e}", err=True)
        raise typer.Exit(1)
    
    apps_with_specs = []
    for app_metadata in validated_apps:
        if app_metadata.get('openapi_spec_target'):
            apps_with_specs.append({
                'app': app_metadata['name'],
                'domain': app_metadata['domain'],
                'openapi_target': app_metadata['openapi_spec_target']
            })
    
    if format == "github":
        # Output GitHub Actions format
        if apps_with_specs:
            matrix = {'include': apps_with_specs}
            matrix_json = json.dumps(matrix)
            typer.echo(f"matrix={matrix_json}")
            # Use domain-app format for consistency with plan_release
            app_names = [f"{app['domain']}-{app['app']}" for app in apps_with_specs]
            typer.echo(f"apps={' '.join(app_names)}")
        else:
            # Empty matrix
            typer.echo("matrix={}")
            typer.echo("apps=")
    else:
        # JSON output
        result = {
            'apps_with_specs': apps_with_specs,
            'count': len(apps_with_specs)
        }
        typer.echo(json.dumps(result, indent=2))


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
    openapi_specs_dir: Annotated[Optional[str], typer.Option("--openapi-specs-dir", help="Directory containing OpenAPI spec files to upload as release assets")] = None,
):
    """Create GitHub releases for multiple apps using pre-generated release notes."""
    try:
        # Check if we have a MATRIX environment variable with per-app versions and domains
        matrix_env = os.getenv('MATRIX')
        app_versions = {}
        app_domains = {}
        
        if matrix_env:
            try:
                matrix_data = json.loads(matrix_env)
                for item in matrix_data.get('include', []):
                    app_name = item.get('app')
                    app_version = item.get('version')
                    app_domain = item.get('domain')
                    if app_name and app_domain:
                        # Use full domain-app format as key to match the app_list format
                        full_app_name = f"{app_domain}-{app_name}"
                        if app_version:
                            app_versions[full_app_name] = app_version
                        # Store domain for lookup (using full name as key)
                        app_domains[full_app_name] = app_domain
                        
                if app_versions:
                    typer.echo(f"Found per-app versions in matrix: {app_versions}")
                if app_domains:
                    typer.echo(f"Found per-app domains in matrix: {app_domains}")
            except (json.JSONDecodeError, KeyError) as e:
                typer.echo(f"Warning: Failed to parse MATRIX environment variable: {e}", err=True)
        
        # Determine which apps to include
        if apps:
            # Parse app list using helper function
            app_list = parse_app_list(apps)
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
            app_versions=app_versions if app_versions else None,
            openapi_specs_dir=openapi_specs_dir,
            app_domains=app_domains if app_domains else None,
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


@app.command("cleanup-releases")
def cleanup_releases_cmd(
    keep_minor_versions: Annotated[int, typer.Option("--keep-minor-versions", help="Number of recent minor versions to keep")] = 2,
    min_age_days: Annotated[int, typer.Option("--min-age-days", help="Minimum age in days for deletion")] = 14,
    dry_run: Annotated[bool, typer.Option("--dry-run/--no-dry-run", help="Preview changes without executing")] = True,
    delete_packages: Annotated[bool, typer.Option("--delete-packages/--no-delete-packages", help="Also delete corresponding GHCR packages")] = True,
):
    """Clean up old Git tags and optionally their corresponding GHCR packages.
    
    This command identifies old releases based on intelligent retention policies:
    - Keeps only the latest patch of each minor version
    - Keeps the last N minor versions (default: 2)
    - Always keeps the latest minor version of each major version
    - Only deletes tags older than the age threshold (default: 14 days)
    
    By default, runs in dry-run mode to preview changes. Use --no-dry-run to actually delete.
    
    When running with --no-dry-run in an interactive terminal, prompts for confirmation
    before proceeding. In CI environments (GitHub Actions) or non-interactive terminals,
    proceeds automatically without prompting.
    
    Examples:
        # Preview what would be deleted (recommended first step)
        bazel run //tools:release -- cleanup-releases
        
        # Actually delete old releases (prompts for confirmation in interactive mode)
        bazel run //tools:release -- cleanup-releases --no-dry-run
        
        # Custom retention policy
        bazel run //tools:release -- cleanup-releases \\
            --keep-minor-versions 3 \\
            --min-age-days 30
        
        # Delete tags only (keep GHCR packages)
        bazel run //tools:release -- cleanup-releases \\
            --no-delete-packages --no-dry-run
    """
    import os
    import sys
    from tools.release_helper.cleanup import ReleaseCleanup
    
    try:
        # Get repository information from environment
        owner = os.environ.get("GITHUB_REPOSITORY_OWNER", "whale-net").lower()
        repo = os.environ.get("GITHUB_REPOSITORY", "whale-net/everything").split("/")[-1]
        
        # Get GitHub token
        token = os.environ.get("GITHUB_TOKEN")
        if not token:
            typer.echo("❌ GITHUB_TOKEN environment variable not set", err=True)
            typer.echo("Please set GITHUB_TOKEN with appropriate permissions:", err=True)
            typer.echo("  - contents:write (for tag deletion)", err=True)
            typer.echo("  - packages:write (for GHCR package deletion)", err=True)
            raise typer.Exit(1)
        
        typer.echo(f"🔍 Analyzing releases for {owner}/{repo}...")
        typer.echo(f"Retention policy:")
        typer.echo(f"  - Keep last {keep_minor_versions} minor versions")
        typer.echo(f"  - Delete only tags older than {min_age_days} days")
        typer.echo(f"  - Delete GHCR packages: {delete_packages}")
        typer.echo("")
        
        # Create cleanup orchestrator
        cleanup = ReleaseCleanup(owner, repo, token)
        
        # Plan the cleanup
        typer.echo("📋 Planning cleanup...")
        plan = cleanup.plan_cleanup(
            keep_minor_versions=keep_minor_versions,
            min_age_days=min_age_days
        )
        
        # Display plan
        typer.echo(f"\n📊 Cleanup Plan:")
        typer.echo(f"  Tags to delete: {plan.total_tag_deletions()}")
        typer.echo(f"  GitHub releases to delete: {plan.total_release_deletions()}")
        typer.echo(f"  Tags to keep: {len(plan.tags_to_keep)}")
        
        if delete_packages:
            typer.echo(f"  GHCR package versions to delete: {plan.total_package_deletions()}")
        
        if plan.is_empty():
            typer.echo("\n✅ Nothing to clean up!")
            return
        
        # Show what will be deleted
        if plan.tags_to_delete:
            typer.echo(f"\n🗑️  Tags marked for deletion ({len(plan.tags_to_delete)}):")
            for tag in plan.tags_to_delete[:10]:  # Show first 10
                release_info = ""
                if tag in plan.releases_to_delete:
                    release_info = f" (+ release)"
                typer.echo(f"  - {tag}{release_info}")
            if len(plan.tags_to_delete) > 10:
                typer.echo(f"  ... and {len(plan.tags_to_delete) - 10} more")
        
        if delete_packages and plan.packages_to_delete:
            typer.echo(f"\n📦 GHCR packages marked for deletion:")
            try:
                # Defensive check: ensure packages_to_delete is not None
                if plan.packages_to_delete is None:
                    typer.echo(f"WARNING: packages_to_delete is None, skipping package display", err=True)
                else:
                    packages_items = plan.packages_to_delete.items()
                    if packages_items is None:
                        typer.echo(f"WARNING: packages_items is None, skipping package display", err=True)
                    else:
                        packages_list = list(packages_items) if packages_items is not None else []
                        if packages_list is None:
                            typer.echo(f"WARNING: packages_list is None after conversion, using empty list", err=True)
                            packages_list = []
                        
                        top_5 = packages_list[:5]
                        for idx, (package_name, version_ids) in enumerate(top_5):
                            if version_ids is None:
                                # This should never happen since we always initialize to []
                                typer.echo(f"WARNING: version_ids is None for package {package_name}! Skipping.", err=True)
                                continue
                            if not isinstance(version_ids, list):
                                typer.echo(f"WARNING: version_ids is not a list for package {package_name}: {type(version_ids)}! Skipping.", err=True)
                                continue
                            typer.echo(f"  - {package_name}: {len(version_ids)} versions")
                        if len(plan.packages_to_delete) > 5:
                            typer.echo(f"  ... and {len(plan.packages_to_delete) - 5} more packages")
            except Exception as e:
                typer.echo(f"ERROR in GHCR package display: {e}", err=True)
                import traceback
                traceback.print_exc()
                # Don't re-raise - allow cleanup to continue
                typer.echo(f"Continuing despite error in package display...", err=True)
        
        # Remove GHCR packages from plan if user doesn't want to delete them
        if not delete_packages:
            plan.packages_to_delete.clear()
        
        # Execute cleanup
        if dry_run:
            typer.echo("\n🧪 DRY RUN MODE - No actual deletions will occur")
            typer.echo("Run with --no-dry-run to actually delete these releases")
        else:
            typer.echo("\n⚠️  WARNING: This will permanently delete tags, releases, and packages!")
            
            # Skip confirmation in CI/non-interactive environments
            is_ci = os.environ.get("CI") == "true" or os.environ.get("GITHUB_ACTIONS") == "true"
            is_interactive = sys.stdin.isatty()
            
            if not is_ci and is_interactive:
                confirm = typer.confirm("Are you sure you want to proceed?")
                if not confirm:
                    typer.echo("Cleanup cancelled.")
                    return
            else:
                if is_ci:
                    typer.echo("Running in CI environment, proceeding with cleanup...")
                else:
                    typer.echo("Running in non-interactive mode, proceeding with cleanup...")
        
        result = cleanup.execute_cleanup(plan, dry_run=dry_run)
        
        # Display results
        typer.echo(f"\n{result.summary()}")
        
        if result.errors:
            typer.echo(f"\n❌ Errors encountered:")
            for error in result.errors[:10]:
                typer.echo(f"  - {error}")
            if len(result.errors) > 10:
                typer.echo(f"  ... and {len(result.errors) - 10} more errors")
        
        if not result.is_successful():
            raise typer.Exit(1)
        
        typer.echo("\n✅ Cleanup complete!")
        
    except Exception as e:
        import traceback
        typer.echo(f"\n❌ Error during cleanup: {e}", err=True)
        typer.echo(f"\nFull traceback:", err=True)
        traceback.print_exc()
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

