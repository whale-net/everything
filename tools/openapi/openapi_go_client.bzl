"""Macro for generating OpenAPI Go clients with workspace sync pattern.

This provides a simple interface for creating Go clients from OpenAPI specs.
Generated code is synced to workspace via ./tools/scripts/sync_go_clients.sh

Example:
    load("//tools/openapi:openapi_go_client.bzl", "openapi_go_client")
    
    openapi_go_client(
        name = "my_api",
        spec = "//path/to:api_spec",
        namespace = "my_namespace",
        app = "my-app",
        importpath = "github.com/whale-net/everything/generated/go/my_namespace/my_api",
    )
"""

load("@rules_go//go:def.bzl", "go_library")

def openapi_go_client(name, spec, namespace, app, importpath, package_name = None, visibility = None):
    """Generate OpenAPI Go client that can be synced to workspace.
    
    Creates two targets:
    1. {name}_tar - Genrule that generates client code as a tar
    2. {name} - go_library that references synced files in workspace
    
    Workflow:
        1. Define openapi_go_client in BUILD.bazel
        2. Run: ./tools/scripts/sync_go_clients.sh
        3. Use the client in your code with normal imports
    
    Args:
        name: Target name (also used as go_library name)
        spec: Label to OpenAPI spec file
        namespace: Namespace (e.g., "manman", "demo")
        app: App name (e.g., "experience-api", "hello_fastapi")
        importpath: Go import path for the generated library
        package_name: Go package name (defaults to app with hyphens->underscores)
        visibility: Target visibility (defaults to public)
    """
    if not package_name:
        package_name = app.replace("-", "_")
    
    # Target 1: Generate tar file with OpenAPI generator
    native.genrule(
        name = name + "_tar",
        srcs = [spec],
        outs = ["{}.tar".format(app)],
        tools = [
            "//tools/openapi:openapi_gen_go_wrapper",
            "@openapi_generator_cli//file",
        ],
        toolchains = ["@bazel_tools//tools/jdk:current_java_runtime"],
        cmd = """
            $(location //tools/openapi:openapi_gen_go_wrapper) \\
                auto \\
                $(JAVA) \\
                $(location @openapi_generator_cli//file) \\
                $(location {spec}) \\
                $@ \\
                {package_name} \\
                {importpath}
        """.format(
            spec = spec,
            package_name = package_name,
            importpath = importpath,
        ),
        tags = ["openapi", "go", "manual"],
        visibility = ["//visibility:private"],
    )
    
    # Target 2: go_library referencing workspace-synced files
    # Files are synced via: ./tools/scripts/sync_go_clients.sh
    go_library(
        name = name,
        srcs = native.glob(
            ["{}/*.go".format(package_name)],
            exclude = ["{}/*_test.go".format(package_name)],
        ),
        importpath = importpath,
        visibility = visibility or ["//visibility:public"],
    )
