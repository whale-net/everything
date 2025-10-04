"""Multiplatform Python binary wrapper with platform transitions.

Creates platform-specific py_binary targets with proper cross-compilation support.
Uses Bazel platform transitions to ensure each binary variant is built for its target
architecture, allowing pycross to select the correct compiled wheels.

Usage - exactly like py_binary:
    multiplatform_py_binary(
        name = "my_app",
        srcs = ["main.py"],
        deps = [":app_lib", "@pypi//:fastapi", "@pypi//:uvicorn"],
    )
    
    release_app(
        name = "my_app",
        language = "python",
        domain = "api",
    )

This creates two user-facing binaries (plus internal base targets):
  - my_app_linux_amd64 (built with --platforms=//tools:linux_x86_64)
  - my_app_linux_arm64 (built with --platforms=//tools:linux_arm64)
  
Internally, this also creates base py_binary targets (_base_amd64, _base_arm64) that
are wrapped by rules with platform transitions applied. The transitions ensure pycross 
selects the correct wheels for each architecture, enabling true cross-compilation."""

load("@rules_python//python:defs.bzl", "py_binary")

def _platform_transition_impl(settings, attr):
    """Transition to the target platform for the binary variant.
    
    This transition changes the --platforms flag to match the target architecture,
    which causes pycross to select the correct wheels for that platform. This is
    critical for apps with compiled dependencies (pydantic, numpy, etc.) to ensure
    ARM64 containers get aarch64 wheels instead of x86_64 wheels.
    
    See docs/CROSS_COMPILATION.md for detailed explanation.
    """
    return {"//command_line_option:platforms": str(attr.target_platform)}

_platform_transition = transition(
    implementation = _platform_transition_impl,
    inputs = [],
    outputs = ["//command_line_option:platforms"],
)

def _multiplatform_py_binary_impl(ctx):
    """Create a wrapper that forwards to the underlying py_binary with platform transition applied."""
    # Get the executable from the binary attribute (which has the transition applied)
    binary_default_info = ctx.attr.binary[0][DefaultInfo]
    
    # Create a symlink to the actual executable
    output_executable = ctx.actions.declare_file(ctx.label.name)
    ctx.actions.symlink(
        output = output_executable,
        target_file = binary_default_info.files_to_run.executable,
        is_executable = True,
    )
    
    return [
        DefaultInfo(
            files = depset([output_executable]),
            runfiles = binary_default_info.default_runfiles,
            executable = output_executable,
        ),
    ]

_multiplatform_py_binary_rule = rule(
    implementation = _multiplatform_py_binary_impl,
    attrs = {
        "binary": attr.label(
            cfg = _platform_transition,
            executable = True,
            mandatory = True,
        ),
        "target_platform": attr.label(
            mandatory = True,
        ),
        "_allowlist_function_transition": attr.label(
            default = "@bazel_tools//tools/allowlists/function_transition_allowlist",
        ),
    },
    executable = True,
)

def multiplatform_py_binary(
    name,
    srcs = None,
    main = None,
    deps = None,
    visibility = None,
    **kwargs):
    """Creates platform-suffixed py_binary targets with proper cross-compilation.
    
    Uses Bazel platform transitions to build each variant for its target architecture,
    ensuring pycross selects the correct compiled wheels (pydantic, numpy, etc).
    
    Takes the exact same parameters as py_binary. Creates *_linux_amd64 and 
    *_linux_arm64 targets that get platform-specific compiled dependencies.
    
    Args:
        name: Base name for the binaries (will create {name}_linux_amd64 and {name}_linux_arm64)
        srcs: Source files (same as py_binary)
        main: Main entry point (same as py_binary)
        deps: Dependencies including @pypi// packages (same as py_binary)
        visibility: Visibility (same as py_binary)
        **kwargs: Additional py_binary arguments (env, args, data, etc)
    """
    # Default main to name.py if not provided
    if not main and srcs:
        # Find the main file from srcs
        main_candidates = [src for src in srcs if src.endswith("main.py") or src == name + ".py"]
        if main_candidates:
            main = main_candidates[0]
        elif len(srcs) == 1:
            main = srcs[0]
        else:
            fail("Could not determine main file for {}, please specify main= parameter".format(name))
    
    # Create base py_binary targets that will be transitioned to different platforms
    # Base targets can be used directly on macOS for development, so they need visibility
    # to be accessible from //tools:release alias
    py_binary(
        name = name + "_base_amd64",
        srcs = srcs,
        main = main,
        deps = deps,
        visibility = visibility or ["//tools:__pkg__"],
        **kwargs
    )
    
    py_binary(
        name = name + "_base_arm64",
        srcs = srcs,
        main = main,
        deps = deps,
        visibility = visibility or ["//tools:__pkg__"],
        **kwargs
    )
    
    # AMD64 binary with platform transition to x86_64
    _multiplatform_py_binary_rule(
        name = name + "_linux_amd64",
        binary = ":" + name + "_base_amd64",
        target_platform = "//tools:linux_x86_64",
        visibility = visibility,
    )
    
    # ARM64 binary with platform transition to aarch64
    _multiplatform_py_binary_rule(
        name = name + "_linux_arm64",
        binary = ":" + name + "_base_arm64",
        target_platform = "//tools:linux_arm64",
        visibility = visibility,
    )
