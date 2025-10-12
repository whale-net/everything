"""Bazel rule implementation for OpenAPI client generation with automatic model discovery."""

def _symlink_tree_impl(ctx):
    """Create a symlink tree that remaps generated code to external/{namespace}/{app}."""
    generated = ctx.attr.generated
    namespace = ctx.attr.namespace
    app = ctx.attr.app
    
    # The generated code DefaultInfo includes the directory tree
    files = generated[DefaultInfo].files.to_list()
    if not files:
        fail("No files generated")
    
    output_tree = files[0]  # The directory tree
    
    # Add the parent directory of the generated code to Python imports path
    # This makes "external.{namespace}.{app}" importable even though files are at
    # "{package}/external/{namespace}/{app}"
    return [
        DefaultInfo(
            files = depset([output_tree]),
            runfiles = ctx.runfiles(files = [output_tree]),
        ),
        PyInfo(
            transitive_sources = depset([output_tree]),
            # Add the directory containing "external" to the import path
            # This allows "from external.{namespace}.{app} import ..." to work
            imports = depset(direct = ["."]),
        ),
    ]

_symlink_tree = rule(
    implementation = _symlink_tree_impl,
    attrs = {
        "generated": attr.label(mandatory = True),
        "namespace": attr.string(mandatory = True),
        "app": attr.string(mandatory = True),
    },
    provides = [DefaultInfo, PyInfo],
)

def _openapi_client_impl(ctx):
    """Generate OpenAPI client with automatic model discovery."""
    spec = ctx.file.spec
    package_name = ctx.attr.package_name
    namespace = ctx.attr.namespace
    app = ctx.attr.app
    
    # Output directory structure
    output_dir = "external/{}/{}".format(namespace, app)
    
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
            
            # Fix imports in generated code to match actual file location
            # Replace "from external.{namespace}." with "from manman.external.{namespace}."
            # Also replace "import external.{namespace}." with "import manman.external.{namespace}."
            find "$1" -name "*.py" -type f -exec sed -i \
                -e 's|from external\\.{namespace}\\.|from manman.external.{namespace}.|g' \
                -e 's|import external\\.{namespace}|import manman.external.{namespace}|g' \
                {{}} \\;
        """.format(namespace=namespace),
        arguments = [output_tree.path, tar_file.path],
    )
    
    return [
        DefaultInfo(
            files = depset([output_tree]),
            runfiles = ctx.runfiles(root_symlinks = {
                output_dir: output_tree,
            }),
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
    
    This creates proper symlinks so the generated code appears at external/{namespace}/{app}
    in the runfiles tree, matching the import path: from external.{namespace}.{app} import ...
    
    Args:
        name: Target name
        spec: OpenAPI spec file
        namespace: Namespace (e.g., "manman")
        app: App name (e.g., "experience_api")
        package_name: Optional package name
        visibility: Target visibility
    """
    if not package_name:
        package_name = "{}-{}".format(namespace, app.replace("_", "-"))
    
    # Generate the client code
    gen_name = name + "_generated"
    
    openapi_client_rule(
        name = gen_name,
        spec = spec,
        namespace = namespace,
        app = app,
        package_name = package_name,
        visibility = ["//visibility:private"],
    )
    
    # Create symlink tree that remaps to external/{namespace}/{app}
    symlink_name = name + "_symlinked"
    
    _symlink_tree(
        name = symlink_name,
        generated = ":" + gen_name,
        namespace = namespace,
        app = app,
        visibility = ["//visibility:private"],
    )
    
    # Wrap in py_library to add runtime deps
    native.py_library(
        name = name,
        deps = [
            ":" + symlink_name,
            "@pypi//:pydantic",
            "@pypi//:python-dateutil",
            "@pypi//:urllib3",
        ],
        visibility = visibility or ["//visibility:public"],
    )
