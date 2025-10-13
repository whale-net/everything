"""Bazel rule implementation for OpenAPI client generation with automatic model discovery."""

load("@bazel_skylib//lib:shell.bzl", "shell")

def _openapi_client_provider_impl(ctx):
    """Provide PyInfo for generated OpenAPI client (target configuration)."""
    # Get the generated tar from the genrule
    tar_file = ctx.file.tar
    app = ctx.attr.app
    
    # Extract tar to proper directory structure
    output_tree = ctx.actions.declare_directory(app)
    
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

openapi_client_provider_rule = rule(
    implementation = _openapi_client_provider_impl,
    attrs = {
        "tar": attr.label(allow_single_file = [".tar"], mandatory = True),
        "app": attr.string(mandatory = True),
        "deps": attr.label_list(providers = [PyInfo]),
        "_package_inits": attr.label_list(
            default = [
                "//generated:init",
                "//generated/manman:namespace_init",
            ],
        ),
    },
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
    
    # Step 1: Generate client code using genrule (exec configuration)
    # This ensures Java runs on the execution platform, not the target platform
    tar_name = name + "_tar_gen"
    native.genrule(
        name = tar_name,
        srcs = [spec],
        outs = ["{}.tar".format(app)],
        tools = [
            "//tools:openapi_gen_wrapper",
            "@openapi_generator_cli//file",
        ],
        toolchains = ["@bazel_tools//tools/jdk:current_java_runtime"],
        cmd = """
            # Use wrapper with "auto" to find system Java, fallback to Bazel Java
            $(location //tools:openapi_gen_wrapper) \\
                auto \\
                $(JAVA) \\
                $(location @openapi_generator_cli//file) \\
                $(location {spec}) \\
                $@ \\
                {package_name} \\
                {namespace} \\
                {app}
        """.format(
            spec = spec,
            package_name = package_name.replace("-", "_"),
            namespace = namespace,
            app = app,
        ),
        visibility = ["//visibility:private"],
        tags = ["openapi"],
    )
    
    # Step 2: Create PyInfo provider (target configuration)
    openapi_client_provider_rule(
        name = name,
        tar = ":" + tar_name,
        app = app,
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
