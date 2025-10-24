"""Bazel rule for generating OpenAPI Go clients.

This generates Go client libraries from OpenAPI specifications using openapi-generator-cli.
Generated code is placed in the workspace at generated/go/{namespace}/{app}/.

Example:
    load("//tools/openapi:openapi_go_client.bzl", "openapi_go_client")
    
    openapi_go_client(
        name = "demo_api_client",
        spec = "//demo/hello_fastapi:hello-fastapi_openapi_spec",
        namespace = "demo",
        app = "hello_fastapi",
        importpath = "github.com/whale-net/everything/generated/go/demo/hello_fastapi",
    )
"""

load("@rules_go//go:def.bzl", "go_library")

def openapi_go_client(name, spec, namespace, app, importpath, package_name = None, visibility = None):
    """Generate OpenAPI Go client with proper Go module structure.
    
    Should be defined in //generated/go/{namespace}/ to ensure generated code appears at
    the correct path in the workspace.
    
    Args:
        name: Target name
        spec: OpenAPI spec file (label)
        namespace: Namespace (e.g., "demo", "manman")
        app: App name (e.g., "hello_fastapi")
        importpath: Go import path (e.g., "github.com/whale-net/everything/generated/go/demo/hello_fastapi")
        package_name: Optional Go package name (defaults to app with underscores)
        visibility: Target visibility
    """
    if not package_name:
        package_name = app.replace("-", "_")
    
    # Step 1: Generate Go client code using genrule (exec configuration)
    # This ensures Java runs on the execution platform, not the target platform
    tar_name = name + "_tar_gen"
    native.genrule(
        name = tar_name,
        srcs = [spec],
        outs = ["{}.tar".format(app)],
        tools = [
            "//tools/openapi:openapi_gen_go_wrapper",
            "@openapi_generator_cli//file",
        ],
        toolchains = ["@bazel_tools//tools/jdk:current_java_runtime"],
        cmd = """
            # Use wrapper with "auto" to find system Java, fallback to Bazel Java
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
        visibility = ["//visibility:private"],
        tags = ["openapi", "go"],
        # Force deterministic caching - client generation is deterministic for given spec
        stamp = 0,
    )
    
    # Step 2: Extract tar and list all .go files to create source files
    # OpenAPI generator creates multiple Go files, we need to extract them all
    extract_name = name + "_extract"
    native.genrule(
        name = extract_name,
        srcs = [":" + tar_name],
        outs = [
            "{}/client.go".format(app),
            "{}/configuration.go".format(app),
            "{}/response.go".format(app),
            "{}/utils.go".format(app),
            "{}/api_default.go".format(app),
            "{}/go.mod".format(app),
            "{}/go.sum".format(app),
        ],
        cmd = """
            mkdir -p $(RULEDIR)/{app}
            tar -xf $(location :{tar_name}) -C $(RULEDIR)/{app} --strip-components=0
            # Move root-level files to app directory
            if [ -f $(RULEDIR)/{app}/client.go ]; then
                # Files are already in the right place
                true
            fi
        """.format(
            app = app,
            tar_name = tar_name,
        ),
        visibility = ["//visibility:private"],
    )
    
    # Step 3: Create go_library target that other Go code can depend on
    # Only include .go source files, not go.mod/go.sum
    go_library(
        name = name,
        srcs = [
            "{}/client.go".format(app),
            "{}/configuration.go".format(app),
            "{}/response.go".format(app),
            "{}/utils.go".format(app),
            "{}/api_default.go".format(app),
        ],
        importpath = importpath,
        visibility = visibility or ["//visibility:public"],
        deps = [],  # OpenAPI generated Go code typically has no external deps for basic clients
    )
