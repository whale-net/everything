"""Rule for generating OpenAPI Go clients with proper Bazel integration.

This rule handles the dynamic file generation problem:
- OpenAPI generator produces a tar with unknown files (depends on spec)
- We extract the tar and discover the .go files at execution time
- We declare outputs dynamically using ctx.actions.declare_directory
- The go_library can then consume these files via a tree artifact
"""

def _go_openapi_sources_impl(ctx):
    """Implementation of go_openapi_sources rule.
    
    This rule:
    1. Generates OpenAPI client code into a tar
    2. Extracts individual .go files from the tar
    3. Returns them as a tree artifact that go_library can use
    """
    spec = ctx.file.spec
    package_name = ctx.attr.package_name
    
    # Step 1: Generate tar using wrapper script
    output_tar = ctx.actions.declare_file("{}.tar".format(ctx.label.name))
    
    # Get Java runtime from toolchain
    java_runtime = ctx.toolchains["@bazel_tools//tools/jdk:runtime_toolchain_type"].java_runtime
    # Use the java home bin/java for execution
    java_home = java_runtime.java_home
    java_bin = "{}/bin/java".format(java_home)
    java_inputs = java_runtime.files
    
    ctx.actions.run(
        inputs = depset([spec, ctx.file._generator], transitive = [java_inputs]),
        outputs = [output_tar],
        executable = ctx.executable._wrapper,
        arguments = [
            "auto",
            java_bin,
            ctx.file._generator.path,
            spec.path,
            output_tar.path,
            package_name,
            ctx.attr.importpath,
        ],
        mnemonic = "OpenAPIGenerate",
        progress_message = "Generating OpenAPI Go client for %s" % ctx.label.name,
    )
    
    # Step 2: Extract tar to get individual .go files
    # The Go files are at the root of the tar, not in a subdirectory
    output_dir = ctx.actions.declare_directory(package_name)
    
    ctx.actions.run_shell(
        inputs = [output_tar],
        outputs = [output_dir],
        command = """
            set -e
            # Extract tar to a temp directory to inspect
            TEMP_DIR=$(mktemp -d)
            tar -xf {tar} -C "$TEMP_DIR"
            
            # List what we got
            echo "Extracted files:" >&2
            ls -la "$TEMP_DIR" >&2
            
            # Copy all Go files (they're at root level, not in subdirectory)
            mkdir -p {output}
            cp "$TEMP_DIR"/*.go {output}/ || {{
                echo "ERROR: No .go files found at tar root" >&2
                exit 1
            }}
            
            # Cleanup
            rm -rf "$TEMP_DIR"
            
            echo "Generated Go files:" >&2
            ls -la {output} >&2
        """.format(
            tar = output_tar.path,
            output = output_dir.path,
        ),
        mnemonic = "OpenAPIExtract",
        progress_message = "Extracting OpenAPI Go files for %s" % ctx.label.name,
    )
    
    # Return the tree artifact
    # go_library should be able to consume this as srcs
    return [
        DefaultInfo(files = depset([output_dir])),
    ]

go_openapi_sources = rule(
    implementation = _go_openapi_sources_impl,
    attrs = {
        "spec": attr.label(
            allow_single_file = True,
            mandatory = True,
            doc = "OpenAPI specification file",
        ),
        "package_name": attr.string(
            mandatory = True,
            doc = "Go package name for generated code",
        ),
        "importpath": attr.string(
            mandatory = True,
            doc = "Go import path for the generated library",
        ),
        "_generator": attr.label(
            default = "@openapi_generator_cli//file",
            allow_single_file = True,
        ),
        "_wrapper": attr.label(
            default = "//tools/openapi:openapi_gen_go_wrapper",
            executable = True,
            cfg = "exec",
        ),
    },
    toolchains = ["@bazel_tools//tools/jdk:runtime_toolchain_type"],
    doc = "Generates OpenAPI Go client source files as a tree artifact",
)
