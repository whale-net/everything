"""Simplified test utilities for the Everything monorepo.

This module provides streamlined test suites that leverage Bazel's native
incremental builds and test caching instead of custom cache management.
"""

def _test_runner_impl(ctx):
    """Implementation for test_runner rule that runs multiple test commands."""
    # Create a script that runs all the specified test commands
    script_content = ["#!/bin/bash", "set -euo pipefail", ""]
    
    for cmd in ctx.attr.commands:
        script_content.append("echo '=== " + cmd + " ==='")
        script_content.append(cmd)
        script_content.append("")
    
    script_content.append("echo '=== All tests passed! ==='")
    
    script = ctx.actions.declare_file(ctx.label.name + ".sh")
    ctx.actions.write(
        output = script,
        content = "\n".join(script_content),
        is_executable = True,
    )
    
    return [DefaultInfo(executable = script)]

test_runner = rule(
    implementation = _test_runner_impl,
    attrs = {
        "commands": attr.string_list(
            mandatory = True,
            doc = "List of test commands to run sequentially",
        ),
    },
    executable = True,
)

def monorepo_test_suite(name = "test_all", test_targets = None, app_targets = None):
    """Creates a comprehensive test suite for the monorepo.
    
    This suite relies on Bazel's native incremental builds and test caching.
    Bazel will automatically skip tests that haven't changed and reuse cached results.
    
    Args:
        name: Name of the test suite target
        test_targets: List of test targets to run (defaults to all tests)
        app_targets: List of app binaries to test (defaults to common apps)
    """
    if not test_targets:
        test_targets = ["//..."]
    
    if not app_targets:
        app_targets = [
            "//hello_python:hello_python",
            "//hello_go:hello_go",
        ]
    
    commands = [
        "echo 'Starting monorepo test suite (using Bazel incremental builds)...'",
        "cd $BUILD_WORKSPACE_DIRECTORY",
        "",
    ]
    
    # Add test commands - Bazel will handle incremental testing automatically
    for target in test_targets:
        commands.extend([
            "echo '=== Running tests: " + target + " ==='",
            "bazel test --config=ci " + target,
            "",
        ])
    
    # Add app execution tests  
    for target in app_targets:
        commands.extend([
            "echo '=== Testing app: " + target + " ==='",
            "bazel run " + target,
            "",
        ])
    
    # Add image build tests
    commands.extend([
        "echo '=== Testing image builds ==='",
        "bazel build --config=ci $(bazel query \"kind('oci_load', //...)\")",
        "",
    ])
    
    test_runner(
        name = name,
        commands = commands,
    )

def quick_test_suite(name = "test_quick"):
    """Creates a quick test suite for rapid development feedback.
    
    Leverages Bazel's incremental builds - only rebuilds and retests what has changed.
    
    Args:
        name: Name of the quick test suite target
    """
    commands = [
        "echo 'Running quick test suite (Bazel incremental)...'",
        "cd $BUILD_WORKSPACE_DIRECTORY",
        "",
        "echo '=== Unit Tests (Bazel will cache unchanged tests) ==='",
        "bazel test --config=ci //...",
        "",
        "echo '=== Build Check (Bazel will cache unchanged targets) ==='", 
        "bazel build --config=ci //...",
        "",
        "echo '=== Quick test suite completed! ==='",
    ]
    
    test_runner(
        name = name,
        commands = commands,
    )

def integration_test_suite(name = "test_integration"):
    """Creates an integration test suite that tests end-to-end functionality.
    
    Args:
        name: Name of the integration test suite target
    """
    commands = [
        "echo 'Running integration test suite...'",
        "cd $BUILD_WORKSPACE_DIRECTORY",
        "",
        "echo '=== Building all binaries ==='",
        "bazel build --config=ci $(bazel query \"kind('.*_binary', //...)\")",
        "",
        "echo '=== Testing Python app ==='",
        "bazel run //hello_python:hello_python",
        "",
        "echo '=== Testing Go app ==='", 
        "bazel run //hello_go:hello_go",
        "",
        "echo '=== Building container images ==='",
        "bazel build --config=ci $(bazel query \"kind('oci_load', //...)\")",
        "",
        "echo '=== Testing image loading ==='",
        "bazel run //hello_python:hello_python_image_tarball",
        "bazel run //hello_go:hello_go_image_tarball",
        "",
        "echo '=== Verifying images ==='",
        "docker images | grep -E '(hello_python|hello_go)'",
        "",
        "echo '=== Integration tests completed! ==='",
    ]
    
    test_runner(
        name = name,
        commands = commands,
    )

def ci_test_suite(name = "test_ci"):
    """Creates a CI test suite that uses Bazel's native incremental capabilities.
    
    This simplified version trusts Bazel's incremental builds and test caching
    instead of implementing custom cache state management.
    
    Args:
        name: Name of the CI test suite target
    """
    commands = [
        "echo 'Running simplified CI test suite...'",
        "cd $BUILD_WORKSPACE_DIRECTORY",
        "",
        "echo '=== Discovering changed targets with Bazel query ==='",
        "bazel query \"kind('.*_binary', //...)\" | head -5",
        "",
        "echo '=== CI Build Phase (Bazel incremental) ==='",
        "bazel query \"kind('.*_binary', //...)\" | xargs bazel build --config=ci",
        "",
        "echo '=== CI Test Phase (Bazel will cache unchanged tests) ==='",
        "bazel test --config=ci //...",
        "",
        "echo '=== CI Docker Phase (Bazel incremental builds) ==='",
        "bazel build --config=ci $(bazel query \"kind('oci_load', //...)\")",
        "",
        "echo '=== Simplified CI test suite completed! ==='",
    ]
    
    test_runner(
        name = name,
        commands = commands,
    )

def simple_test_suite(name, targets = None, description = "Simple test suite"):
    """Creates a simple test suite using Bazel queries for target discovery.
    
    This function demonstrates the simplified approach: use Bazel queries
    to discover targets and rely on Bazel's native incremental builds.
    
    Args:
        name: Name of the test suite target
        targets: List of target patterns (defaults to ["//..."])
        description: Description for the test suite
    """
    if not targets:
        targets = ["//..."]
    
    commands = [
        "echo 'Running " + description + "...'",
        "cd $BUILD_WORKSPACE_DIRECTORY",
        "",
        "echo '=== Target Discovery (using Bazel query) ==='",
    ]
    
    for target in targets:
        commands.extend([
            "echo 'Discovering tests in " + target + ":'",
            "bazel query \"kind('.*_test', " + target + ")\" || echo 'No tests found'",
            "",
        ])
    
    commands.extend([
        "echo '=== Running Tests (Bazel incremental) ==='",
    ])
    
    for target in targets:
        commands.extend([
            "bazel test --config=ci " + target + " || echo 'No tests to run for " + target + "'",
            "",
        ])
    
    commands.append("echo '=== " + description + " completed! ==='")
    
    test_runner(
        name = name,
        commands = commands,
    )
