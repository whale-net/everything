"""Simplified test utilities for the Everything monorepo.

This module provides a single test suite that leverages Bazel's native
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

def test_suite(name = "test"):
    """Creates a single comprehensive test suite that runs whatever has changed.
    
    This suite leverages Bazel's native incremental builds and test caching.
    Bazel will automatically skip tests that haven't changed and reuse cached results.
    This is the only test suite needed - it handles all testing scenarios efficiently.
    
    Args:
        name: Name of the test suite target
    """
    commands = [
        "echo 'Running comprehensive test suite (Bazel incremental builds)...'",
        "cd $BUILD_WORKSPACE_DIRECTORY",
        "",
        "echo '=== Running all tests (Bazel will cache unchanged tests) ==='",
        "bazel test --config=ci //...",
        "",
        "echo '=== Building all targets (Bazel will cache unchanged builds) ==='",
        "bazel build --config=ci //...",
        "",
        "echo '=== Testing app execution ==='",
        "bazel run //hello_python:hello_python",
        "bazel run //hello_go:hello_go",
        "",
        "echo '=== Building container images (Bazel incremental) ==='",
        "bazel build --config=ci $(bazel query 'kind(\"oci_load\", //...)')",
        "",
        "echo '=== Test suite completed! ==='",
    ]
    
    test_runner(
        name = name,
        commands = commands,
    )
