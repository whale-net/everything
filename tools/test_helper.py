#!/usr/bin/env python3
"""
Testing helper script for the Everything monorepo.

This script provides intelligent testing strategies that integrate with the release helper,
enabling context-aware testing for different scenarios (PR, main branch, release).
"""

import json
import subprocess
import sys
import argparse
import os
from typing import List, Optional
from enum import Enum

# Import shared utilities
from shared_utils import (
    find_workspace_root, run_bazel, list_all_apps, get_app_metadata,
    detect_changed_files, detect_changed_apps, get_previous_commit,
    get_base_commit_for_pr
)


class TestContext(Enum):
    """Different testing contexts."""
    PR = "pr"
    MAIN = "main"
    RELEASE = "release"
    LOCAL = "local"


def run_release_helper(args: List[str]) -> subprocess.CompletedProcess:
    """Run the release helper script."""
    workspace_root = find_workspace_root()
    cmd = ["python3", "tools/release_helper.py"] + args
    try:
        return subprocess.run(
            cmd,
            capture_output=True,
            text=True,
            check=True,
            cwd=workspace_root
        )
    except subprocess.CalledProcessError as e:
        print(f"Release helper command failed: {' '.join(cmd)}")
        if e.stderr:
            print(f"stderr: {e.stderr}")
        if e.stdout:
            print(f"stdout: {e.stdout}")
        raise


def get_all_apps() -> List[str]:
    """Get all apps from the release helper."""
    return list_all_apps()


def determine_test_context() -> TestContext:
    """Determine the current testing context."""
    # Check environment variables for CI context
    if os.environ.get("GITHUB_EVENT_NAME") == "pull_request":
        return TestContext.PR
    elif os.environ.get("GITHUB_REF") == "refs/heads/main":
        return TestContext.MAIN
    elif os.environ.get("GITHUB_EVENT_NAME") == "release":
        return TestContext.RELEASE

    # Check for release-related environment variables
    if os.environ.get("RELEASE_APP"):
        return TestContext.RELEASE

    # Default to local development
    return TestContext.LOCAL


def get_apps_to_test(context: TestContext, release_app: Optional[str] = None) -> List[str]:
    """Determine which apps to test based on context."""
    all_apps = get_all_apps()

    if context == TestContext.RELEASE:
        if release_app:
            return [release_app]
        else:
            # If no specific app, test all (shouldn't happen in release)
            print("⚠️  Release context but no specific app specified, testing all")
            return all_apps

    elif context == TestContext.PR:
        base_commit = get_base_commit_for_pr()
        if base_commit:
            print(f"🔍 PR context: Testing changes since {base_commit[:8]}")
            changed_files = detect_changed_files(base_commit)
            changed_apps = detect_changed_apps(changed_files)
            if changed_apps:
                print(f"📦 Testing changed apps: {', '.join(changed_apps)}")
                return changed_apps
            else:
                print("📦 No app changes detected, running minimal test suite")
                return []
        else:
            print("⚠️  Could not determine PR base, testing all apps")
            return all_apps

    elif context == TestContext.MAIN:
        prev_commit = get_previous_commit()
        if prev_commit:
            print(f"🔍 Main branch context: Testing changes since {prev_commit[:8]}")
            changed_files = detect_changed_files(prev_commit)
            changed_apps = detect_changed_apps(changed_files)
            if changed_apps:
                print(f"📦 Testing changed apps: {', '.join(changed_apps)}")
                return changed_apps
            else:
                print("📦 No app changes detected, running minimal test suite")
                return []
        else:
            print("⚠️  Could not determine previous commit, testing all apps")
            return all_apps

    else:  # LOCAL context
        print("🏠 Local development context")
        # In local development, we could be more sophisticated
        # For now, test all apps
        return all_apps


def get_test_targets_for_apps(apps: List[str]) -> List[str]:
    """Get Bazel test targets for the specified apps."""
    targets = []

    # Always include shared/infrastructure tests
    targets.extend([
        "//libs/...",
        "//tools:test_helper_test",  # Test the testing helper itself
    ])

    # Add app-specific test targets
    for app in apps:
        targets.append(f"//{app}/...")

    return targets


def run_tests(targets: List[str], config: str = "ci") -> None:
    """Run the specified test targets."""
    if not targets:
        print("✅ No tests to run")
        return

    print(f"🧪 Running tests for: {', '.join(targets)}")

    # Build test targets first
    build_cmd = ["build", f"--config={config}"] + targets
    try:
        run_bazel(build_cmd)
        print("✅ Build completed successfully")
    except subprocess.CalledProcessError:
        print("❌ Build failed")
        raise

    # Run tests
    test_cmd = ["test", f"--config={config}"] + targets
    try:
        run_bazel(test_cmd)
        print("✅ Tests completed successfully")
    except subprocess.CalledProcessError:
        print("❌ Tests failed")
        raise


def generate_test_summary(apps: List[str], context: TestContext, success: bool) -> str:
    """Generate a test summary."""
    summary = []
    summary.append("## 🧪 Test Summary")
    summary.append("")

    if success:
        summary.append("✅ **Result:** All tests passed")
    else:
        summary.append("❌ **Result:** Some tests failed")

    summary.append("")
    summary.append(f"📦 **Apps Tested:** {', '.join(apps) if apps else 'None'}")
    summary.append(f"🎯 **Context:** {context.value.title()}")

    if context == TestContext.PR:
        base_commit = get_base_commit_for_pr()
        if base_commit:
            summary.append(f"🔀 **Base Commit:** {base_commit[:8]}")
    elif context == TestContext.MAIN:
        prev_commit = get_previous_commit()
        if prev_commit:
            summary.append(f"📈 **Previous Commit:** {prev_commit[:8]}")

    summary.append("")
    summary.append("### 🛠️ Test Commands")
    summary.append("```bash")
    summary.append("# Run tests locally")
    summary.append("bazel run //tools:test -- plan")
    summary.append("")
    summary.append("# Run specific app tests")
    if apps:
        for app in apps[:2]:  # Show first 2 apps as examples
            summary.append(f"bazel test //{app}/...")
    summary.append("```")

    return "\n".join(summary)


def main():
    parser = argparse.ArgumentParser(description="Testing helper for Everything monorepo")
    subparsers = parser.add_subparsers(dest="command", help="Available commands")

    # Plan tests command
    plan_parser = subparsers.add_parser("plan", help="Plan which tests to run based on context")
    plan_parser.add_argument("--context", choices=["pr", "main", "release", "local"], help="Override test context")
    plan_parser.add_argument("--release-app", help="Specific app for release testing")
    plan_parser.add_argument("--format", choices=["json", "github"], default="json", help="Output format")

    # Run tests command
    run_parser = subparsers.add_parser("run", help="Run tests based on context")
    run_parser.add_argument("--context", choices=["pr", "main", "release", "local"], help="Override test context")
    run_parser.add_argument("--release-app", help="Specific app for release testing")
    run_parser.add_argument("--config", default="ci", help="Bazel config to use")

    # List apps command
    list_parser = subparsers.add_parser("list", help="List all testable apps")

    # Detect changes command
    changes_parser = subparsers.add_parser("changes", help="Detect changed apps since commit")
    changes_parser.add_argument("--since", help="Commit to compare against (default: auto-detect)")
    changes_parser.add_argument("--format", choices=["json", "github"], default="json", help="Output format")

    # Summary command
    summary_parser = subparsers.add_parser("summary", help="Generate test summary")
    summary_parser.add_argument("--apps", required=True, help="JSON list of apps tested")
    summary_parser.add_argument("--context", required=True, choices=["pr", "main", "release", "local"], help="Test context")
    summary_parser.add_argument("--success", action="store_true", help="Whether tests succeeded")

    args = parser.parse_args()

    if not args.command:
        parser.print_help()
        return

    try:
        if args.command == "plan":
            # Determine context
            context_override = TestContext(args.context) if args.context else None
            context = context_override or determine_test_context()

            # Get apps to test
            apps = get_apps_to_test(context, args.release_app)

            plan = {
                "context": context.value,
                "apps": apps,
                "test_targets": get_test_targets_for_apps(apps),
                "release_app": args.release_app
            }

            if args.format == "github":
                # GitHub Actions format
                print(f"context={context.value}")
                if apps:
                    print(f"apps={' '.join(apps)}")
                else:
                    print("apps=")
                print(f"test_targets={' '.join(plan['test_targets'])}")
            else:
                print(json.dumps(plan, indent=2))

        elif args.command == "run":
            # Determine context
            context_override = TestContext(args.context) if args.context else None
            context = context_override or determine_test_context()

            # Get apps and run tests
            apps = get_apps_to_test(context, args.release_app)
            targets = get_test_targets_for_apps(apps)

            try:
                run_tests(targets, args.config)
                success = True
            except subprocess.CalledProcessError:
                success = False
                sys.exit(1)

        elif args.command == "list":
            apps = get_all_apps()
            for app in apps:
                print(app)

        elif args.command == "changes":
            since_commit = args.since
            if not since_commit:
                context = determine_test_context()
                if context == TestContext.PR:
                    since_commit = get_base_commit_for_pr()
                elif context == TestContext.MAIN:
                    since_commit = get_previous_commit()

            if since_commit:
                changed_files = detect_changed_files(since_commit)
                changed_apps = detect_changed_apps(changed_files)

                result = {
                    "since_commit": since_commit,
                    "changed_files": changed_files,
                    "changed_apps": changed_apps
                }

                if args.format == "github":
                    print(f"changed_apps={' '.join(changed_apps)}")
                    print(f"changed_files_count={len(changed_files)}")
                else:
                    print(json.dumps(result, indent=2))
            else:
                print("Could not determine commit to compare against")

        elif args.command == "summary":
            context = TestContext(args.context)
            apps = json.loads(args.apps) if args.apps != "[]" else []
            summary = generate_test_summary(apps, context, args.success)
            print(summary)

    except Exception as e:
        print(f"Error: {e}", file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()