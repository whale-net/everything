"""
Generic OpenAPI specification generator for FastAPI applications.

This module provides a CLI for generating OpenAPI specs from any FastAPI application
by dynamically importing the module and extracting the FastAPI app instance.
"""

import importlib
import json
import logging
import sys
from pathlib import Path

import typer
from typing_extensions import Annotated

app = typer.Typer()
logger = logging.getLogger(__name__)


@app.command()
def main(
    module_path: Annotated[
        str,
        typer.Option(
            "--module-path", "-m",
            help="Python module path to import (e.g., 'demo.hello_fastapi.main')"
        ),
    ],
    app_variable: Annotated[
        str,
        typer.Option(
            "--app-variable", "-a",
            help="Name of the FastAPI app variable in the module (default: 'app')"
        ),
    ] = "app",
    output: Annotated[
        str,
        typer.Option(
            "--output", "-o",
            help="Output file path for the OpenAPI spec JSON"
        ),
    ] = None,
):
    """
    Generate OpenAPI specification from a FastAPI app by importing it dynamically.
    
    Examples:
        # Generate spec for demo/hello_fastapi/main.py with app variable 'app'
        openapi_gen_generic --module-path demo.hello_fastapi.main --app-variable app -o spec.json
        
        # Using module:variable syntax (shorthand)
        openapi_gen_generic --module-path demo.hello_fastapi.main:myapp -o spec.json
    """
    # Setup basic logging
    logging.basicConfig(
        level=logging.INFO,
        format="%(levelname)s: %(message)s"
    )
    
    # Handle module:variable syntax
    if ":" in module_path:
        module_path, app_variable = module_path.split(":", 1)
    
    logger.info(f"Importing module: {module_path}")
    logger.info(f"Looking for FastAPI app variable: {app_variable}")
    
    try:
        # Import the module
        module = importlib.import_module(module_path)
        
        # Get the FastAPI app instance
        if not hasattr(module, app_variable):
            typer.echo(f"Error: Module '{module_path}' has no attribute '{app_variable}'", err=True)
            typer.echo(f"Available attributes: {', '.join(dir(module))}", err=True)
            raise typer.Exit(1)
        
        fastapi_app = getattr(module, app_variable)
        
        # Verify it's a FastAPI app
        from fastapi import FastAPI
        if not isinstance(fastapi_app, FastAPI):
            typer.echo(
                f"Error: '{app_variable}' is not a FastAPI instance (got {type(fastapi_app).__name__})",
                err=True
            )
            raise typer.Exit(1)
        
        logger.info("Successfully imported FastAPI app")
        
        # Generate OpenAPI spec
        spec = fastapi_app.openapi()
        
        # Write to file or stdout
        if output:
            output_path = Path(output)
            output_path.parent.mkdir(parents=True, exist_ok=True)
            with open(output_path, "w") as f:
                json.dump(spec, f, indent=2)
            logger.info(f"OpenAPI spec saved to: {output_path}")
            typer.echo(f"✅ OpenAPI spec saved to: {output_path}")
        else:
            # Output to stdout
            print(json.dumps(spec, indent=2))
            
    except ImportError as e:
        typer.echo(f"Error: Failed to import module '{module_path}': {e}", err=True)
        typer.echo("\nMake sure:", err=True)
        typer.echo("  1. The module path is correct", err=True)
        typer.echo("  2. All dependencies are available", err=True)
        typer.echo("  3. The module is in the Python path", err=True)
        raise typer.Exit(1)
    except Exception as e:
        typer.echo(f"Error: {e}", err=True)
        import traceback
        traceback.print_exc()
        raise typer.Exit(1)


if __name__ == "__main__":
    app()
