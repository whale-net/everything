"""
CLI for generating OpenAPI specs without environment dependencies.
"""

import json
import logging
from pathlib import Path

import typer
from fastapi import FastAPI
from typing_extensions import Annotated

from manman.config import ManManConfig
from manman.logging_config import setup_logging

app = typer.Typer()
logger = logging.getLogger(__name__)


def _generate_openapi_spec(fastapi_app: FastAPI, service_name: str) -> None:
    """Generate and save OpenAPI spec for a FastAPI app."""
    output_path = Path("./openapi-specs")
    output_path.mkdir(exist_ok=True)

    spec = fastapi_app.openapi()
    spec_file = output_path / f"{service_name}.json"
    with open(spec_file, "w") as f:
        json.dump(spec, f, indent=2)

    logger.info(f"OpenAPI spec saved to: {spec_file}")
    typer.echo(f"OpenAPI spec saved to: {spec_file}")


@app.callback(invoke_without_command=True)
def main(
    api_name: Annotated[
        str,
        typer.Argument(
            help=f"Name of the API to generate OpenAPI spec for. Options: {', '.join(ManManConfig.KNOWN_API_NAMES)}"
        ),
    ],
):
    """Generate OpenAPI specification for a specific API without requiring environment setup."""
    # Setup logging
    setup_logging(microservice_name="openapi-generator")
    logger.info(f"Generating OpenAPI spec for {api_name}...")

    # Validate API name
    try:
        validated_api_name = ManManConfig.validate_api_name(api_name)
        # api_config = ManManConfig.get_api_config(validated_api_name)
    except ValueError as e:
        raise typer.BadParameter(str(e))

    # Build FastAPI app based on API
    if validated_api_name == ManManConfig.EXPERIENCE_API:
        from manman.host.api.experience import create_app

        fastapi_app = create_app()

    elif validated_api_name == ManManConfig.STATUS_API:
        from manman.host.api.status import create_app

        fastapi_app = create_app()

    elif validated_api_name == ManManConfig.WORKER_DAL_API:
        from manman.host.api.worker_dal import create_app

        fastapi_app = create_app()

    else:
        raise typer.BadParameter(
            f"Unknown API name: {api_name}. Valid options are: {', '.join(ManManConfig.KNOWN_API_NAMES)}"
        )

    # Generate and save the spec
    _generate_openapi_spec(fastapi_app, api_name)
    logger.info(f"OpenAPI spec generation completed for {api_name}")
