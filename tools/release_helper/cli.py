"""
Command line interface for the release helper.
"""

import json
import sys
from typing import Optional

import typer
from typing_extensions import Annotated

from tools.release_helper.changes import detect_changed_apps
from tools.release_helper.git import get_previous_tag
from tools.release_helper.images import build_image
from tools.release_helper.metadata import list_all_apps
from tools.release_helper.release import plan_release, tag_and_push_image
from tools.release_helper.summary import generate_release_summary
from tools.release_helper.validation import validate_release_version

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
    apps: Annotated[Optional[str], typer.Option(help="Comma-separated list of apps or 'all' (for manual releases)")] = None,
    version: Annotated[Optional[str], typer.Option(help="Release version")] = None,
    increment_minor: Annotated[bool, typer.Option("--increment-minor", help="Auto-increment minor version (resets patch to 0)")] = False,
    increment_patch: Annotated[bool, typer.Option("--increment-patch", help="Auto-increment patch version")] = False,
    base_commit: Annotated[Optional[str], typer.Option(help="Compare changes against this commit (compares HEAD to this commit)")] = None,
    format: Annotated[str, typer.Option(help="Output format")] = "json",
):
    """Plan a release and output CI matrix."""
    if event_type not in ["workflow_dispatch", "tag_push"]:
        typer.echo("Error: event-type must be one of: workflow_dispatch, tag_push", err=True)
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


def main():
    """Main entry point for the CLI."""
    try:
        app()
    except Exception as e:
        typer.echo(f"Error: {e}", err=True)
        raise typer.Exit(1)


if __name__ == "__main__":
    main()