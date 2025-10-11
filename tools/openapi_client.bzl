"""Bazel rule for generating OpenAPI Python clients in external/ directory."""

def openapi_client(name, spec, namespace, app, package_name = None, model_files = None, visibility = None):
    """Generate OpenAPI client library in external/{namespace}/{app}/ directory.
    
    Args:
        name: Target name for the generated py_library
        spec: Label pointing to OpenAPI spec JSON file
        namespace: Namespace for grouping (e.g., "manman", "demo")
        app: Application name (e.g., "experience_api", "hello_fastapi")
        package_name: Optional package name for setup.py (defaults to {namespace}-{app})
        model_files: Optional list of model file names (without .py extension) to include.
                     If None, no model files will be declared. 
                     Example: ["user", "order", "product"]
        visibility: Visibility for the generated py_library
    """
    
    if not package_name:
        package_name = "{}-{}".format(namespace, app.replace("_", "-"))
    
    output_dir = "external/{}/{}".format(namespace, app)
    gen_name = name + "_gen"
    tar_name = name + "_tar"
    tar_file = output_dir + ".tar"
    
    # Step 1: Generate client and package as tar
    native.genrule(
        name = tar_name,
        srcs = [spec],
        outs = [tar_file],
        cmd = """
            set -e
            
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
            
            # Verify generation
            if [ ! -d "$$TMPDIR/{package_name}" ]; then
                echo "Error: Generated package directory not found"
                ls -la $$TMPDIR
                exit 1
            fi
            
            # Fix imports to use absolute external.* paths
            # Pattern 1: from package_name.module import ...
            find "$$TMPDIR/{package_name}" -name "*.py" -type f -exec sed -i \
                's|from {package_name}\\.|from external.{namespace}.{app}.|g' {{}} +
            # Pattern 2: import package_name.module
            find "$$TMPDIR/{package_name}" -name "*.py" -type f -exec sed -i \
                's|import {package_name}\\.|import external.{namespace}.{app}.|g' {{}} +
            # Pattern 3: from package_name import module (no dot after package name)
            find "$$TMPDIR/{package_name}" -name "*.py" -type f -exec sed -i \
                's|from {package_name} import|from external.{namespace}.{app} import|g' {{}} +
            # Pattern 4: import package_name (standalone, no dot)
            find "$$TMPDIR/{package_name}" -name "*.py" -type f -exec sed -i \
                's|^import {package_name}$$|import external.{namespace}.{app}|g' {{}} +
            
            # Create tar archive - tar from temp dir but output to absolute path
            tar -cf "$@" -C "$$TMPDIR" {package_name}/
        """.format(
            output_dir = output_dir,
            spec = spec,
            package_name = package_name.replace("-", "_"),
            namespace = namespace,
            app = app,
            tar_file = tar_file,
        ),
        tools = ["@openapi_generator_cli//file"],
        toolchains = ["@bazel_tools//tools/jdk:current_java_runtime"],
        visibility = ["//visibility:private"],
    )
    
    # Step 2: Extract tar to get all Python files
    # Build the outs list dynamically based on model_files parameter
    base_outs = [
        output_dir + "/__init__.py",
        output_dir + "/api/__init__.py",
        output_dir + "/api/default_api.py",
        output_dir + "/api_response.py",
        output_dir + "/api_client.py",
        output_dir + "/configuration.py",
        output_dir + "/exceptions.py",
        output_dir + "/rest.py",
        output_dir + "/models/__init__.py",
    ]
    
    # Add model files if specified
    model_outs = []
    if model_files:
        model_outs = [output_dir + "/models/" + model + ".py" for model in model_files]
    
    all_outs = base_outs + model_outs
    
    native.genrule(
        name = gen_name,
        srcs = [":" + tar_name],
        outs = all_outs,
        cmd = """
            set -e
            OUTPUT_DIR="$$(dirname $(location {output_dir}/__init__.py))"
            mkdir -p "$$OUTPUT_DIR"
            
            # Extract tar
            tar -xf $(location :{tar_name}) -C "$$OUTPUT_DIR" --strip-components=1
            
            # Verify extraction
            if [ ! -f "$$OUTPUT_DIR/__init__.py" ]; then
                echo "Error: Extraction failed"
                ls -la "$$OUTPUT_DIR"
                exit 1
            fi
        """.format(
            output_dir = output_dir,
            tar_name = tar_name,
        ),
        visibility = ["//visibility:private"],
    )
    
    # Step 3: Create py_library from generated code
    native.py_library(
        name = name,
        srcs = [":" + gen_name],
        imports = ["."],  # Make external/ importable
        deps = [
            "@pypi//:pydantic",  # For model validation
            "@pypi//:python-dateutil",  # Required by generated ApiClient
            "@pypi//:urllib3",  # HTTP transport dependency
        ],
        visibility = visibility or ["//visibility:public"],
    )
