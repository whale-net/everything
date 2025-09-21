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
from tools.release_helper.images import build_image
from tools.release_helper.metadata import list_all_apps
from tools.release_helper.release import find_app_bazel_target, plan_release, tag_and_push_image
from tools.release_helper.release_notes import generate_release_notes, generate_release_notes_for_all_apps
from tools.release_helper.summary import generate_release_summary
from tools.release_helper.validation import validate_release_version
from tools.release_helper.github_release import create_app_release, create_releases_for_apps, create_releases_for_apps_with_notes

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


@app.command("increment-chart-version")
def increment_chart_version_cmd(
    domain: Annotated[str, typer.Argument(help="Chart domain (e.g., manman, demo)")],
    chart_name: Annotated[str, typer.Argument(help="Chart name (e.g., host_chart)")],
    increment_type: Annotated[str, typer.Argument(help="Increment type: 'minor' or 'patch'")],
):
    """Calculate the next version for a helm chart based on increment type."""
    from tools.release_helper.git import auto_increment_chart_version
    
    if increment_type not in ["minor", "patch"]:
        typer.echo("Error: increment_type must be 'minor' or 'patch'", err=True)
        raise typer.Exit(1)
    
    try:
        new_version = auto_increment_chart_version(domain, chart_name, increment_type)
        typer.echo(f"{domain}-{chart_name}: {new_version}")
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
def list_charts():
    """List all helm charts with release metadata."""
    from tools.release_helper.metadata import list_all_helm_charts
    charts = list_all_helm_charts()
    for chart_info in charts:
        typer.echo(f"{chart_info['name']} (domain: {chart_info['domain']}, target: {chart_info['bazel_target']})")


@app.command()
def build(
    app_name: Annotated[str, typer.Argument(help="App name")],
    platform: Annotated[Optional[str], typer.Option(help="Target platform")] = None,
):
    """Build and load container image."""
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
        base_commit=base_commit
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


@app.command("plan-helm-charts")
def plan_helm_charts(
    event_type: Annotated[str, typer.Option(help="Type of trigger event")],
    domain: Annotated[Optional[str], typer.Option(help="Domain filter (e.g., manman, demo) or 'all'")] = None,
    chart_version: Annotated[Optional[str], typer.Option(help="Chart version")] = None,
    increment_minor: Annotated[bool, typer.Option("--increment-minor", help="Auto-increment minor version (resets patch to 0)")] = False,
    increment_patch: Annotated[bool, typer.Option("--increment-patch", help="Auto-increment patch version")] = False,
    format: Annotated[str, typer.Option(help="Output format")] = "json",
):
    """Plan a helm chart release and output CI matrix."""
    from tools.release_helper.metadata import list_all_helm_charts
    from tools.release_helper.git import auto_increment_chart_version
    
    if event_type not in ["workflow_dispatch", "tag_push", "pull_request", "push", "fallback"]:
        typer.echo("Error: event-type must be one of: workflow_dispatch, tag_push, pull_request, push, fallback", err=True)
        raise typer.Exit(1)
    
    if format not in ["json", "github"]:
        typer.echo("Error: format must be one of: json, github", err=True)
        raise typer.Exit(1)

    # Validate mutually exclusive version options
    version_options = [chart_version is not None, increment_minor, increment_patch]
    if sum(version_options) > 1:
        typer.echo("Error: chart_version, --increment-minor, and --increment-patch are mutually exclusive", err=True)
        raise typer.Exit(1)
    elif sum(version_options) == 0:
        typer.echo("Error: must specify chart_version, --increment-minor, or --increment-patch", err=True)
        raise typer.Exit(1)
    
    # Get all helm charts
    all_charts = list_all_helm_charts()
    
    # Filter by domain if specified
    if domain and domain != "all":
        filtered_charts = [chart for chart in all_charts if chart['domain'] == domain]
    else:
        filtered_charts = all_charts
    
    if not filtered_charts:
        typer.echo(f"No helm charts found for domain: {domain}" if domain != "all" else "No helm charts found")
        if format == "github":
            typer.echo("matrix={\"include\":[]}")
            typer.echo("charts=")
        else:
            typer.echo('{"matrix": {"include": []}, "charts": []}')
        return
    
    # Determine version for each chart
    chart_releases = []
    for chart in filtered_charts:
        if chart_version:
            version = chart_version
        elif increment_minor:
            version = auto_increment_chart_version(chart['domain'], chart['name'], "minor")
        elif increment_patch:
            version = auto_increment_chart_version(chart['domain'], chart['name'], "patch")
        
        chart_releases.append({
            'chart': chart['name'],
            'domain': chart['domain'],
            'bazel_target': chart['bazel_target'],
            'version': version
        })
    
    # Create matrix
    matrix = {
        "include": chart_releases
    }
    
    plan_result = {
        "matrix": matrix,
        "charts": [chart['chart'] for chart in chart_releases],
        "domains": list(set(chart['domain'] for chart in chart_releases)),
        "version": chart_version,  # For legacy compatibility, may be None for increment modes
        "event_type": event_type
    }

    if format == "github":
        # Output GitHub Actions format
        matrix_json = json.dumps(plan_result["matrix"])
        typer.echo(f"matrix={matrix_json}")
        if plan_result["charts"]:
            typer.echo(f"charts={' '.join(plan_result['charts'])}")
        else:
            typer.echo("charts=")
        if plan_result["domains"]:
            typer.echo(f"domains={' '.join(plan_result['domains'])}")
        else:
            typer.echo("domains=")
    else:
        # JSON output
        typer.echo(json.dumps(plan_result, indent=2))


@app.command()
def changes(
    base_commit: Annotated[Optional[str], typer.Option(help="Compare changes against this commit (compares HEAD to this commit, defaults to previous tag)")] = None,
    use_bazel_query: Annotated[bool, typer.Option("--use-bazel-query/--no-bazel-query", help="Use Bazel query for precise dependency analysis")] = True,
):
    """Detect changed apps since a commit."""
    base_commit = base_commit or get_previous_tag()
    if base_commit:
        typer.echo(f"Detecting changes against commit: {base_commit}", err=True)
    else:
        typer.echo("No base commit specified and no previous tag found, considering all apps as changed", err=True)

    changed_apps = detect_changed_apps(base_commit, use_bazel_query=use_bazel_query)
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


@app.command("release-helm-chart")
def release_helm_chart(
    domain: Annotated[str, typer.Argument(help="Chart domain (e.g., manman, demo)")],
    chart_name: Annotated[str, typer.Argument(help="Chart name (e.g., host_chart)")],
    version: Annotated[str, typer.Option(help="Chart version tag")] = "latest",
    commit: Annotated[Optional[str], typer.Option(help="Commit SHA for additional tag")] = None,
    dry_run: Annotated[bool, typer.Option("--dry-run", help="Show what would be done without actually doing it")] = False,
    create_git_tag: Annotated[bool, typer.Option("--create-git-tag", help="Create and push a Git tag for this chart release")] = False,
):
    """Build helm chart and optionally create git tag."""
    from tools.release_helper.git import create_git_tag as git_create_tag, push_git_tag, format_git_tag
    
    # Build the chart
    chart_target = f"//{domain}:{chart_name}"
    
    try:
        if not dry_run:
            # Build the chart
            typer.echo(f"Building helm chart: {chart_target}")
            build_image(chart_target)  # Using the existing build_image function as a placeholder
            typer.echo(f"Chart {domain}/{chart_name} built successfully")
        else:
            typer.echo(f"DRY RUN: Would build chart {chart_target}")
    
        # Create git tag if requested
        if create_git_tag:
            tag_name = f"{domain}-{chart_name}-chart.{version}"
            
            if dry_run:
                typer.echo(f"DRY RUN: Would create git tag: {tag_name}")
                if commit:
                    typer.echo(f"DRY RUN: Would tag commit: {commit}")
            else:
                typer.echo(f"Creating git tag: {tag_name}")
                git_create_tag(tag_name, commit, f"Release {domain}-{chart_name} chart version {version}")
                push_git_tag(tag_name)
                typer.echo(f"Git tag {tag_name} created and pushed")
    
    except Exception as e:
        typer.echo(f"Error releasing helm chart: {e}", err=True)
        raise typer.Exit(1)
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


def main():
    """Main entry point for the CLI."""
    try:
        app()
    except Exception as e:
        typer.echo(f"Error: {e}", err=True)
        raise typer.Exit(1)


if __name__ == "__main__":
    main()
