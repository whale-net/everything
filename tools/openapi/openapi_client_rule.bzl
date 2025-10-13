"""Bazel rule implementation for OpenAPI client generation with automatic model discovery."""

def _openapi_client_impl(ctx):
    """Generate OpenAPI client with automatic model discovery."""
    spec = ctx.file.spec
    package_name = ctx.attr.package_name
    namespace = ctx.attr.namespace
    app = ctx.attr.app
    
    # Output directory structure must match import path
    # For imports: from generated.{namespace}.{app} import ...
    # This rule should be called from //generated/{namespace}/BUILD.bazel
    # Output to just {app}, the package path provides the namespace part
    # Result: bazel-bin/generated/{namespace}/{app}
    #         runfiles/_main/generated/{namespace}/{app}
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
    
    # Step 2: Extract tar to proper directory structure
    output_tree = ctx.actions.declare_directory(output_dir)
    
    ctx.actions.run_shell(
        inputs = [tar_file],
        outputs = [output_tree],
        command = """
            set -e
            # Extract the generated client code
            mkdir -p "$1"
            tar -xf "$2" -C "$1" --strip-components=1
            
            # Verify extraction
            if [ ! -f "$1/__init__.py" ]; then
                echo "Error: Client extraction failed"
                ls -la "$1"
                exit 1
            fi
        """,
        arguments = [output_tree.path, tar_file.path],
    )
    
    # Now files are at bazel-bin/generated/{namespace}/generated/{namespace}/{app}
    # In runfiles: bazel-bin/generated/manman -> runfiles/manman/
    # So files end up at: runfiles/manman/generated/{namespace}/{app}
    # To import "from generated.{namespace}.{app}", Python needs runfiles/manman/ in sys.path
    # We add the repository name (manman) as an import path
    #
    # For the package structure to work, we also need:
    # - generated/__init__.py  
    # - generated/{namespace}/__init__.py
    # These are created as separate genrule targets in the BUILD file
    
    # Collect init files from _package_inits attribute
    init_files = []
    for target in ctx.attr._package_inits:
        init_files.extend(target.files.to_list())
    
    # Collect transitive dependencies
    deps_transitive_sources = []
    deps_imports = []
    for dep in ctx.attr.deps:
        if PyInfo in dep:
            deps_transitive_sources.append(dep[PyInfo].transitive_sources)
            deps_imports.append(dep[PyInfo].imports)
    
    return [
        DefaultInfo(
            files = depset([output_tree] + init_files),
            runfiles = ctx.runfiles(files = [output_tree] + init_files),
        ),
        PyInfo(
            transitive_sources = depset(
                direct = [output_tree] + init_files,
                transitive = deps_transitive_sources,
            ),
            imports = depset(
                direct = ["generated"],  # Add "generated" to Python path so imports work
                transitive = deps_imports,
            ),
        ),
    ]

openapi_client_rule = rule(
    implementation = _openapi_client_impl,
    attrs = {
        "spec": attr.label(allow_single_file = [".json"], mandatory = True),
        "namespace": attr.string(mandatory = True),
        "app": attr.string(mandatory = True),
        "package_name": attr.string(mandatory = True),
        "deps": attr.label_list(providers = [PyInfo]),  # Runtime deps like pydantic
        "_package_inits": attr.label_list(
            default = [
                "//generated:init",
                "//generated/manman:namespace_init",
            ],
        ),
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
    
    Should be defined in //generated/{namespace}/ to ensure generated code appears at
    the correct import path: from generated.{namespace}.{app} import ...
    
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
    
    # Generate the client code with runtime deps included
    # This creates a TreeArtifact which works locally with MANIFEST
    openapi_client_rule(
        name = name,
        spec = spec,
        namespace = namespace,
        app = app,
        package_name = package_name,
        deps = [
            "@pypi//:pydantic",
            "@pypi//:python-dateutil",
            "@pypi//:urllib3",
        ],
        visibility = visibility or ["//visibility:public"],
    )
    
    # For containers: Create a tar file that can be properly included
    # Container build will extract this at runtime if needed
    native.genrule(
        name = name + "_tar",
        srcs = [":" + name],
        outs = ["{}_container.tar".format(app)],
        cmd = "tar -cf $@ -C $(location :{})/../ {}".format(name, app),
        visibility = ["//visibility:public"],
        tags = ["manual"],
    )
