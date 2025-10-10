"""Bazel rule for generating OpenAPI Python clients in external/ directory."""

load("//tools:requirements.bzl", "requirement")

def openapi_client(name, spec, namespace, app, package_name = None, visibility = None):
    """Generate OpenAPI client library in external/{namespace}/{app}/ directory.
    
    Args:
        name: Target name for the generated py_library
        spec: Label pointing to OpenAPI spec JSON file
        namespace: Namespace for grouping (e.g., "manman", "demo")
        app: Application name (e.g., "experience_api", "hello_fastapi")
        package_name: Optional package name for setup.py (defaults to {namespace}-{app})
        visibility: Visibility for the generated py_library
    """
    
    if not package_name:
        package_name = "{}-{}".format(namespace, app.replace("_", "-"))
    
    output_dir = "external/{}/{}".format(namespace, app)
    gen_name = name + "_gen"
    
    # Generate the client using OpenAPI Generator
    native.genrule(
        name = gen_name,
        srcs = [spec],
        outs = [
            output_dir + "/__init__.py",
            output_dir + "/api/__init__.py",
            output_dir + "/api/default_api.py",
            output_dir + "/models/__init__.py",
            output_dir + "/api_client.py",
            output_dir + "/configuration.py",
            output_dir + "/exceptions.py",
            output_dir + "/rest.py",
        ],
        cmd = """
            set -e
            
            # Create output directory
            mkdir -p $(@D)/{output_dir}
            
            # Create temp directory for full generation
            TMPDIR=$$(mktemp -d)
            trap "rm -rf $$TMPDIR" EXIT
            
            # Run OpenAPI Generator with Java from toolchain
            $(JAVA) -jar $(location @openapi_generator_cli//file) generate \
                -i $(location {spec}) \
                -g python \
                -o $$TMPDIR \
                --package-name {package_name} \
                --additional-properties=packageName={package_name},generateSourceCodeOnly=true,library=urllib3
            
            # Copy only the package files we need (not test, docs, etc)
            if [ -d "$$TMPDIR/{package_name}" ]; then
                cp -r $$TMPDIR/{package_name}/* $(@D)/{output_dir}/
            else
                echo "Error: Generated package directory not found"
                ls -la $$TMPDIR
                exit 1
            fi
            
            # Fix imports to use relative imports instead of absolute package imports
            # Replace 'from {package_name}.' with 'from external.{namespace}.{app}.'
            find $(@D)/{output_dir} -name "*.py" -type f -exec sed -i \
                's|from {package_name}\\.|from external.{namespace}.{app}.|g' {{}} +
            find $(@D)/{output_dir} -name "*.py" -type f -exec sed -i \
                's|import {package_name}\\.|import external.{namespace}.{app}.|g' {{}} +
            
            # Ensure __init__.py exists
            touch $(@D)/{output_dir}/__init__.py
            touch $(@D)/{output_dir}/api/__init__.py
            touch $(@D)/{output_dir}/models/__init__.py
        """.format(
            output_dir = output_dir,
            spec = spec,
            package_name = package_name.replace("-", "_"),
            namespace = namespace,
            app = app,
        ),
        tools = ["@openapi_generator_cli//file"],
        toolchains = ["@bazel_tools//tools/jdk:current_java_runtime"],
        visibility = ["//visibility:private"],
    )
    
    # Create py_library from generated code
    # Generated clients are standalone and only need basic dependencies
    # that should already be in the project's uv.lock
    native.py_library(
        name = name,
        srcs = [":" + gen_name],
        imports = ["."],  # Make external/ importable
        deps = [
            "@pypi//:pydantic",  # For model validation
            # Note: Generated clients may also use urllib3, python-dateutil, typing-extensions
            # but these will be runtime dependencies, not build-time dependencies
        ],
        visibility = visibility or ["//visibility:public"],
    )
