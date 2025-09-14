"""Incremental testing utilities for the Everything monorepo."""

load("@rules_python//python:defs.bzl", "py_binary")

def _incremental_test_runner_impl(ctx):
    """Implementation for incremental test runner."""
    script_content = ["#!/bin/bash", "set -euo pipefail", ""]
    
    # Add logic to determine what to test based on cache state and changes
    script_content.extend([
        "# Determine changed domains based on cache hits",
        "echo '=== Analyzing cache state ==='",
        "",
        "# Check which domains have cache misses (indicating changes)",
        "GO_CACHE_HIT=${GO_CACHE_HIT:-false}",
        "PYTHON_CACHE_HIT=${PYTHON_CACHE_HIT:-false}",
        "",
        "if [[ \"$GO_CACHE_HIT\" == \"false\" ]]; then",
        "  echo '🔄 Go domain cache miss - testing Go targets'",
        "  TEST_GO=true",
        "else",
        "  echo '✅ Go domain cache hit - skipping Go tests'",
        "  TEST_GO=false",
        "fi",
        "",
        "if [[ \"$PYTHON_CACHE_HIT\" == \"false\" ]]; then",
        "  echo '🔄 Python domain cache miss - testing Python targets'",
        "  TEST_PYTHON=true",
        "else",
        "  echo '✅ Python domain cache hit - skipping Python tests'",
        "  TEST_PYTHON=false",
        "fi",
        "",
        "cd $BUILD_WORKSPACE_DIRECTORY",
        "",
    ])
    
    # Add test commands based on cache state
    script_content.extend([
        "echo '=== Running incremental tests ==='",
        "",
        "# Always run shared/infrastructure tests",
        "echo 'Testing shared infrastructure...'",
        "bazel test --config=ci //libs/...",
        "",
        "# Test Go domain if cache miss",
        "if [[ \"$TEST_GO\" == \"true\" ]]; then",
        "  echo 'Testing Go domain...'",
        "  bazel test --config=ci --config=go //hello_go/...",
        "fi",
        "",
        "# Test Python domain if cache miss", 
        "if [[ \"$TEST_PYTHON\" == \"true\" ]]; then",
        "  echo 'Testing Python domain...'",
        "  bazel test --config=ci --config=python //hello_python/...",
        "fi",
        "",
        "echo '=== Incremental testing completed ==='",
    ])
    
    script = ctx.actions.declare_file(ctx.label.name + ".sh")
    ctx.actions.write(
        output = script,
        content = "\n".join(script_content),
        is_executable = True,
    )
    
    return [DefaultInfo(executable = script)]

incremental_test_runner = rule(
    implementation = _incremental_test_runner_impl,
    attrs = {},
    executable = True,
)

def incremental_test_suite(name = "test_incremental"):
    """Creates an incremental test suite that tests based on cache state.
    
    Args:
        name: Name of the incremental test suite target
    """
    incremental_test_runner(name = name)