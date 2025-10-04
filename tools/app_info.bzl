"""Application information provider for extracting metadata from binaries."""

AppInfo = provider(
    doc = """Provider that exposes application metadata from binary targets.
    
    This allows release_app to extract information from the binary definition
    without requiring duplication in the release configuration. Information here
    is intrinsic to the application code (what port it listens on, what args it needs)
    rather than deployment configuration (how many replicas, what ingress host).
    """,
    fields = {
        "args": "List of command-line arguments to pass to the binary",
        "binary_name": "Name of the binary target",
        "port": "Port the application listens on (0 if no HTTP server)",
        "app_type": "Application type: external-api, internal-api, worker, or job",
    },
)

def _app_info_impl(ctx):
    """Create an AppInfo provider with application metadata."""
    return [
        AppInfo(
            args = ctx.attr.args,
            binary_name = ctx.attr.binary_name,
            port = ctx.attr.port,
            app_type = ctx.attr.app_type,
        ),
    ]

app_info = rule(
    implementation = _app_info_impl,
    attrs = {
        "args": attr.string_list(default = []),
        "binary_name": attr.string(mandatory = True),
        "port": attr.int(default = 0),
        "app_type": attr.string(default = ""),
    },
    doc = """Rule that exposes application metadata through AppInfo provider.
    
    This is automatically created by multiplatform_py_binary and multiplatform_go_binary
    to expose args and other metadata to the release system.
    """,
)
