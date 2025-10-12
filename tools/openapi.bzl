"""Bazel rules for generating OpenAPI specifications from FastAPI apps."""

def openapi_spec(name, app_target, module_path, app_variable="app", **kwargs):
    """
    Generate an OpenAPI specification from a FastAPI application.
    
    This creates a py_binary that imports the FastAPI app and generates its OpenAPI spec.
    
    Args:
        name: Name of the genrule target (will create {name}.json output)
        app_target: Label of the py_library or py_binary containing the FastAPI app
        module_path: Python module path to import (e.g., "demo.hello_fastapi.main")
        app_variable: Name of the FastAPI variable in the module (default: "app")
        **kwargs: Additional arguments passed to py_binary
    
    Example:
        openapi_spec(
            name = "my_api_openapi_spec",
            app_target = ":my_api_lib",
            module_path = "mypackage.api.main",
            app_variable = "app",
        )
    """
    
    # Create a Python script that generates the spec
    script_name = name + "_generator_script"
    script_content = """
import json
import sys
import importlib
import inspect

def main():
    try:
        # Import the module
        module = importlib.import_module("{module_path}")
        
        # Get the FastAPI app or factory function
        app_or_factory = getattr(module, "{app_variable}")
        
        # Check if it's a factory function (not a FastAPI instance)
        # FastAPI instances are callable (ASGI apps), so we need to check the type
        from fastapi import FastAPI
        if isinstance(app_or_factory, FastAPI):
            # It's already a FastAPI instance
            app = app_or_factory
        elif callable(app_or_factory) and inspect.isfunction(app_or_factory):
            # It's a factory function, call it to get the app
            app = app_or_factory()
        else:
            # Assume it's an app instance
            app = app_or_factory
        
        # Generate OpenAPI spec
        spec = app.openapi()
        
        # Write to stdout (will be redirected to file by genrule)
        print(json.dumps(spec, indent=2))
        
    except Exception as e:
        print(f"Error generating OpenAPI spec: {e}", file=sys.stderr)
        import traceback
        traceback.print_exc(file=sys.stderr)
        sys.exit(1)

if __name__ == "__main__":
    main()
""".format(module_path=module_path, app_variable=app_variable)
    
    native.genrule(
        name = script_name,
        outs = [script_name + ".py"],
        cmd = "cat > $@ << 'EOF'\n" + script_content + "\nEOF",
    )
    
    # Create a py_binary that includes the app as a dependency
    generator_name = name + "_generator"
    native.py_binary(
        name = generator_name,
        srcs = [":" + script_name],
        main = script_name + ".py",
        deps = [
            app_target,
            "@pypi//:fastapi",
        ],
        visibility = ["//visibility:private"],
    )
    
    # Run the generator and capture output
    native.genrule(
        name = name,
        outs = [name + ".json"],
        cmd = "$(location :" + generator_name + ") > $@",
        tools = [":" + generator_name],
        visibility = kwargs.get("visibility", ["//visibility:public"]),
        tags = kwargs.get("tags", []) + ["openapi"],
    )
