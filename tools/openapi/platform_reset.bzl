"""Shared transition for pinning platform-independent OpenAPI codegen to one config.

Both `openapi_spec` (openapi.bzl) and `openapi_client` (openapi_client_rule.bzl)
depend on release_app binaries whose transitive closure gets a --platforms split
applied by every consumer's own multiplatform_image build. Since neither macro's
output (a JSON spec, or a generated Python client) actually varies by platform,
compilation mode, or coverage instrumentation, pinning the dependency edge to one
canonical configuration collapses what would otherwise be N cache-incompatible
action instances (one per incoming configuration) back into a single, shared,
cacheable action. See issue #1867 and PR #1868.
"""

def _reset_openapi_codegen_config_impl(settings, attr):
    return {
        "//command_line_option:platforms": ["//tools:linux_x86_64"],
        "//command_line_option:compilation_mode": "fastbuild",
        "//command_line_option:collect_code_coverage": False,
    }

reset_openapi_codegen_config = transition(
    implementation = _reset_openapi_codegen_config_impl,
    inputs = [],
    outputs = [
        "//command_line_option:platforms",
        "//command_line_option:compilation_mode",
        "//command_line_option:collect_code_coverage",
    ],
)

def _pinned_dep_impl(ctx):
    # attr.label with a `cfg` transition always resolves to a list (transitions
    # can in principle split into multiple configs), even though this one is 1:1.
    dep = ctx.attr.dep[0]
    return [
        DefaultInfo(
            files = dep[DefaultInfo].files,
            runfiles = dep[DefaultInfo].default_runfiles,
        ),
        dep[PyInfo],
    ]

pinned_py_dep = rule(
    implementation = _pinned_dep_impl,
    doc = """Forwards a py_library/py_binary's DefaultInfo+PyInfo across a pinned-config edge.

    Native rules (py_binary, genrule) can't attach a per-attribute `cfg`
    transition to their own attrs, so this thin wrapper rule exists purely to
    give `dep` a `cfg = reset_openapi_codegen_config` attribute to depend
    through.
    """,
    attrs = {
        "dep": attr.label(cfg = reset_openapi_codegen_config, providers = [PyInfo], mandatory = True),
    },
)
