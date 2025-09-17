"""
Command line interface for the release helper.
"""

import argparse
import json
import sys

from tools.release_helper.changes import detect_changed_apps
from tools.release_helper.git import get_previous_tag
from tools.release_helper.images import build_image
from tools.release_helper.metadata import get_app_metadata, list_all_apps
from tools.release_helper.release import plan_release, tag_and_push_image
from tools.release_helper.summary import generate_release_summary
from tools.release_helper.validation import validate_apps, validate_release_version


def main():
    parser = argparse.ArgumentParser(description="Release helper for Everything monorepo")
    subparsers = parser.add_subparsers(dest="command", help="Available commands")

    # List apps command
    list_parser = subparsers.add_parser("list", help="List all apps with release metadata")

    # Show metadata command
    metadata_parser = subparsers.add_parser("metadata", help="Show metadata for an app")
    metadata_parser.add_argument("app", help="App name")

    # Build image command
    build_parser = subparsers.add_parser("build", help="Build and load container image")
    build_parser.add_argument("app", help="App name")
    build_parser.add_argument("--platform", choices=["amd64", "arm64"], help="Target platform")

    # Release command
    release_parser = subparsers.add_parser("release", help="Build, tag, and push container image")
    release_parser.add_argument("app", help="App name")
    release_parser.add_argument("--version", default="latest", help="Version tag")
    release_parser.add_argument("--commit", help="Commit SHA for additional tag")
    release_parser.add_argument("--dry-run", action="store_true", help="Show what would be pushed without actually pushing")
    release_parser.add_argument("--allow-overwrite", action="store_true", help="Allow overwriting existing versions (dangerous!)")
    release_parser.add_argument("--create-git-tag", action="store_true", help="Create and push a Git tag for this release")

    # Plan release command (for CI)
    plan_parser = subparsers.add_parser("plan", help="Plan a release and output CI matrix")
    plan_parser.add_argument("--event-type", required=True, choices=["workflow_dispatch", "tag_push"], help="Type of trigger event")
    plan_parser.add_argument("--apps", help="Comma-separated list of apps or 'all' (for manual releases)")
    plan_parser.add_argument("--version", help="Release version")
    plan_parser.add_argument("--since-tag", help="Compare changes since this tag")
    plan_parser.add_argument("--format", choices=["json", "github"], default="json", help="Output format")

    # Detect changes command
    changes_parser = subparsers.add_parser("changes", help="Detect changed apps since a tag")
    changes_parser.add_argument("--since-tag", help="Compare changes since this tag (defaults to previous tag)")

    # Validate apps command
    validate_parser = subparsers.add_parser("validate", help="Validate that apps exist")
    validate_parser.add_argument("apps", nargs="+", help="App names to validate")

    # Validate version command
    validate_version_parser = subparsers.add_parser("validate-version", help="Validate version format and availability")
    validate_version_parser.add_argument("app", help="App name")
    validate_version_parser.add_argument("version", help="Version to validate")
    validate_version_parser.add_argument("--allow-overwrite", action="store_true", help="Allow overwriting existing versions")

    # Summary command (for CI)
    summary_parser = subparsers.add_parser("summary", help="Generate release summary for GitHub Actions")
    summary_parser.add_argument("--matrix", required=True, help="Release matrix JSON")
    summary_parser.add_argument("--version", required=True, help="Release version")
    summary_parser.add_argument("--event-type", required=True, choices=["workflow_dispatch", "tag_push"], help="Event type")
    summary_parser.add_argument("--dry-run", action="store_true", help="Whether this was a dry run")
    summary_parser.add_argument("--repository-owner", default="", help="GitHub repository owner")

    args = parser.parse_args()

    if not args.command:
        parser.print_help()
        return

    try:
        if args.command == "list":
            apps = list_all_apps()
            for app in apps:
                print(f"{app['name']} (domain: {app['domain']}, target: {app['bazel_target']})")

        elif args.command == "metadata":
            # Try to find the app by name first, then use as bazel target if not found
            try:
                from tools.release_helper.release import find_app_bazel_target
                bazel_target = find_app_bazel_target(args.app)
            except ValueError:
                # Maybe it's already a bazel target
                bazel_target = args.app
            
            metadata = get_app_metadata(bazel_target)
            print(json.dumps(metadata, indent=2))

        elif args.command == "build":
            # Try to find the app by name first, then use as bazel target if not found
            try:
                from tools.release_helper.release import find_app_bazel_target
                bazel_target = find_app_bazel_target(args.app)
            except ValueError:
                # Maybe it's already a bazel target
                bazel_target = args.app
            
            image_tag = build_image(bazel_target, args.platform)
            print(f"Image loaded as: {image_tag}")

        elif args.command == "release":
            tag_and_push_image(args.app, args.version, args.commit, args.dry_run, args.allow_overwrite, args.create_git_tag)

        elif args.command == "plan":
            plan = plan_release(
                event_type=args.event_type,
                requested_apps=args.apps,
                version=args.version,
                since_tag=args.since_tag
            )

            if args.format == "github":
                # Output GitHub Actions format
                matrix_json = json.dumps(plan["matrix"])
                print(f"matrix={matrix_json}")
                if plan["apps"]:
                    print(f"apps={' '.join(plan['apps'])}")
                else:
                    print("apps=")
            else:
                # JSON output
                print(json.dumps(plan, indent=2))

        elif args.command == "changes":
            since_tag = args.since_tag or get_previous_tag()
            if since_tag:
                print(f"Detecting changes since tag: {since_tag}", file=sys.stderr)
            else:
                print("No previous tag found, considering all apps as changed", file=sys.stderr)

            changed_apps = detect_changed_apps(since_tag)
            for app in changed_apps:
                print(app['name'])  # Print just the app name for compatibility

        elif args.command == "validate":
            try:
                valid_apps = validate_apps(args.apps)
                app_names = [app['name'] for app in valid_apps]
                print(f"All apps are valid: {', '.join(app_names)}")
            except ValueError as e:
                print(f"Validation failed: {e}", file=sys.stderr)
                sys.exit(1)

        elif args.command == "validate-version":
            try:
                # Try to find the app by name first
                from tools.release_helper.release import find_app_bazel_target
                bazel_target = find_app_bazel_target(args.app)
                validate_release_version(bazel_target, args.version, args.allow_overwrite)
                print(f"✓ Version '{args.version}' is valid for app '{args.app}'")
            except ValueError as e:
                print(f"Version validation failed: {e}", file=sys.stderr)
                sys.exit(1)

        elif args.command == "summary":
            summary = generate_release_summary(
                matrix_json=args.matrix,
                version=args.version,
                event_type=args.event_type,
                dry_run=args.dry_run,
                repository_owner=args.repository_owner
            )
            print(summary)

    except Exception as e:
        print(f"Error: {e}", file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()