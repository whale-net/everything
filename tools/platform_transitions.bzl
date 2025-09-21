"""Platform transition rules for automatic platform selection in image builds.

This module implements Bazel transitions that automatically configure the target platform
based on the specific image target being built, eliminating the need for manual --platforms flags.
"""

def _linux_amd64_transition_impl(settings, attr):
    """Transition to Linux AMD64 platform."""
    return {"//command_line_option:platforms": ["//tools:linux_x86_64"]}

def _linux_arm64_transition_impl(settings, attr):
    """Transition to Linux ARM64 platform.""" 
    return {"//command_line_option:platforms": ["//tools:linux_arm64"]}

linux_amd64_transition = transition(
    implementation = _linux_amd64_transition_impl,
    inputs = [],
    outputs = ["//command_line_option:platforms"],
)

linux_arm64_transition = transition(
    implementation = _linux_arm64_transition_impl,
    inputs = [],
    outputs = ["//command_line_option:platforms"],
)

def _platform_oci_load_impl(ctx):
    """Implementation of platform_oci_load rule that applies platform transition and loads image."""
    # The oci_load_target was built with the platform transition applied to all dependencies
    # Create a wrapper script that executes the transitioned oci_load target
    
    # Get the actual oci_load script from the transitioned dependency
    oci_load_files = ctx.files.oci_load_target
    if len(oci_load_files) != 1:
        fail("Expected exactly one file from oci_load_target, got: {}".format(oci_load_files))
    
    oci_load_script = oci_load_files[0]
    
    # Create our own executable wrapper
    wrapper_script = ctx.actions.declare_file(ctx.label.name)
    ctx.actions.write(
        output = wrapper_script,
        content = """#!/bin/bash
# Auto-generated platform-specific oci_load wrapper
exec {} "$@"
""".format(oci_load_script.short_path),
        is_executable = True,
    )
    
    # Return both files so the oci_load script is available at runtime
    return [
        DefaultInfo(
            files = depset([wrapper_script, oci_load_script]),
            executable = wrapper_script,
            runfiles = ctx.runfiles(files = [oci_load_script]),
        ),
    ]

_platform_oci_load_amd64 = rule(
    implementation = _platform_oci_load_impl,
    attrs = {
        "oci_load_target": attr.label(
            mandatory = True,
            cfg = linux_amd64_transition,
            executable = True,
            doc = "The oci_load target to build with AMD64 platform transition",
        ),
        "_allowlist_function_transition": attr.label(
            default = "@bazel_tools//tools/allowlists/function_transition_allowlist",
        ),
    },
    executable = True,
    doc = "Rule that applies Linux AMD64 platform transition to oci_load targets",
)

_platform_oci_load_arm64 = rule(
    implementation = _platform_oci_load_impl,
    attrs = {
        "oci_load_target": attr.label(
            mandatory = True,
            cfg = linux_arm64_transition,
            executable = True,
            doc = "The oci_load target to build with ARM64 platform transition",
        ),
        "_allowlist_function_transition": attr.label(
            default = "@bazel_tools//tools/allowlists/function_transition_allowlist",
        ),
    },
    executable = True,
    doc = "Rule that applies Linux ARM64 platform transition to oci_load targets",
)

def platform_oci_load_amd64(name, oci_load_target, **kwargs):
    """Create an oci_load target that automatically uses Linux AMD64 platform."""
    _platform_oci_load_amd64(
        name = name,
        oci_load_target = oci_load_target,
        **kwargs
    )

def platform_oci_load_arm64(name, oci_load_target, **kwargs):
    """Create an oci_load target that automatically uses Linux ARM64 platform."""
    _platform_oci_load_arm64(
        name = name,
        oci_load_target = oci_load_target,
        **kwargs
    )