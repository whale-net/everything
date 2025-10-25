"""Macro for generating OpenAPI Go clients fully integrated in Bazel build graph.

This provides a simple interface for creating Go clients from OpenAPI specs.
All code generation happens automatically within Bazel - no manual steps required.
Generated files are never committed, only built on-demand.

Example:
    load("//tools/openapi:openapi_go_client.bzl", "openapi_go_client")
    
    openapi_go_client(
        name = "my_api",
        spec = "//path/to:api_spec",
        namespace = "my_namespace",
        app = "my-api",
        importpath = "github.com/whale-net/everything/generated/go/my_namespace/my_api",
    )
"""

load("@rules_go//go:def.bzl", "go_library")
load("//tools/openapi:go_client.bzl", "go_openapi_sources")

def openapi_go_client(name, spec, namespace, app, importpath, package_name = None, visibility = None):
    """Generate OpenAPI Go client as part of Bazel build graph.
    
    Creates targets:
    1. {name}_srcs - Custom rule that generates and extracts .go files
    2. {name} - go_library that uses the generated files
    
    The generation happens automatically when the go_library is built.
    Files are generated into bazel-bin and never committed to the repo.
    
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
    
    src_target = name + "_srcs"
    
    # Target 1: Generate Go client files using custom rule
    go_openapi_sources(
        name = src_target,
        spec = spec,
        package_name = package_name,
        importpath = importpath,
        tags = ["openapi", "go"],
        visibility = ["//visibility:private"],
    )
    
    # Target 2: go_library using generated files
    go_library(
        name = name,
        srcs = [":{}".format(src_target)],
        importpath = importpath,
        visibility = visibility or ["//visibility:public"],
    )
