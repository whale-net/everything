"""Bazel rule implementation for OpenAPI client generation with automatic model discovery."""

def _discover_models_impl(ctx):
    """Extract model names from OpenAPI spec."""
    spec = ctx.file.spec
    output = ctx.outputs.out
    
    ctx.actions.run_shell(
        inputs = [spec],
        outputs = [output],
        command = """
            python3 - "$1" > "$2" <<'PYTHON_SCRIPT'
import sys
import json
import re

spec_file = sys.argv[1]
with open(spec_file, 'r') as f:
    spec = json.load(f)

# Extract all schema names from components/schemas
models = []
if 'components' in spec and 'schemas' in spec['components']:
    for schema_name in spec['components']['schemas'].keys():
        # Convert to snake_case filename (OpenAPI Generator pattern)
        s1 = re.sub('(.)([A-Z][a-z]+)', r'\\1_\\2', schema_name)
        snake_case = re.sub('([a-z0-9])([A-Z])', r'\\1_\\2', s1).lower()
        models.append(snake_case)

# Output one model per line
for model in sorted(models):
    print(model)
PYTHON_SCRIPT
        """,
        arguments = [spec.path, output.path],
    )
    
    return [DefaultInfo(files = depset([output]))]

discover_models = rule(
    implementation = _discover_models_impl,
    attrs = {
        "spec": attr.label(allow_single_file = [".json"], mandatory = True),
        "out": attr.output(mandatory = True),
    },
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
    
    # Step 2: Discover models from spec
    models_file = ctx.actions.declare_file("{}_models.txt".format(app))
    
    ctx.actions.run_shell(
        inputs = [spec],
        outputs = [models_file],
        command = """
            python3 - "$1" > "$2" <<'PYTHON_SCRIPT'
import sys
import json
import re

spec_file = sys.argv[1]
with open(spec_file, 'r') as f:
    spec = json.load(f)

models = []
if 'components' in spec and 'schemas' in spec['components']:
    for schema_name in spec['components']['schemas'].keys():
        s1 = re.sub('(.)([A-Z][a-z]+)', r'\\1_\\2', schema_name)
        snake_case = re.sub('([a-z0-9])([A-Z])', r'\\1_\\2', s1).lower()
        models.append(snake_case)

for model in sorted(models):
    print(model)
PYTHON_SCRIPT
        """,
        arguments = [spec.path, models_file.path],
    )
    
    # Step 3: Extract tar and discover actual files
    # Declare a directory output - this is the key!
    output_tree = ctx.actions.declare_directory(output_dir)
    
    ctx.actions.run_shell(
        inputs = [tar_file, models_file],
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
    
    # The rule already provides PyInfo, so just add deps via a py_library wrapper
    gen_name = name + "_generated"
    
    openapi_client_rule(
        name = gen_name,
        spec = spec,
        namespace = namespace,
        app = app,
        package_name = package_name,
        visibility = ["//visibility:private"],
    )
    
    # Create a py_library that depends on the generated code and adds runtime deps
    native.py_library(
        name = name,
        deps = [
            ":" + gen_name,
            "@pypi//:pydantic",
            "@pypi//:python-dateutil",
            "@pypi//:urllib3",
        ],
        visibility = visibility or ["//visibility:public"],
    )
