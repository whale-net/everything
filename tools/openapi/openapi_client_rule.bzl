"""Bazel rule implementation for OpenAPI client generation with automatic model discovery."""

def _openapi_client_impl(ctx):
    """Generate OpenAPI client with automatic model discovery."""
    spec = ctx.file.spec
    package_name = ctx.attr.package_name
    namespace = ctx.attr.namespace
    app = ctx.attr.app
    
    # Output directory - just the app name since we're defining in //external/{namespace}
    # Files will be at: bazel-bin/external/{namespace}/{app}
    # Which becomes: _main/external/{namespace}/{app} in runfiles
    # Allowing imports: from external.{namespace}.{app} import ...
    output_dir = app
    
    # Step 1: Generate tar
    tar_file = ctx.actions.declare_file("{}.tar".format(app))
    
    # Get Java runtime from toolchain
    java_toolchain_info = ctx.toolchains["@bazel_tools//tools/jdk:runtime_toolchain_type"].java_runtime
    java_executable = java_toolchain_info.java_home + "/bin/java"
    
    # Use wrapper script
    ctx.actions.run(
        executable = ctx.executable._wrapper_script,
        arguments = [
            java_executable,
            ctx.file._openapi_generator.path,
            spec.path,
            tar_file.path,
            package_name,
            namespace,
            app,
        ],
        inputs = depset([spec, ctx.file._openapi_generator] + java_toolchain_info.files.to_list()),
        outputs = [tar_file],
        mnemonic = "OpenAPIGenerate",
        progress_message = "Generating OpenAPI client for {}".format(app),
    )
    
    # Step 2: Extract tar and discover actual files
    # Declare a directory output - this is the key!
    output_tree = ctx.actions.declare_directory(output_dir)
    
    ctx.actions.run_shell(
        inputs = [tar_file],
        outputs = [output_tree],
        command = """
            set -e
            mkdir -p "$1"
            tar -xf "$2" -C "$1" --strip-components=1
            
            # Verify extraction
            if [ ! -f "$1/__init__.py" ]; then
                echo "Error: Extraction failed"
                ls -la "$1"
                exit 1
            fi
        """,
        arguments = [output_tree.path, tar_file.path],
    )
    
    # Since we're in //external/manman/, output_dir is just the app name
    # The tree will be at bazel-bin/external/manman/{app}
    # In runfiles it needs to be at _main/external/manman/{app}
    # But root_symlinks are relative to _main/, so we need external/manman/{app} -> the tree
    runfiles_path = "external/{}/{}".format(namespace, app)
    
    return [
        DefaultInfo(
            files = depset([output_tree]),
            runfiles = ctx.runfiles(
                files = [output_tree],
                root_symlinks = {runfiles_path: output_tree},
            ),
        ),
        PyInfo(
            transitive_sources = depset([output_tree]),
            imports = depset(direct = ["."]),
        ),
    ]

openapi_client_rule = rule(
    implementation = _openapi_client_impl,
    attrs = {
        "spec": attr.label(allow_single_file = [".json"], mandatory = True),
        "namespace": attr.string(mandatory = True),
        "app": attr.string(mandatory = True),
        "package_name": attr.string(mandatory = True),
        "_openapi_generator": attr.label(
            default = "@openapi_generator_cli//file",
            allow_single_file = True,
            cfg = "exec",
        ),
        "_wrapper_script": attr.label(
            default = "//tools:openapi_gen_wrapper",
            executable = True,
            cfg = "exec",
        ),
    },
    toolchains = ["@bazel_tools//tools/jdk:runtime_toolchain_type"],
    provides = [DefaultInfo, PyInfo],
)

def openapi_client(name, spec, namespace, app, package_name = None, visibility = None):
    """Generate OpenAPI client with automatic model discovery.
    
    Should be defined in //external/{namespace}/ to ensure generated code appears at
    the correct import path: from external.{namespace}.{app} import ...
    
    Args:
        name: Target name
        spec: OpenAPI spec file
        namespace: Namespace (e.g., "manman")
        app: App name (e.g., "experience_api")
        package_name: Optional package name for the generated package
        visibility: Target visibility
    """
    if not package_name:
        package_name = "{}-{}".format(namespace, app.replace("_", "-"))
    
    # Generate the client code
    gen_name = name + "_gen"
    
    openapi_client_rule(
        name = gen_name,
        spec = spec,
        namespace = namespace,
        app = app,
        package_name = package_name,
        visibility = ["//visibility:private"],
    )
    
    # Wrap in py_library to add runtime deps
    native.py_library(
        name = name,
        data = [":" + gen_name],
        deps = [
            "@pypi//:pydantic",
            "@pypi//:python-dateutil",
            "@pypi//:urllib3",
        ],
        visibility = visibility or ["//visibility:public"],
    )
